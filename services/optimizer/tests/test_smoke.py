"""Smoke tests: the package imports and OR-Tools can solve a trivial routing problem."""

from ortools.constraint_solver import pywrapcp, routing_enums_pb2

import optiroute
from optiroute.cli import main


def test_package_version():
    assert optiroute.__version__


def test_cli_runs():
    assert main([]) == 0


def test_ortools_solves_tiny_tsp():
    dist = [
        [0, 2, 9, 10],
        [2, 0, 6, 4],
        [9, 6, 0, 3],
        [10, 4, 3, 0],
    ]
    manager = pywrapcp.RoutingIndexManager(len(dist), 1, 0)
    routing = pywrapcp.RoutingModel(manager)
    cb = routing.RegisterTransitCallback(
        lambda i, j: dist[manager.IndexToNode(i)][manager.IndexToNode(j)]
    )
    routing.SetArcCostEvaluatorOfAllVehicles(cb)
    params = pywrapcp.DefaultRoutingSearchParameters()
    params.first_solution_strategy = routing_enums_pb2.FirstSolutionStrategy.PATH_CHEAPEST_ARC

    solution = routing.SolveWithParameters(params)

    assert solution is not None
    assert solution.ObjectiveValue() == 18  # 0-1-3-2-0 = 2 + 4 + 3 + 9
