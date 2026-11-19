"""S2: OR-Tools routing with guided local search, warm-started from Clarke-Wright (S1).

Model mapping (docs/ARCHITECTURE.md §5). Objective units are milli-currency (``COST_SCALE``):

* arc cost: one node-indexed transit matrix per distinct ``cost_per_km``; fixed vehicle cost
* weight and volume: capacity dimensions
* time: transit = service(from) + travel(from, to); slack = waiting. HARD windows are cumul
  ranges, SOFT windows are soft upper bounds. Shift: hard end at ``latest_finish``, overtime is a
  soft upper bound at ``regular_end``
* unserved: a disjunction per customer with penalty ``unserved * priority``. It is used even when
  ``allow_unassigned`` is false, so the solver returns its best partial plan and the validator
  reports the result as INFEASIBLE instead of failing silently
* eligibility: the node's ``VehicleVar`` domain; ``UNREACHABLE`` arcs are removed from ``NextVar``

Reproducibility: OR-Tools has no random seed. With the same inputs the search is deterministic,
but a time limit stops it at a machine-dependent point, so compare runs on one machine (spec §10.5).
"""

from __future__ import annotations

from collections import defaultdict

from ortools.constraint_solver import pywrapcp, routing_enums_pb2

from optiroute.model import UNREACHABLE, Instance, Route, Solution, TimeWindowMode
from optiroute.solvers import clarke_wright
from optiroute.solvers.base import SolveParams, SolverRun

COST_SCALE = 1000  # objective units per currency unit

_STATUS = routing_enums_pb2.RoutingSearchStatus


def solve(instance: Instance, params: SolveParams | None = None) -> SolverRun:
    params = params or SolveParams()
    manager, routing = _build(instance)

    solutions = [0]
    routing.AddAtSolutionCallback(lambda: solutions.__setitem__(0, solutions[0] + 1))

    search = pywrapcp.DefaultRoutingSearchParameters()
    search.first_solution_strategy = routing_enums_pb2.FirstSolutionStrategy.PATH_CHEAPEST_ARC
    search.local_search_metaheuristic = (
        routing_enums_pb2.LocalSearchMetaheuristic.GUIDED_LOCAL_SEARCH
    )
    search.time_limit.FromMilliseconds(max(1, round(params.time_limit_s * 1000)))
    routing.CloseModelWithParameters(search)

    initial = _warm_start(instance, manager, routing)
    if initial is not None:
        solution = routing.SolveFromAssignmentWithParameters(initial, search)
    else:
        solution = routing.SolveWithParameters(search)

    status = routing.status()
    if solution is None:
        return SolverRun(
            Solution([]),
            iterations=solutions[0],
            hit_time_limit=status == _STATUS.ROUTING_FAIL_TIMEOUT,
        )
    return SolverRun(
        _extract(instance, manager, routing, solution),
        iterations=solutions[0],
        proven_optimal=status == _STATUS.ROUTING_OPTIMAL,
    )


