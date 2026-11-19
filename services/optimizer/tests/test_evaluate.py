"""Hand-computed objective values on the line instance (see conftest)."""

import pytest
from conftest import line_instance, with_customer

from optiroute.model import Route, Solution, TimeWindowMode
from optiroute.validation import evaluate, simulate_route


def test_travel_cost_and_schedule(line):
    stats = simulate_route(line, 0, [1, 2, 3])
    assert stats.distance == 6000  # 0->1->2->3->0
    assert [s.arrival for s in stats.stops] == [100, 200, 300]
    assert stats.finish == 600 and stats.duration == 600
    ev = evaluate(line, Solution([Route(0, [1, 2, 3])]))
    assert ev.cost.travel == pytest.approx(12.0)  # 6 km * 2.0/km
    assert ev.cost.total == pytest.approx(12.0)
    assert ev.vehicles_used == 1 and ev.late_stops == 0


def test_waiting_and_service_times(line):
    inst = with_customer(with_customer(line, 1, tw_start=150, service=20), 2, service=30)
    s1, s2, s3 = simulate_route(inst, 0, [1, 2, 3]).stops
    assert (s1.arrival, s1.start, s1.departure) == (100, 150, 170)
    assert (s2.arrival, s2.start, s2.departure) == (270, 270, 300)
    assert s3.arrival == 400


def test_lateness_cost_scales_with_priority():
    inst = line_instance(tw_mode=TimeWindowMode.SOFT)
    inst = with_customer(inst, 3, tw_end=180, priority=2)  # arrives 300 -> 120 s late
    ev = evaluate(inst, Solution([Route(0, [1, 2, 3])]))
    assert ev.late_stops == 1
    assert ev.cost.lateness == pytest.approx(60.0 * 2 * 2)  # 60/min * priority 2 * 2 min


def test_fixed_and_overtime_cost():
    inst = line_instance(fixed_cost=25.0, shift_end=420, break_s=60)  # regular end 360
    ev = evaluate(inst, Solution([Route(0, [1, 2, 3])]))  # finish 600 -> 240 s overtime
    assert ev.cost.fleet == 25.0
    assert ev.cost.overtime == pytest.approx(6.0 * 4)


def test_unused_vehicle_costs_nothing_and_unserved_is_penalised():
    inst = line_instance(vehicles=2, fixed_cost=25.0)
    ev = evaluate(inst, Solution([Route(0, [1, 2]), Route(1, [])]))
    assert ev.vehicles_used == 1
    assert ev.cost.fleet == 25.0
    assert ev.unserved == (3,)
    assert ev.cost.unserved == 1000.0
