"""Shared objective evaluator: the only basis for comparing algorithms (spec §10.5).

Z = travel + lateness + fleet + overtime + unserved (+ stability, Phase 5).

Schedules use earliest times: each vehicle leaves its depot at ``shift_start`` and waits when it
arrives before a window opens. With absolute shift limits this is exact for lateness, overtime
and feasibility; a later departure only shortens idle time and is applied when routes are
published.
"""

from __future__ import annotations

from dataclasses import dataclass

from optiroute.model import UNREACHABLE, Instance, Solution


@dataclass(frozen=True, slots=True)
class StopTiming:
    node: int
    arrival: int
    start: int  # service start
    departure: int
    late: int


@dataclass(frozen=True, slots=True)
class RouteStats:
    vehicle: int
    stops: tuple[StopTiming, ...]
    distance: int  # metres, depot to depot
    finish: int  # arrival time back at the end depot
    duration: int  # finish - shift_start, including waiting
    late: int  # summed lateness, seconds
    overtime: int  # seconds beyond the regular shift end
    weight: int
    volume: int

    @property
    def used(self) -> bool:
        return bool(self.stops)


@dataclass(frozen=True, slots=True)
class CostBreakdown:
    travel: float
    lateness: float
    fleet: float
    overtime: float
    unserved: float
    stability: float = 0.0

    @property
    def total(self) -> float:
        return (
            self.travel + self.lateness + self.fleet + self.overtime + self.unserved
        ) + self.stability


@dataclass(frozen=True, slots=True)
class Evaluation:
    cost: CostBreakdown
    routes: tuple[RouteStats, ...]
    unserved: tuple[int, ...]

    @property
    def distance(self) -> int:
        return sum(r.distance for r in self.routes)

    @property
    def duration(self) -> int:
        return sum(r.duration for r in self.routes if r.used)

    @property
    def vehicles_used(self) -> int:
        return sum(r.used for r in self.routes)

    @property
    def late_stops(self) -> int:
        return sum(1 for r in self.routes for s in r.stops if s.late > 0)


def simulate_route(instance: Instance, vehicle: int, stops: list[int]) -> RouteStats:
    """Earliest-time schedule of one route. Raises ``ValueError`` on an unreachable arc."""
    v = instance.vehicles[vehicle]
    dist, dur = instance.matrix.distance, instance.matrix.duration
    if not stops:
        return RouteStats(vehicle, (), 0, v.shift_start, 0, 0, 0, 0, 0)

    def leg(a: int, b: int) -> tuple[int, int]:
        if dist[a][b] == UNREACHABLE or dur[a][b] == UNREACHABLE:
            raise ValueError(f"unreachable arc {a}->{b} on vehicle {vehicle}")
        return dist[a][b], dur[a][b]

    t, prev, distance, late_sum, weight, volume = v.shift_start, v.start_node, 0, 0, 0, 0
    timings: list[StopTiming] = []
    for node in stops:
        d, travel = leg(prev, node)
        c = instance.customer(node)
        arrival = t + travel
        start = max(arrival, c.tw_start)
        late = max(0, start - c.tw_end)
        t = start + c.service
        timings.append(StopTiming(node, arrival, start, t, late))
        distance += d
        late_sum += late
        weight += c.weight
        volume += c.volume
        prev = node
    d, travel = leg(prev, v.end_node)
    distance += d
    t += travel

    return RouteStats(
        vehicle=vehicle,
        stops=tuple(timings),
        distance=distance,
        finish=t,
        duration=t - v.shift_start,
        late=late_sum,
        overtime=max(0, t - v.regular_end),
        weight=weight,
        volume=volume,
    )


def evaluate(instance: Instance, solution: Solution) -> Evaluation:
    """Cost breakdown of a structurally sound solution (run the validator for feasibility)."""
    w = instance.weights
    stats = tuple(simulate_route(instance, r.vehicle, r.stops) for r in solution.routes)
    unserved = tuple(solution.unserved_nodes(instance))

    travel = fleet = lateness = overtime = 0.0
    for r in stats:
        if not r.used:
            continue
        v = instance.vehicles[r.vehicle]
        travel += v.cost_per_km * r.distance / 1000
        fleet += v.fixed_cost
        overtime += w.overtime_per_min * r.overtime / 60
        for s in r.stops:
            if s.late:
                lateness += w.lateness_per_min * instance.customer(s.node).priority * s.late / 60
    unserved_cost = sum(w.unserved * instance.customer(n).priority for n in unserved)

    return Evaluation(
        cost=CostBreakdown(travel, lateness, fleet, overtime, unserved_cost),
        routes=stats,
        unserved=unserved,
    )
