from conftest import FIXTURES

from optiroute.benchmarks import runner, solomon
from optiroute.model import Route, Solution
from optiroute.solvers import get_solver
from optiroute.validation import evaluate, validate

C101 = FIXTURES / "solomon" / "C101.txt"


def test_dimacs_distance_truncates_to_one_decimal():
    assert solomon.dimacs_distance(0, 0, 3, 4) == 5000
    assert solomon.dimacs_distance(0, 0, 1, 1) == 1400  # sqrt(2) = 1.414... -> 1.4


def test_parse_c101():
    inst = solomon.load(C101)
    assert inst.name == "C101"
    assert len(inst.customers) == 100 and len(inst.vehicles) == 25
    assert inst.vehicles[0].cap_weight == 200
    assert inst.vehicles[0].shift_end == 1236 * solomon.SCALE
    c1 = inst.customer(1)
    assert (c1.weight, c1.tw_start, c1.tw_end, c1.service) == (10, 912_000, 967_000, 90_000)


def test_best_known_solution_is_valid_and_reproduces_its_cost():
    """Ground truth for the validator and evaluator: published BKS must pass and score exactly."""
    inst = solomon.load(C101)
    routes, cost = solomon.parse_solution((FIXTURES / "solomon" / "C101.sol").read_text())
    solution = Solution([Route(k, stops) for k, stops in enumerate(routes)])
    assert validate(inst, solution) == []
    assert evaluate(inst, solution).distance == round(cost * solomon.SCALE)


def test_manifest_covers_all_56_instances():
    entries = solomon.manifest()
    assert len(entries) == 56
    assert entries["C101"].distance == 827.3
    assert entries["C101"].sha256 == solomon._sha256(C101.read_bytes())


def test_nearest_neighbour_is_feasible_on_c101():
    inst = solomon.load(C101)
    run = get_solver("S0")(inst, None)
    assert validate(inst, run.solution) == []
    assert not run.proven_optimal


def test_runner_reports_gap():
    (row,) = runner.run([C101], ["S0"])
    assert row.feasible and row.instance == "C101"
    assert row.gap_pct is not None and row.gap_pct > 0
    assert "S0" in runner.summary([row])
