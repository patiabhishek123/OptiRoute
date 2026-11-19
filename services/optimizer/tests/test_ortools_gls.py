import json
from dataclasses import replace

from conftest import FIXTURES, line_instance, with_customer

from optiroute.benchmarks import solomon
from optiroute.contracts import v1
from optiroute.matrices import HaversineMatrixProvider
from optiroute.model import UNREACHABLE, Matrix, Route, TimeWindowMode
from optiroute.pipeline import solve_request
from optiroute.solvers import SolveParams, get_solver
from optiroute.validation import evaluate, validate

FAST = SolveParams(time_limit_s=0.5)
solve = get_solver("S2")


def test_line_is_one_route(line):
    run = solve(line, FAST)
    assert run.solution.routes == [Route(0, [1, 2, 3])]
    assert not run.proven_optimal and run.iterations >= 1


def test_soft_window_serves_late_when_cheaper_than_dropping():
    # Node 2 opens at 0 and closes at 50 but is 200 s away: late by 150 s (cost 150) < 1000 penalty.
    inst = with_customer(line_instance(tw_mode=TimeWindowMode.SOFT), 2, tw_end=50)
    run = solve(inst, FAST)
    assert validate(inst, run.solution) == []
    ev = evaluate(inst, run.solution)
    assert ev.unserved == () and ev.late_stops >= 1


def test_hard_window_drops_unreachable_customer():
    inst = with_customer(line_instance(), 2, tw_end=50)
    run = solve(inst, FAST)
    assert validate(inst, run.solution) == []
    assert run.solution.unserved_nodes(inst) == [2]


def test_avoids_unreachable_arcs(line):
    dist = [row[:] for row in line.matrix.distance]
    dur = [row[:] for row in line.matrix.duration]
    dist[1][2] = dur[1][2] = UNREACHABLE
    inst = replace(line, matrix=Matrix(dist, dur))
    run = solve(inst, FAST)
    assert validate(inst, run.solution) == []
    assert run.solution.unserved_nodes(inst) == []


def test_overtime_is_allowed_and_charged():
    inst = line_instance(shift_end=500, max_overtime=200)  # route needs 600 s
    run = solve(inst, FAST)
    assert validate(inst, run.solution) == []
    assert evaluate(inst, run.solution).cost.overtime > 0


def test_never_worse_than_its_clarke_wright_warm_start_on_c101():
    inst = solomon.load(FIXTURES / "solomon" / "C101.txt")
    run = solve(inst, SolveParams(time_limit_s=2))
    assert validate(inst, run.solution) == []
    cw = get_solver("S1")(inst, None).solution
    assert evaluate(inst, run.solution).distance <= evaluate(inst, cw).distance


def test_multi_depot_and_unservable_order_via_pipeline():
    raw = json.loads((FIXTURES / "solve-request.json").read_text())
    raw["depots"].append({"id": "east", "lat": 52.5, "lon": 13.5})  # depot without vehicles
    raw["orders"].append({**raw["orders"][0], "id": "o-x", "depot_id": "east"})
    raw["solver"]["time_limit_s"] = 1
    result = solve_request(v1.SolveRequest.model_validate(raw), HaversineMatrixProvider(), "S2")
    assert result.status is v1.Status.FEASIBLE
    by_vehicle = {r.vehicle_id: {s.order_id for s in r.stops} for r in result.routes}
    assert not {"o-4", "o-5"} & by_vehicle.get("van-1", set())
    assert [u.order_id for u in result.unassigned] == ["o-x"]
