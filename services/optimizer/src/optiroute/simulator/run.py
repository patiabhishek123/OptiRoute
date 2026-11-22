"""Drive the ACTIVE plan through the public tracking API (spec §12.6).

The simulator is an ordinary API client: it reads the plan and its road geometry, builds the
timeline (:mod:`optiroute.simulator.timeline`) and sends each event as the driver app would.
Simulated time runs ``speed_up`` times faster than real time but never ahead of the present,
because the API rejects timestamps from the future: a past day replays at full speed, today's plan
catches up with now and then runs in real time. ``until`` stops the replay at a simulated time,
and running again resumes from the progress recorded so far.
"""

from __future__ import annotations

import datetime as dt
import json
import sys
import time
import urllib.error
import urllib.request
from collections.abc import Callable
from dataclasses import dataclass, field
from typing import Any
from zoneinfo import ZoneInfo

from optiroute.simulator.timeline import Behaviour, Event, RoutePlan, StopPlan, timeline

FAILURE_REASON = "customer not available (simulated)"


class SimulationError(Exception):
    pass


class Api:
    """Minimal JSON client for the public API; refreshes the access token once on a 401."""

    def __init__(self, base_url: str, access: str, refresh: str | None = None) -> None:
        self.base_url = base_url.rstrip("/")
        self.access, self.refresh = access, refresh

    @classmethod
    def login(cls, base_url: str, org: str, email: str, password: str) -> Api:
        api = cls(base_url, "")
        status, body = api.call(
            "POST", "/auth/login", {"organization": org, "email": email, "password": password}
        )
        if status != 200:
            raise SimulationError(f"login failed ({status}): {body}")
        return cls(base_url, body["access_token"], body["refresh_token"])

    def call(self, method: str, path: str, body: Any = None, *, retry: bool = True):
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(self.base_url + path, data=data, method=method)
        req.add_header("Content-Type", "application/json")
        if self.access:
            req.add_header("Authorization", "Bearer " + self.access)
        try:
            with urllib.request.urlopen(req, timeout=30) as r:
                return r.status, json.loads(r.read() or b"null")
        except urllib.error.HTTPError as e:
            if e.code == 401 and retry and self.refresh:
                s, tokens = self.call(
                    "POST", "/auth/refresh", {"refresh_token": self.refresh}, retry=False
                )
                if s == 200:
                    self.access, self.refresh = tokens["access_token"], tokens["refresh_token"]
                    return self.call(method, path, body, retry=False)
            return e.code, json.loads(e.read() or b"null")

    def get(self, path: str) -> Any:
        status, body = self.call("GET", path)
        if status != 200:
            raise SimulationError(f"GET {path} failed ({status}): {body}")
        return body


def _time(v: str | None) -> dt.datetime | None:
    return dt.datetime.fromisoformat(v) if v else None


def active_plan_id(api: Api, planning_date: dt.date) -> str:
    plans = api.get(f"/route-plans?planning_date={planning_date.isoformat()}&status=ACTIVE")
    if not plans["items"]:
        raise SimulationError(f"no ACTIVE plan on {planning_date}")
    return plans["items"][0]["id"]


def load_routes(api: Api, plan_id: str) -> tuple[dict, list[RoutePlan]]:
    plan = api.get(f"/route-plans/{plan_id}")
    if plan["status"] != "ACTIVE":
        raise SimulationError(f"plan {plan_id} is {plan['status']}; only the ACTIVE plan is driven")
    lines = {
        g["route_id"]: [tuple(p) for p in g["coordinates"]]
        for g in api.get(f"/route-plans/{plan_id}/geometry")["routes"]
    }
    routes = []
    for r in plan["routes"]:
        started = None
        if r["status"] == "IN_PROGRESS":
            started = _time(api.get(f"/routes/{r['id']}/progress")["started_at"])
        stops = [
            StopPlan(
                id=s["id"],
                lat=s["lat"],
                lon=s["lon"],
                status=s["status"],
                planned_arrival=_time(s["planned_arrival"]),
                planned_departure=_time(s["planned_departure"]),
                tw_start=_time(s["time_window_start"]),
                actual_arrival=_time(s["actual_arrival"]),
                actual_departure=_time(s["actual_departure"]),
            )
            for s in r["stops"]
        ]
        routes.append(
            RoutePlan(r["id"], r["vehicle_id"], r["status"], stops, lines[r["id"]], started)
        )
    return plan, routes


