"""S1: Clarke-Wright parallel savings for the heterogeneous, multi-depot VRPTW.

1. Cluster: each customer joins the nearest depot that has a vehicle allowed to serve it.
2. Merge, per depot: start with one route per customer, then merge route ends in decreasing order
   of directional savings ``s_ij = d(i,0) + d(0,j) - d(i,j)``, plus the fixed cost of the vehicle a
   merge frees, expressed in metres. A merge appends the route starting at ``j`` to the route
   ending at ``i``. It is checked in O(1) against the depot's most permissive vehicle, using the
   cached earliest departure from ``i`` and the latest feasible service start at ``j``.
3. Assign: routes, heaviest first, go to the cheapest unused real vehicle that can actually run
   them. This is re-checked with the shared evaluator's schedule.
4. Repair: leftover customers get cheapest feasible insertion, possibly opening an unused vehicle.

Like S0, time windows are treated as hard even in SOFT mode.
"""

from __future__ import annotations

from dataclasses import dataclass

from optiroute.model import UNREACHABLE, Instance, Route, Solution
from optiroute.solvers.base import SolveParams, SolverRun
from optiroute.validation.evaluate import RouteStats, simulate_route

INFEASIBLE = None


@dataclass(frozen=True, slots=True)
class _Profile:
    """Loosest bounds over a depot's vehicles; real vehicles are checked again at assignment."""

    depot: int
    cap_weight: int
    cap_volume: int
    shift_start: int
    latest_finish: int
    fixed_in_metres: int  # fixed cost of one vehicle expressed as distance


@dataclass(slots=True)
class _Partial:
    stops: list[int]
    weight: int
    volume: int
    allowed: frozenset[int]  # vehicles that may serve every stop
    depart_last: int  # earliest departure from the last stop
    latest_first: int  # latest service start at the first stop keeping the rest feasible


def solve(instance: Instance, params: SolveParams | None = None) -> SolverRun:
    groups, leftovers = _cluster(instance)
    routes: list[Route] = []
    used: set[int] = set()
    merges = 0
    for depot, customers in sorted(groups.items()):
        fleet = [k for k, v in enumerate(instance.vehicles) if v.start_node == depot]
        profile = _profile(instance, depot, fleet)
        partials, infeasible, n_merges = _merge(instance, profile, customers, frozenset(fleet))
        merges += n_merges
        leftovers += infeasible
        assigned, unassigned = _assign(instance, partials, fleet, used)
        routes += assigned
        leftovers += unassigned
    # Tightest windows first: they have the fewest feasible insertion points.
    window = {n: instance.customer(n).tw_end - instance.customer(n).tw_start for n in leftovers}
    _repair(instance, routes, sorted(leftovers, key=lambda n: (window[n], n)), used)
    return SolverRun(Solution(routes), iterations=merges)


# ------------------------------------------------------------------ clustering


def _cluster(instance: Instance) -> tuple[dict[int, list[int]], list[int]]:
    dist = instance.matrix.distance
    groups: dict[int, list[int]] = {}
    unservable: list[int] = []
    for n in instance.customer_nodes:
        c = instance.customer(n)
        depots = {v.start_node for k, v in enumerate(instance.vehicles) if c.allows(k)}
        reachable = [d for d in depots if UNREACHABLE not in (dist[d][n], dist[n][d])]
        if not reachable:
            unservable.append(n)
            continue
        depot = min(reachable, key=lambda d: (dist[d][n] + dist[n][d], d))
        groups.setdefault(depot, []).append(n)
    return groups, unservable


def _profile(instance: Instance, depot: int, fleet: list[int]) -> _Profile:
    vs = [instance.vehicles[k] for k in fleet]
    cheapest_km = min(v.cost_per_km for v in vs)
    cheapest_fixed = min(v.fixed_cost for v in vs)
    fixed_m = round(cheapest_fixed / cheapest_km * 1000) if cheapest_km > 0 else 0
    return _Profile(
        depot=depot,
        cap_weight=max(v.cap_weight for v in vs),
        cap_volume=max(v.cap_volume for v in vs),
        shift_start=min(v.shift_start for v in vs),
        latest_finish=max(v.latest_finish for v in vs),
        fixed_in_metres=fixed_m,
    )


# ------------------------------------------------------------------ savings merge


def _merge(
    instance: Instance, p: _Profile, customers: list[int], fleet: frozenset[int]
) -> tuple[list[_Partial], list[int], int]:
    dist, dur = instance.matrix.distance, instance.matrix.duration
    route_of: dict[int, _Partial] = {}
    infeasible: list[int] = []
    for n in customers:
        partial = _partial(instance, p, [n], fleet)
        if partial is INFEASIBLE:
            infeasible.append(n)
        else:
            route_of[n] = partial

    nodes = sorted(route_of)
    savings = []
    for i in nodes:
        for j in nodes:
            if i == j or UNREACHABLE in (dist[i][j], dur[i][j]):
                continue
            s = dist[i][p.depot] + dist[p.depot][j] - dist[i][j] + p.fixed_in_metres
            if s > 0:
                savings.append((-s, i, j))
    savings.sort()

    merges = 0
    for _, i, j in savings:
        a, b = route_of[i], route_of[j]
        if (
            a is b
            or a.stops[-1] != i
            or b.stops[0] != j
            or a.weight + b.weight > p.cap_weight
            or a.volume + b.volume > p.cap_volume
            or not (a.allowed & b.allowed)
            or a.depart_last + dur[i][j] > b.latest_first
        ):
            continue
        merged = _partial(instance, p, a.stops + b.stops, a.allowed & b.allowed)
        if merged is INFEASIBLE:  # cannot happen if the O(1) test is right; stay safe
            continue
        for n in merged.stops:
            route_of[n] = merged
        merges += 1

    unique = {id(r): r for r in route_of.values()}
    return list(unique.values()), infeasible, merges


