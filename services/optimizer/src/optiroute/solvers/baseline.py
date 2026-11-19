"""S0: time-oriented nearest-neighbour baseline (Solomon 1987).

Vehicles are filled one at a time. Each step appends the "closest" unrouted customer that keeps
the route feasible (capacity, eligibility, reachability, time window, return before the shift
limit); a route closes when no candidate fits. Closeness follows Solomon's metric, all in seconds:

    score = 0.4 * travel time + 0.4 * (service start - departure) + 0.2 * (window end - arrival)

i.e. near, little waiting, and urgent. Pure distance strands customers on tight-window instances.
Time windows are always treated as hard here, even in SOFT mode: the baseline never trades
lateness for distance.
"""

from __future__ import annotations

from optiroute.model import UNREACHABLE, Instance, Route, Solution
from optiroute.solvers.base import SolveParams, SolverRun

W_TRAVEL, W_GAP, W_URGENCY = 0.4, 0.4, 0.2


def solve(instance: Instance, params: SolveParams | None = None) -> SolverRun:
    dist, dur = instance.matrix.distance, instance.matrix.duration
    unrouted = set(instance.customer_nodes)
    routes: list[Route] = []
    steps = 0

    for k, v in enumerate(instance.vehicles):
        if not unrouted:
            break
        stops: list[int] = []
        prev, clock, weight, volume = v.start_node, v.shift_start, 0, 0
        while True:
            best: tuple[tuple[float, int], int, int] | None = None
            for n in unrouted:
                c = instance.customer(n)
                if (
                    not c.allows(k)
                    or weight + c.weight > v.cap_weight
                    or volume + c.volume > v.cap_volume
                    or UNREACHABLE in (dist[prev][n], dur[prev][n], dist[n][v.end_node])
                ):
                    continue
                arrival = clock + dur[prev][n]
                start = max(arrival, c.tw_start)
                if start > c.tw_end or start + c.service + dur[n][v.end_node] > v.latest_finish:
                    continue
                score = (
                    W_TRAVEL * dur[prev][n]
                    + W_GAP * (start - clock)
                    + W_URGENCY * (c.tw_end - arrival)
                )
                key = (score, n)  # node id breaks ties deterministically
                if best is None or key < best[0]:
                    best = (key, n, start)
            steps += 1
            if best is None:
                break
            _, n, start = best
            c = instance.customer(n)
            stops.append(n)
            unrouted.remove(n)
            prev, clock = n, start + c.service
            weight += c.weight
            volume += c.volume
        if stops:
            routes.append(Route(k, stops))

    return SolverRun(Solution(routes), iterations=steps)
