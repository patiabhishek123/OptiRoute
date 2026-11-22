"""Turn an active plan into a timeline of driving events (pure: no I/O, no clock).

Each route drives its road geometry at a configurable speed, emitting a GPS fix every ``tick_s``
simulated seconds, then arrives at a stop, waits for the window to open, serves it for the planned
service time and completes it (or fails it with probability ``fail_rate``). Routes that already
have progress resume from it: a finished stop's departure, an ARRIVED stop, or the route start.
"""

from __future__ import annotations

import datetime as dt
import math
import random
from bisect import bisect_right
from dataclasses import dataclass
from itertools import accumulate, pairwise
from typing import Literal

EARTH_RADIUS_M = 6_371_008.8
FINISHED = frozenset({"COMPLETED", "FAILED"})

Point = tuple[float, float]  # (lon, lat), the GeoJSON / OSRM order


@dataclass(frozen=True, slots=True)
class StopPlan:
    id: str
    lat: float
    lon: float
    status: str
    planned_arrival: dt.datetime  # service start (the plan never starts service before the window)
    planned_departure: dt.datetime
    tw_start: dt.datetime
    actual_arrival: dt.datetime | None = None
    actual_departure: dt.datetime | None = None

    @property
    def service(self) -> dt.timedelta:
        return self.planned_departure - self.planned_arrival


@dataclass(frozen=True, slots=True)
class RoutePlan:
    id: str
    vehicle_id: str
    status: str  # PLANNED | IN_PROGRESS | COMPLETED | CANCELLED
    stops: list[StopPlan]
    line: list[Point]  # depot -> stops -> depot
    started_at: dt.datetime | None = None


@dataclass(frozen=True, slots=True)
class Behaviour:
    speed_kmh: float = 35.0
    tick_s: int = 60
    jitter: float = 0.0  # per-leg speed varies uniformly within ±jitter (0.2 = ±20%)
    fail_rate: float = 0.0  # probability that a delivery fails
    seed: int = 0

    def __post_init__(self) -> None:
        if self.speed_kmh <= 0 or self.tick_s <= 0:
            raise ValueError("speed_kmh and tick_s must be positive")
        if not 0 <= self.jitter < 1 or not 0 <= self.fail_rate <= 1:
            raise ValueError("jitter must be in [0, 1) and fail_rate in [0, 1]")


Kind = Literal["start", "location", "arrive", "complete"]


@dataclass(frozen=True, slots=True)
class Event:
    at: dt.datetime
    seq: int  # position within its route: keeps emission order on equal times
    route_id: str
    vehicle_id: str
    kind: Kind
    stop_id: str | None = None
    lat: float | None = None
    lon: float | None = None
    speed_kmh: float | None = None
    heading: float | None = None
    failed: bool = False


# ---------------------------------------------------------------- geometry


def haversine_m(a: Point, b: Point) -> float:
    lon1, lat1, lon2, lat2 = map(math.radians, (*a, *b))
    h = (
        math.sin((lat2 - lat1) / 2) ** 2
        + math.cos(lat1) * math.cos(lat2) * math.sin((lon2 - lon1) / 2) ** 2
    )
    return 2 * EARTH_RADIUS_M * math.asin(math.sqrt(h))


def bearing(a: Point, b: Point) -> float:
    """Initial compass bearing from a to b, in [0, 360)."""
    lon1, lat1, lon2, lat2 = map(math.radians, (*a, *b))
    y = math.sin(lon2 - lon1) * math.cos(lat2)
    x = math.cos(lat1) * math.sin(lat2) - math.sin(lat1) * math.cos(lat2) * math.cos(lon2 - lon1)
    return math.degrees(math.atan2(y, x)) % 360


def split_legs(line: list[Point], stops: list[Point]) -> list[list[Point]]:
    """Cut the route line at each stop, in order: leg i ends at stop i.

    Each stop maps to the nearest line vertex at or after the previous cut (the earliest on ties),
    so a route that passes a stop again on the way back is still cut at the right visit. The leg's
    last point is the stop itself (OSRM snaps waypoints onto the road).
    """
    legs, start = [], 0
    for stop in stops:
        cut = min(range(start, len(line)), key=lambda i: (haversine_m(line[i], stop), i))
        legs.append([*line[start : cut + 1], stop])
        start = cut
    return legs