def request_for(e: Event) -> tuple[str, dict]:
    at = e.at.isoformat()
    match e.kind:
        case "start":
            return f"/routes/{e.route_id}/start", {"at": at}
        case "location":
            body = {"lat": e.lat, "lon": e.lon, "speed_kmh": e.speed_kmh, "recorded_at": at}
            if e.heading is not None:
                body["heading"] = e.heading
            return f"/vehicles/{e.vehicle_id}/location", body
        case "arrive":
            return f"/routes/{e.route_id}/stops/{e.stop_id}/arrive", {"at": at}
        case "complete":
            if e.failed:
                body = {"at": at, "status": "FAILED", "failure_reason": FAILURE_REASON}
            else:
                body = {"at": at}
            return f"/routes/{e.route_id}/stops/{e.stop_id}/complete", body
    raise ValueError(e.kind)


@dataclass
class Stats:
    sent: int = 0
    delivered: int = 0
    failed: int = 0
    errors: list[str] = field(default_factory=list)
    broken_routes: set[str] = field(default_factory=set)
    last: dt.datetime | None = None


def play(
    events: list[Event],
    send: Callable[[Event], tuple[int, Any]],
    *,
    speed_up: float,
    until: dt.datetime | None = None,
    now: Callable[[], dt.datetime] = lambda: dt.datetime.now(dt.UTC),
    sleep: Callable[[float], None] = time.sleep,
    monotonic: Callable[[], float] = time.monotonic,
    log: Callable[[str], None] = lambda _: None,
    tz: dt.tzinfo = dt.UTC,
) -> Stats:
    """Send events in order, paced by ``speed_up`` (0 = as fast as possible), never in the future.

    A route whose call fails stops there: its later events would only fail too.
    """
    stats = Stats()
    events = [e for e in events if until is None or e.at <= until]
    if not events:
        return stats
    if events[0].at > now() + dt.timedelta(minutes=1):
        raise SimulationError(
            f"the plan starts at {events[0].at.isoformat()}, in the future; the tracking API only "
            "accepts timestamps up to now"
        )
    sim0, real0 = events[0].at, monotonic()
    for e in events:
        if e.route_id in stats.broken_routes:
            continue
        if speed_up > 0:
            wait = (e.at - sim0).total_seconds() / speed_up - (monotonic() - real0)
            if wait > 0:
                sleep(wait)
        if (ahead := (e.at - now()).total_seconds()) > 0:
            sleep(ahead)
        status, body = send(e)
        stats.sent += 1
        stats.last = e.at
        if not 200 <= status < 300:
            code = (body or {}).get("error", {}).get("code", "") if isinstance(body, dict) else ""
            stats.errors.append(f"{e.kind} route={e.route_id} stop={e.stop_id}: {status} {code}")
            stats.broken_routes.add(e.route_id)
            log(f"{e.at.astimezone(tz):%H:%M} ERROR {stats.errors[-1]}")
            continue
        if e.kind == "complete":
            stats.failed += e.failed
            stats.delivered += not e.failed
        if e.kind in ("start", "arrive", "complete"):
            what = "FAILED" if e.failed else e.kind
            stop = e.stop_id[:8] if e.stop_id else ""
            log(f"{e.at.astimezone(tz):%H:%M} {what:<8} route {e.route_id[:8]} {stop}")
    return stats


def parse_until(value: str | None, planning_date: dt.date, tz: str) -> dt.datetime | None:
    """``HH:MM`` on the planning date in the organization's timezone, or an ISO timestamp."""
    if not value:
        return None
    try:
        t = dt.time.fromisoformat(value)
        return dt.datetime.combine(planning_date, t, tzinfo=ZoneInfo(tz))
    except ValueError:
        pass
    at = dt.datetime.fromisoformat(value)
    if at.tzinfo is None:
        raise SimulationError("--until needs HH:MM or a timestamp with a UTC offset")
    return at


def simulate(
    api: Api,
    *,
    planning_date: dt.date | None,
    plan_id: str | None,
    behaviour: Behaviour,
    speed_up: float,
    until: str | None,
    log: Callable[[str], None] = lambda m: print(m, file=sys.stderr),
) -> Stats:
    if not plan_id:
        if planning_date is None:
            raise SimulationError("give a plan id or a planning date")
        plan_id = active_plan_id(api, planning_date)
    plan, routes = load_routes(api, plan_id)
    tz = api.get("/organizations/me")["organization"]["timezone"]
    day = dt.date.fromisoformat(plan["planning_date"])
    cutoff = parse_until(until, day, tz)
    events = [e for e in timeline(routes, behaviour) if cutoff is None or e.at <= cutoff]
    log(f"plan {plan_id} ({day}): {len(routes)} routes, {len(events)} events to send")

    def send(e: Event) -> tuple[int, Any]:
        path, body = request_for(e)
        return api.call("POST", path, body)

    stats = play(
        events,
        send,
        speed_up=speed_up,
        until=cutoff,
        log=log,
        tz=ZoneInfo(tz),
    )
    log(
        f"sent {stats.sent} events up to {stats.last.isoformat() if stats.last else '-'}: "
        f"{stats.delivered} delivered, {stats.failed} failed, {len(stats.errors)} errors"
    )
    return stats
