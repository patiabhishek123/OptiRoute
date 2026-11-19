import json

from conftest import FIXTURES, line_instance, with_customer

from optiroute.benchmarks import solomon
from optiroute.contracts import v1
from optiroute.matrices import HaversineMatrixProvider
from optiroute.model import Route
from optiroute.pipeline import solve_request
from optiroute.solvers import get_solver
from optiroute.validation import evaluate, validate

solve = get_solver("S1")


def test_merges_everything_into_one_route_when_capacity_allows(line):
    run = solve(line, None)
    assert [(r.vehicle, r.stops) for r in run.solution.routes] == [(0, [1, 2, 3])]
    assert run.iterations == 2


def test_respects_capacity_and_uses_more_vehicles():
    inst = line_instance(n=4, vehicles=3, cap_weight=2)
    run = solve(inst, None)
    assert validate(inst, run.solution) == []
    assert all(len(r.stops) <= 2 for r in run.solution.routes)
    assert run.solution.unserved_nodes(inst) == []


def test_unreachable_window_is_left_unserved(line):
    inst = with_customer(line, 2, tw_end=50)  # node 2 needs 200 s from the depot
    run = solve(inst, None)
    assert validate(inst, run.solution) == []
    assert run.solution.unserved_nodes(inst) == [2]


def test_leftover_route_is_repaired_into_existing_vehicle():
    # One vehicle, three customers: merging must end with every customer on vehicle 0.
    inst = line_instance(n=3, vehicles=1)
    run = solve(inst, None)
    assert run.solution.routes == [Route(0, [1, 2, 3])]


def test_feasible_on_c101_within_twice_the_bks_distance():
    inst = solomon.load(FIXTURES / "solomon" / "C101.txt")
    run = solve(inst, None)
    assert validate(inst, run.solution) == []
    assert evaluate(inst, run.solution).distance < 2 * 827.3 * solomon.SCALE


def test_multi_depot_request_via_pipeline():
    raw = json.loads((FIXTURES / "solve-request.json").read_text())
    req = v1.SolveRequest.model_validate(raw)
    result = solve_request(req, HaversineMatrixProvider(), algorithm="S1")
    assert result.status is v1.Status.FEASIBLE
    by_vehicle = {r.vehicle_id: {s.order_id for s in r.stops} for r in result.routes}
    assert not {"o-4", "o-5"} & by_vehicle.get("van-1", set())
    served = {s.order_id for r in result.routes for s in r.stops}
    assert served == {o["id"] for o in raw["orders"]}
