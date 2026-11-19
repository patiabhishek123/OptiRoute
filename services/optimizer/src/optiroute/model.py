"""Index-based domain model used by all solvers, the evaluator and the validator.

The wire contract (``optiroute.contracts``) is converted into an :class:`Instance` once by
``optiroute.preprocessing``. Everything here is integer-valued:

* distance in metres, time in seconds since local midnight of the planning date,
* weight in grams, volume in cm³.

Node numbering: depots occupy ``0 .. num_depots-1``; customer ``i`` is node ``num_depots + i``.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import StrEnum

UNREACHABLE = -1
"""Matrix sentinel for an arc that does not exist (e.g. OSRM could not route between two points)."""


class TimeWindowMode(StrEnum):
    HARD = "HARD"  # service must start by tw_end
    SOFT = "SOFT"  # late service allowed, penalised per minute


@dataclass(frozen=True, slots=True)
class Matrix:
    distance: list[list[int]]  # metres
    duration: list[list[int]]  # seconds

    def __post_init__(self) -> None:
        n = len(self.distance)
        if len(self.duration) != n or any(len(r) != n for r in (*self.distance, *self.duration)):
            raise ValueError("distance and duration matrices must both be square and equal size")

    @property
    def size(self) -> int:
        return len(self.distance)


@dataclass(frozen=True, slots=True)
class Customer:
    id: str
    weight: int
    volume: int
    tw_start: int  # earliest service start
    tw_end: int  # latest service start
    service: int
    priority: int = 1
    allowed_vehicles: frozenset[int] | None = None  # vehicle indices; None = any vehicle

    def allows(self, vehicle: int) -> bool:
        return self.allowed_vehicles is None or vehicle in self.allowed_vehicles


@dataclass(frozen=True, slots=True)
class Vehicle:
    """One routable resource: a vehicle already paired with its driver for the day."""

    id: str
    start_node: int
    end_node: int
    cap_weight: int
    cap_volume: int
    cost_per_km: float
    fixed_cost: float
    shift_start: int
    shift_end: int
    max_overtime: int = 0
    break_s: int = 0
    driver_id: str | None = None

    @property
    def regular_end(self) -> int:
        """End of paid regular time. V1 approximation: the break shortens the usable shift."""
        return self.shift_end - self.break_s

    @property
    def latest_finish(self) -> int:
        """Hard limit for returning to the end depot."""
        return self.regular_end + self.max_overtime


@dataclass(frozen=True, slots=True)
class Weights:
    """Objective weights in currency units. ``p_i`` and ``M_i`` scale with customer priority."""

    lateness_per_min: float = 50.0
    unserved: float = 1_000_000.0
    overtime_per_min: float = 20.0


@dataclass(frozen=True, slots=True)
class Instance:
    name: str
    num_depots: int
    customers: tuple[Customer, ...]
    vehicles: tuple[Vehicle, ...]
    matrix: Matrix
    weights: Weights = field(default_factory=Weights)
    tw_mode: TimeWindowMode = TimeWindowMode.HARD
    allow_unassigned: bool = True

    def __post_init__(self) -> None:
        if self.matrix.size != self.num_nodes:
            raise ValueError(f"matrix size {self.matrix.size} != node count {self.num_nodes}")
        for k, v in enumerate(self.vehicles):
            if not (self.is_depot(v.start_node) and self.is_depot(v.end_node)):
                raise ValueError(f"vehicle {k} must start and end at depot nodes")

    @property
    def num_nodes(self) -> int:
        return self.num_depots + len(self.customers)

    @property
    def customer_nodes(self) -> range:
        return range(self.num_depots, self.num_nodes)

    def is_depot(self, node: int) -> bool:
        return 0 <= node < self.num_depots

    def is_customer(self, node: int) -> bool:
        return self.num_depots <= node < self.num_nodes

    def customer(self, node: int) -> Customer:
        return self.customers[node - self.num_depots]


@dataclass(slots=True)
class Route:
    vehicle: int
    stops: list[int]  # customer nodes in visiting order; depots are implied by the vehicle


@dataclass(slots=True)
class Solution:
    routes: list[Route] = field(default_factory=list)

    def served_nodes(self) -> set[int]:
        return {n for r in self.routes for n in r.stops}

    def unserved_nodes(self, instance: Instance) -> list[int]:
        served = self.served_nodes()
        return [n for n in instance.customer_nodes if n not in served]
