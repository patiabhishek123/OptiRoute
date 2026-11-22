import datetime as dt

import pytest

from optiroute.simulator import Behaviour, Event, RoutePlan, SimulationError, StopPlan, play
from optiroute.simulator.run import parse_until, request_for
from optiroute.simulator.timeline import haversine_m, route_events, split_legs, timeline

UTC = dt.UTC
DEPOT = (85.0, 20.0)
NORTH_1KM = 1000 / haversine_m((0.0, 0.0), (0.0, 1.0))  # degrees of latitude per km


def at(hhmmss: str) -> dt.datetime:
    return dt.datetime.fromisoformat(f"2026-11-02T{hhmmss}+00:00")


def north(km: float) -> tuple[float, float]:
    return (DEPOT[0], DEPOT[1] + km * NORTH_1KM)


def stop(sid, km, status="PENDING", planned="09:00:00", tw="09:10:00", **kw) -> StopPlan:
    lon, lat = north(km)
    p = at(planned)
    return StopPlan(sid, lat, lon, status, p, p + dt.timedelta(minutes=5), at(tw), **kw)


def route(stops, status="PLANNED", started_at=None) -> RoutePlan:
    # Straight north 1 km, then 0.6 km more, then back: the depot is the start and the end.
    line = [DEPOT, north(1), north(1.6), DEPOT]
    return RoutePlan("r1", "v1", status, stops, line, started_at)


FAST = Behaviour(speed_kmh=36.0, tick_s=30)  # 10 m/s: 1 km takes 100 s


def kinds(events):
    """(kind, time) pairs, leaving out the fixes a vehicle sends while parked at a stop."""
    out, parked = [], False
    for e in events:
        if e.kind == "location" and parked:
            continue
        parked = e.kind == "arrive" or (parked and e.kind != "complete")
        out.append((e.kind, e.at.strftime("%H:%M:%S")))
    return out


def test_split_legs_cuts_at_each_stop_in_order():
    line = [DEPOT, north(1), north(2), north(1), DEPOT]  # passes north(1) twice
    legs = split_legs(line, [north(1), north(2)])
    assert [len(leg) for leg in legs] == [3, 3]
    assert legs[0][:2] == [DEPOT, north(1)]
    assert legs[1][:2] == [north(1), north(2)]


def test_planned_route_drives_waits_for_the_window_and_serves():
    events = route_events(
        route([stop("s1", 1), stop("s2", 1.6, planned="09:16:00", tw="09:00:00")]), FAST
    )
    assert kinds(events) == [
        ("start", "08:58:20"),  # 100 s before the planned 09:00 service start
        ("location", "08:58:20"),
        ("location", "08:58:50"),
        ("location", "08:59:20"),
        ("location", "08:59:50"),
        ("location", "09:00:00"),
        ("arrive", "09:00:00"),
        ("complete", "09:15:00"),  # waited for the 09:10 window, then 5 minutes of service
        ("location", "09:15:30"),
        ("location", "09:16:00"),
        ("arrive", "09:16:00"),
        ("complete", "09:21:00"),
    ]
    fix = events[2]
    assert fix.lat == pytest.approx(north(0.3)[1]) and fix.speed_kmh == 36.0 and fix.heading == 0
    assert events[5].lat == pytest.approx(north(1)[1]) and events[5].speed_kmh == 0
    assert not any(e.failed for e in events)


def test_a_parked_vehicle_keeps_reporting():
    events = route_events(route([stop("s1", 1)]), FAST)
    i, j = (
        next(k for k, e in enumerate(events) if e.kind == kind) for kind in ("arrive", "complete")
    )
    parked = events[i + 1 : j]
    assert len(parked) == 29  # every 30 s from 09:00:30 until service ends at 09:15
    assert {(e.kind, e.lat, e.speed_kmh) for e in parked} == {("location", events[i - 1].lat, 0.0)}


def test_resume_from_an_arrived_stop():
    s1 = stop("s1", 1, status="ARRIVED", actual_arrival=at("09:30:00"))
    events = route_events(route([s1, stop("s2", 1.6)], "IN_PROGRESS", at("08:50:00")), FAST)
    assert events[0].kind == "complete" and events[0].stop_id == "s1"  # no repeated parked fixes
    assert events[0].at == at("09:35:00")
    assert kinds(events)[-2:] == [("arrive", "09:36:00"), ("complete", "09:41:00")]


def test_resume_after_a_finished_stop():
    s1 = stop(
        "s1",
        1,
        status="COMPLETED",
        actual_arrival=at("09:30:00"),
        actual_departure=at("09:40:00"),
    )
    events = route_events(route([s1, stop("s2", 1.6)], "IN_PROGRESS", at("08:50:00")), FAST)
    assert [e for e in events if e.stop_id == "s1"] == []
    assert kinds(events)[-2:] == [("arrive", "09:41:00"), ("complete", "09:46:00")]


def test_resume_a_started_route_without_progress_from_its_start():
    events = route_events(route([stop("s1", 1)], "IN_PROGRESS", at("08:00:00")), FAST)
    assert kinds(events)[-2:] == [("arrive", "08:01:40"), ("complete", "09:15:00")]
    with pytest.raises(ValueError, match="resume"):
        route_events(route([stop("s1", 1)], "IN_PROGRESS", None), FAST)