def _build(instance: Instance) -> tuple[pywrapcp.RoutingIndexManager, pywrapcp.RoutingModel]:
    vehicles = instance.vehicles
    dist, dur = instance.matrix.distance, instance.matrix.duration
    n = instance.num_nodes
    manager = pywrapcp.RoutingIndexManager(
        n, len(vehicles), [v.start_node for v in vehicles], [v.end_node for v in vehicles]
    )
    routing = pywrapcp.RoutingModel(manager)

    # Arc costs: one matrix per distinct cost rate.
    cost_cb: dict[float, int] = {}
    for k, v in enumerate(vehicles):
        if v.cost_per_km not in cost_cb:
            rate = v.cost_per_km * COST_SCALE / 1000  # per metre
            matrix = [[0 if d == UNREACHABLE else round(rate * d) for d in row] for row in dist]
            cost_cb[v.cost_per_km] = routing.RegisterTransitMatrix(matrix)
        routing.SetArcCostEvaluatorOfVehicle(cost_cb[v.cost_per_km], k)
        routing.SetFixedCostOfVehicle(round(v.fixed_cost * COST_SCALE), k)

    # Capacities.
    def demand(attr: str) -> list[int]:
        return [0] * instance.num_depots + [getattr(c, attr) for c in instance.customers]

    for name, attr, cap in (("weight", "weight", "cap_weight"), ("volume", "volume", "cap_volume")):
        cb = routing.RegisterUnaryTransitVector(demand(attr))
        routing.AddDimensionWithVehicleCapacity(
            cb, 0, [getattr(v, cap) for v in vehicles], True, name
        )

    # Time.
    service = [0] * instance.num_depots + [c.service for c in instance.customers]
    transit = [
        [0 if dur[i][j] == UNREACHABLE else service[i] + dur[i][j] for j in range(n)]
        for i in range(n)
    ]
    horizon = max(v.latest_finish for v in vehicles)
    routing.AddDimension(routing.RegisterTransitMatrix(transit), horizon, horizon, False, "time")
    time = routing.GetDimensionOrDie("time")
    w = instance.weights
    for k, v in enumerate(vehicles):
        time.CumulVar(routing.Start(k)).SetRange(v.shift_start, v.latest_finish)
        time.CumulVar(routing.End(k)).SetRange(v.shift_start, v.latest_finish)
        if v.max_overtime:
            coef = round(w.overtime_per_min * COST_SCALE / 60)
            time.SetCumulVarSoftUpperBound(routing.End(k), v.regular_end, coef)
        routing.AddVariableMinimizedByFinalizer(time.CumulVar(routing.Start(k)))
        routing.AddVariableMinimizedByFinalizer(time.CumulVar(routing.End(k)))

    # Customers: windows, eligibility, optional service.
    for node in instance.customer_nodes:
        c = instance.customer(node)
        idx = manager.NodeToIndex(node)
        if instance.tw_mode is TimeWindowMode.HARD:
            time.CumulVar(idx).SetRange(c.tw_start, c.tw_end)
        else:
            time.CumulVar(idx).SetRange(c.tw_start, horizon)
            coef = round(w.lateness_per_min * c.priority * COST_SCALE / 60)
            time.SetCumulVarSoftUpperBound(idx, c.tw_end, coef)
        routing.AddDisjunction([idx], round(w.unserved * c.priority * COST_SCALE))
        if c.allowed_vehicles is not None:
            # -1 = unperformed. (SetAllowedVehiclesForIndex's Python binding rejects every
            # sequence type in OR-Tools 9.15, and an empty list there would mean "any vehicle".)
            routing.VehicleVar(idx).SetValues([-1, *sorted(c.allowed_vehicles)])

    # Depots no vehicle starts or ends at are ordinary nodes to OR-Tools: never visit them.
    terminals = {v.start_node for v in vehicles} | {v.end_node for v in vehicles}
    for depot in range(instance.num_depots):
        if depot not in terminals:
            idx = manager.NodeToIndex(depot)
            routing.AddDisjunction([idx], 0)
            routing.ActiveVar(idx).SetMax(0)

    _forbid_unreachable(instance, manager, routing, terminals)
    return manager, routing


def _forbid_unreachable(
    instance: Instance,
    manager: pywrapcp.RoutingIndexManager,
    routing: pywrapcp.RoutingModel,
    terminals: set[int],
) -> None:
    dist, dur = instance.matrix.distance, instance.matrix.duration
    pairs = [
        (i, j)
        for i in range(instance.num_nodes)
        for j in range(instance.num_nodes)
        if i != j and UNREACHABLE in (dist[i][j], dur[i][j])
    ]
    if not pairs:
        return
    starts, ends = defaultdict(list), defaultdict(list)
    for k, v in enumerate(instance.vehicles):
        starts[v.start_node].append(routing.Start(k))
        ends[v.end_node].append(routing.End(k))

    def indices(node: int, side: dict[int, list[int]]) -> list[int]:
        if instance.is_customer(node):
            return [manager.NodeToIndex(node)]
        return side.get(node, []) if node in terminals else []

    for i, j in pairs:
        for a in indices(i, starts):
            for b in indices(j, ends):
                routing.NextVar(a).RemoveValue(b)


def _warm_start(
    instance: Instance, manager: pywrapcp.RoutingIndexManager, routing: pywrapcp.RoutingModel
) -> pywrapcp.Assignment | None:
    seed = clarke_wright.solve(instance).solution
    routes: list[list[int]] = [[] for _ in instance.vehicles]
    for r in seed.routes:
        routes[r.vehicle] = [manager.NodeToIndex(n) for n in r.stops]
    return routing.ReadAssignmentFromRoutes(routes, True)


def _extract(
    instance: Instance,
    manager: pywrapcp.RoutingIndexManager,
    routing: pywrapcp.RoutingModel,
    solution: pywrapcp.Assignment,
) -> Solution:
    routes = []
    for k in range(len(instance.vehicles)):
        idx = solution.Value(routing.NextVar(routing.Start(k)))
        stops = []
        while not routing.IsEnd(idx):
            stops.append(manager.IndexToNode(idx))
            idx = solution.Value(routing.NextVar(idx))
        if stops:
            routes.append(Route(k, stops))
    return Solution(routes)