def _partial(
    instance: Instance, p: _Profile, stops: list[int], allowed: frozenset[int]
) -> _Partial | None:
    """Earliest-departure / latest-start summary of ``stops`` under profile ``p``."""
    dur = instance.matrix.duration
    customers = [instance.customer(n) for n in stops]
    allowed = frozenset(k for k in allowed if all(c.allows(k) for c in customers))
    if not allowed:
        return INFEASIBLE

    clock, prev = p.shift_start, p.depot
    for n, c in zip(stops, customers, strict=True):
        start = max(clock + dur[prev][n], c.tw_start)
        if start > c.tw_end:
            return INFEASIBLE
        clock, prev = start + c.service, n
    if clock + dur[prev][p.depot] > p.latest_finish:
        return INFEASIBLE

    latest = min(
        customers[-1].tw_end, p.latest_finish - dur[stops[-1]][p.depot] - customers[-1].service
    )
    for idx in range(len(stops) - 2, -1, -1):
        c = customers[idx]
        latest = min(c.tw_end, latest - dur[stops[idx]][stops[idx + 1]] - c.service)

    return _Partial(
        stops=stops,
        weight=sum(c.weight for c in customers),
        volume=sum(c.volume for c in customers),
        allowed=allowed,
        depart_last=clock,
        latest_first=latest,
    )


# ------------------------------------------------------------------ assignment and repair


def _run(instance: Instance, k: int, stops: list[int]) -> RouteStats | None:
    """Schedule ``stops`` on vehicle ``k``; None if any hard constraint fails."""
    v = instance.vehicles[k]
    customers = [instance.customer(n) for n in stops]
    if (
        sum(c.weight for c in customers) > v.cap_weight
        or sum(c.volume for c in customers) > v.cap_volume
        or not all(c.allows(k) for c in customers)
    ):
        return None
    try:
        stats = simulate_route(instance, k, stops)
    except ValueError:  # unreachable arc
        return None
    if stats.late or stats.finish > v.latest_finish:
        return None
    return stats


def _cost(instance: Instance, k: int, stats: RouteStats) -> float:
    v = instance.vehicles[k]
    return v.cost_per_km * stats.distance / 1000 + (v.fixed_cost if stats.used else 0.0)


def _assign(
    instance: Instance, partials: list[_Partial], fleet: list[int], used: set[int]
) -> tuple[list[Route], list[int]]:
    routes: list[Route] = []
    leftovers: list[int] = []
    # Longest routes first: if routes outnumber vehicles, the leftovers are short and easy to
    # re-insert. Ties go to the heavier route, which needs the larger vehicles.
    for part in sorted(partials, key=lambda r: (-len(r.stops), -r.weight, r.stops[0])):
        best: tuple[float, int] | None = None
        for k in fleet:
            if k in used or k not in part.allowed:
                continue
            stats = _run(instance, k, part.stops)
            if stats is not None and (best is None or (_cost(instance, k, stats), k) < best):
                best = (_cost(instance, k, stats), k)
        if best is None:
            leftovers += part.stops
        else:
            used.add(best[1])
            routes.append(Route(best[1], part.stops))
    return routes, leftovers


def _repair(instance: Instance, routes: list[Route], leftovers: list[int], used: set[int]) -> None:
    """Cheapest feasible insertion of leftovers, in place; unplaceable customers stay unserved."""
    for n in leftovers:
        best: tuple[float, int, int, int] | None = None  # (delta, route idx or -1, vehicle, pos)
        for idx, route in enumerate(routes):
            base = _run(instance, route.vehicle, route.stops)
            if base is None:
                continue
            base_cost = _cost(instance, route.vehicle, base)
            for pos in range(len(route.stops) + 1):
                stops = [*route.stops[:pos], n, *route.stops[pos:]]
                stats = _run(instance, route.vehicle, stops)
                if stats is None:
                    continue
                cand = (_cost(instance, route.vehicle, stats) - base_cost, idx, route.vehicle, pos)
                best = cand if best is None or cand < best else best
        for k in range(len(instance.vehicles)):
            if k in used:
                continue
            stats = _run(instance, k, [n])
            if stats is not None:
                cand = (_cost(instance, k, stats), -1, k, 0)
                best = cand if best is None or cand < best else best
        if best is None:
            continue
        _, idx, k, pos = best
        if idx == -1:
            used.add(k)
            routes.append(Route(k, [n]))
        else:
            routes[idx].stops.insert(pos, n)
