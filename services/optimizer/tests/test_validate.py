"""Spec §18.4 mathematical-correctness cases: each must be rejected by the validator."""

from dataclasses import replace

import pytest
from conftest import line_instance, with_customer

from optiroute.model import UNREACHABLE, Matrix, Route, Solution, TimeWindowMode
from optiroute.validation import Code, validate


def codes(instance, *routes):
    return [v.code for v in validate(instance, Solution(list(routes)))]


def test_feasible_solution_has_no_violations(line):
    assert codes(line, Route(0, [1, 2, 3])) == []


def test_duplicate_order_assignment(line):
    inst = line_instance(vehicles=2)
    assert codes(inst, Route(0, [1, 2]), Route(1, [2, 3])) == [Code.DUPLICATE_ASSIGNMENT]


def test_duplicate_within_one_route(line):
    assert codes(line, Route(0, [1, 2, 1, 3])) == [Code.DUPLICATE_ASSIGNMENT]


def test_weight_capacity_exceeded():
    inst = line_instance(cap_weight=2)
    assert codes(inst, Route(0, [1, 2, 3])) == [Code.WEIGHT_CAPACITY]


def test_volume_capacity_exceeded():
    inst = line_instance(cap_volume=2)
    assert codes(inst, Route(0, [1, 2, 3])) == [Code.VOLUME_CAPACITY]


def test_unknown_vehicle(line):
    assert codes(line, Route(5, [1, 2, 3])) == [Code.UNKNOWN_VEHICLE]


def test_vehicle_used_twice():
    inst = line_instance(vehicles=2)
    assert codes(inst, Route(0, [1]), Route(0, [2, 3])) == [Code.DUPLICATE_VEHICLE]


def test_ineligible_vehicle():
    inst = with_customer(line_instance(vehicles=2), 2, allowed_vehicles=frozenset({1}))
    assert codes(inst, Route(0, [1, 2, 3])) == [Code.VEHICLE_NOT_ALLOWED]


def test_hard_time_window_violation(line):
    # Node 3 is reached at t=300 at the earliest; its window closes at 200.
    inst = with_customer(line, 3, tw_end=200)
    assert codes(inst, Route(0, [1, 2, 3])) == [Code.TIME_WINDOW]


def test_soft_time_window_lateness_is_not_a_violation():
    inst = with_customer(line_instance(tw_mode=TimeWindowMode.SOFT), 3, tw_end=200)
    assert codes(inst, Route(0, [1, 2, 3])) == []


def test_waiting_for_window_then_violating_next(line):
    # Wait at node 1 until 500; node 2 is then reached at 600 > 550.
    inst = with_customer(with_customer(line, 1, tw_start=500), 2, tw_end=550)
    assert codes(inst, Route(0, [1, 2, 3])) == [Code.TIME_WINDOW]


def test_driver_shift_exceeded():
    # Route 0-1-2-3-0 takes 600 s; shift 500 s minus 50 s break, 100 s overtime allowed -> 550.
    inst = line_instance(shift_end=500, break_s=50, max_overtime=100)
    assert codes(inst, Route(0, [1, 2, 3])) == [Code.SHIFT_EXCEEDED]


def test_overtime_within_allowance_is_feasible():
    inst = line_instance(shift_end=500, max_overtime=100)
    assert codes(inst, Route(0, [1, 2, 3])) == []


def test_disconnected_route(line):
    dist = [row[:] for row in line.matrix.distance]
    dist[1][2] = UNREACHABLE
    inst = replace(line, matrix=Matrix(dist, line.matrix.duration))
    assert codes(inst, Route(0, [1, 2, 3])) == [Code.UNREACHABLE_ARC]
    assert codes(inst, Route(0, [2, 1, 3])) == []  # 2->1 still exists


def test_invalid_depot_sequence(line):
    assert codes(line, Route(0, [1, 0, 2, 3])) == [Code.DEPOT_IN_ROUTE]


def test_unknown_node(line):
    assert codes(line, Route(0, [1, 2, 3, 9])) == [Code.UNKNOWN_NODE]


def test_unserved_when_not_allowed():
    inst = line_instance(allow_unassigned=False)
    assert codes(inst, Route(0, [1, 3])) == [Code.UNSERVED_NOT_ALLOWED]
    assert codes(line_instance(), Route(0, [1, 3])) == []


@pytest.mark.skip(reason="frozen/completed stops arrive with REOPTIMIZE in Phase 5")
def test_completed_stop_reassignment(): ...
