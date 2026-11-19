"""Shared fixtures: a tiny hand-checkable instance on a straight line.

Depot at x=0, customer i (node i) at x=i km. Distance |dx| km in metres; speed 10 m/s, so each
km takes 100 s. Defaults: one vehicle, capacity 10, wide windows, no service time.
"""

from __future__ import annotations

from dataclasses import replace
from pathlib import Path

import pytest

from optiroute.model import Customer, Instance, Matrix, TimeWindowMode, Vehicle, Weights

FIXTURES = Path(__file__).parent / "fixtures"


def line_instance(
    n: int = 3,
    *,
    vehicles: int = 1,
    tw_mode: TimeWindowMode = TimeWindowMode.HARD,
    allow_unassigned: bool = True,
    **vehicle_overrides,
) -> Instance:
    xs = range(n + 1)
    dist = [[abs(a - b) * 1000 for b in xs] for a in xs]
    dur = [[d // 10 for d in row] for row in dist]
    customers = tuple(
        Customer(id=f"o{i}", weight=1, volume=1, tw_start=0, tw_end=100_000, service=0)
        for i in range(1, n + 1)
    )
    base = Vehicle(
        id="v",
        start_node=0,
        end_node=0,
        cap_weight=10,
        cap_volume=10,
        cost_per_km=2.0,
        fixed_cost=0.0,
        shift_start=0,
        shift_end=100_000,
    )
    fleet = tuple(replace(base, id=f"v{k}", **vehicle_overrides) for k in range(vehicles))
    return Instance(
        name="line",
        num_depots=1,
        customers=customers,
        vehicles=fleet,
        matrix=Matrix(dist, dur),
        weights=Weights(lateness_per_min=60.0, unserved=1000.0, overtime_per_min=6.0),
        tw_mode=tw_mode,
        allow_unassigned=allow_unassigned,
    )


def with_customer(instance: Instance, node: int, **changes) -> Instance:
    i = node - instance.num_depots
    customers = list(instance.customers)
    customers[i] = replace(customers[i], **changes)
    return replace(instance, customers=tuple(customers))


@pytest.fixture
def line() -> Instance:
    return line_instance()