def test_finished_routes_have_nothing_left():
    assert route_events(route([stop("s1", 1)], "COMPLETED"), FAST) == []


def test_failures_and_jitter_are_seeded():
    r = route([stop("s1", 1), stop("s2", 1.6, planned="09:16:00", tw="09:00:00")])
    always = Behaviour(speed_kmh=36, tick_s=30, fail_rate=1.0)
    assert all(e.failed for e in route_events(r, always) if e.kind == "complete")
    noisy = Behaviour(speed_kmh=36, tick_s=30, jitter=0.3, seed=7)
    assert route_events(r, noisy) == route_events(r, noisy)
    assert route_events(r, noisy) != route_events(r, Behaviour(36, 30, 0.3, seed=8))


def test_timeline_merges_routes_by_time():
    a = route([stop("s1", 1)])
    b = RoutePlan("r2", "v2", "PLANNED", [stop("t1", 1.6, planned="08:59:00")], a.line)
    events = timeline([a, b], FAST)
    assert [e.at for e in events] == sorted(e.at for e in events)
    assert {e.route_id for e in events} == {"r1", "r2"}


def test_behaviour_rejects_nonsense():
    with pytest.raises(ValueError):
        Behaviour(speed_kmh=0)
    with pytest.raises(ValueError):
        Behaviour(jitter=1.0)


# ---------------------------------------------------------------- playback


def ev(t: str, kind="location", route_id="r1", **kw) -> Event:
    return Event(at(t), 0, route_id, "v1", kind, lat=20.0, lon=85.0, **kw)


class Clock:
    def __init__(self, now: dt.datetime):
        self.now, self.real, self.sleeps = now, 0.0, []

    def sleep(self, s: float) -> None:
        self.sleeps.append(round(s, 3))
        self.real += s
        self.now += dt.timedelta(seconds=s)


def run(events, clock, sent=None, status=200, **kw):
    sent = [] if sent is None else sent

    def send(e):
        sent.append(e)
        return (status(e) if callable(status) else status), {"error": {"code": "X"}}

    stats = play(
        events,
        send,
        now=lambda: clock.now,
        sleep=clock.sleep,
        monotonic=lambda: clock.real,
        **kw,
    )
    return stats, sent


def test_play_paces_by_speed_up():
    clock = Clock(at("23:00:00"))  # the day is in the past: only the speed-up paces
    events = [ev("09:00:00"), ev("09:01:00"), ev("09:03:00")]
    stats, sent = run(events, clock, speed_up=60)
    assert sent == events and stats.sent == 3
    assert clock.sleeps == [1.0, 2.0]  # one simulated minute per real second


def test_play_never_runs_ahead_of_now():
    clock = Clock(at("09:00:30"))
    _, sent = run([ev("09:00:00"), ev("09:01:00")], clock, speed_up=0)
    assert len(sent) == 2 and clock.sleeps == [30.0]


def test_play_refuses_a_future_plan():
    with pytest.raises(SimulationError, match="future"):
        run([ev("10:00:00")], Clock(at("09:00:00")), speed_up=0)


def test_play_stops_at_until_and_counts_outcomes():
    events = [
        ev("09:00:00", "arrive", stop_id="s1"),
        ev("09:05:00", "complete", stop_id="s1"),
        ev("09:10:00", "arrive", stop_id="s2"),
        ev("09:15:00", "complete", stop_id="s2", failed=True),
        ev("12:00:01", "arrive", stop_id="s3"),
    ]
    stats, sent = run(events, Clock(at("23:00:00")), speed_up=0, until=at("12:00:00"))
    assert len(sent) == 4 and stats.delivered == 1 and stats.failed == 1


def test_a_failed_call_stops_that_route_only():
    events = [ev("09:00:00", "start"), ev("09:01:00"), ev("09:02:00", route_id="r2")]
    stats, sent = run(
        events,
        Clock(at("23:00:00")),
        speed_up=0,
        status=lambda e: 409 if e.kind == "start" else 201,
    )
    assert [e.route_id for e in sent] == ["r1", "r2"]
    assert stats.broken_routes == {"r1"} and len(stats.errors) == 1 and "409 X" in stats.errors[0]


def test_requests_match_the_tracking_api():
    path, body = request_for(ev("09:00:00", "complete", stop_id="s1", failed=True))
    assert path == "/routes/r1/stops/s1/complete"
    assert body["status"] == "FAILED" and body["failure_reason"]
    path, body = request_for(ev("09:00:00", speed_kmh=30.0, heading=90.0))
    assert path == "/vehicles/v1/location"
    assert body == {
        "lat": 20.0,
        "lon": 85.0,
        "speed_kmh": 30.0,
        "heading": 90.0,
        "recorded_at": "2026-11-02T09:00:00+00:00",
    }


def test_parse_until():
    day = dt.date(2026, 11, 2)
    assert parse_until("12:00", day, "Asia/Kolkata") == at("06:30:00")
    assert parse_until("2026-11-02T12:00:00+05:30", day, "UTC") == at("06:30:00")
    assert parse_until(None, day, "UTC") is None
    with pytest.raises(SimulationError):
        parse_until("2026-11-02T12:00:00", day, "UTC")