def _position(leg: list[Point], cum: list[float], d: float) -> tuple[Point, float]:
    """Point and heading at distance d along the leg."""
    i = min(max(bisect_right(cum, d) - 1, 0), len(leg) - 2)
    a, b = leg[i], leg[i + 1]
    seg = cum[i + 1] - cum[i]
    f = 0.0 if seg == 0 else min(max((d - cum[i]) / seg, 0.0), 1.0)
    return (a[0] + (b[0] - a[0]) * f, a[1] + (b[1] - a[1]) * f), bearing(a, b)


# ---------------------------------------------------------------- timeline


def route_events(route: RoutePlan, behaviour: Behaviour) -> list[Event]:
    """Every remaining event of one route, in time order."""
    if route.status not in ("PLANNED", "IN_PROGRESS") or not route.stops:
        return []
    rng = random.Random(f"{behaviour.seed}/{route.id}")  # independent per route, reproducible
    legs = split_legs(route.line, [(s.lon, s.lat) for s in route.stops])
    nominal_mps = behaviour.speed_kmh / 3.6
    out: list[Event] = []

    def emit(at: dt.datetime, kind: Kind, **kw) -> None:
        out.append(Event(at, len(out), route.id, route.vehicle_id, kind, **kw))

    def serve(i: int, arrived: dt.datetime, *, report: bool = True) -> dt.datetime:
        """Wait for the window, serve, complete. A parked vehicle keeps reporting its position,
        except on resume, where those fixes were sent by the previous run."""
        stop = route.stops[i]
        departure = max(arrived, stop.tw_start) + stop.service
        tick = dt.timedelta(seconds=behaviour.tick_s)
        t = arrived + tick
        while report and t < departure - dt.timedelta(seconds=0.5):
            emit(t, "location", lat=stop.lat, lon=stop.lon, speed_kmh=0.0)
            t += tick
        emit(departure, "complete", stop_id=stop.id, failed=rng.random() < behaviour.fail_rate)
        return departure

    # Where the vehicle is now.
    finished = [i for i, s in enumerate(route.stops) if s.status in FINISHED]
    arrived = next((i for i, s in enumerate(route.stops) if s.status == "ARRIVED"), None)
    if route.status == "PLANNED":
        first = route.stops[0]
        leg_m = sum(haversine_m(a, b) for a, b in pairwise(legs[0]))
        clock = first.planned_arrival - dt.timedelta(seconds=leg_m / nominal_mps)
        depot = legs[0][0]
        emit(clock, "start")
        emit(clock, "location", lat=depot[1], lon=depot[0], speed_kmh=0.0)
        nxt = 0
    elif arrived is not None:
        clock = serve(arrived, route.stops[arrived].actual_arrival, report=False)
        nxt = arrived + 1
    elif finished:
        last = route.stops[max(finished)]
        clock = last.actual_departure
        nxt = max(finished) + 1
    else:
        clock = route.started_at
        nxt = 0
    if clock is None:
        raise ValueError(f"route {route.id} is IN_PROGRESS without the times to resume from")

    for i in range(nxt, len(route.stops)):
        if route.stops[i].status != "PENDING":
            continue  # finished out of sequence
        leg = legs[i]
        cum = list(accumulate((haversine_m(a, b) for a, b in pairwise(leg)), initial=0.0))
        speed = nominal_mps * (1 + rng.uniform(-behaviour.jitter, behaviour.jitter))
        drive = cum[-1] / speed
        t = behaviour.tick_s
        while t < drive - 0.5:  # the arrival emits its own fix
            (lon, lat), head = _position(leg, cum, t * speed)
            emit(
                clock + dt.timedelta(seconds=t),
                "location",
                lat=lat,
                lon=lon,
                speed_kmh=round(speed * 3.6, 1),
                heading=round(head, 1) % 360,
            )
            t += behaviour.tick_s
        clock += dt.timedelta(seconds=drive)
        stop = route.stops[i]
        emit(clock, "location", lat=stop.lat, lon=stop.lon, speed_kmh=0.0)
        emit(clock, "arrive", stop_id=stop.id)
        clock = serve(i, clock)
    return out


def timeline(routes: list[RoutePlan], behaviour: Behaviour) -> list[Event]:
    """All routes' events merged by time (a route's own order is kept on ties)."""
    events = [e for r in routes for e in route_events(r, behaviour)]
    return sorted(events, key=lambda e: (e.at, e.route_id, e.seq))
