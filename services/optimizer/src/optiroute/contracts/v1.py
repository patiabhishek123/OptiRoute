"""Optimizer wire contract v1 (docs/ARCHITECTURE.md §4).

The JSON Schemas exported from these models into ``packages/api-contracts/optimizer/v1`` are the
cross-language source of truth. Integer units only: metres, seconds since local midnight of
``planning_date``, grams, cm³. Changes must be backward compatible within v1 (new optional fields).
"""

from __future__ import annotations

import datetime as dt
from enum import StrEnum
from typing import Annotated, Literal, Self

from pydantic import BaseModel, ConfigDict, Field, model_validator

CONTRACT_VERSION = "1.0"

NonNegInt = Annotated[int, Field(ge=0)]


class _Model(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)


# ---------------------------------------------------------------- request


class Algorithm(StrEnum):
    NEAREST_NEIGHBOUR = "NEAREST_NEIGHBOUR"  # S0
    CLARKE_WRIGHT = "CLARKE_WRIGHT"  # S1
    ORTOOLS_GLS = "ORTOOLS_GLS"  # S2
    HYBRID_GLS = "HYBRID_GLS"  # S3


class JobKind(StrEnum):
    SOLVE = "SOLVE"
    REOPTIMIZE = "REOPTIMIZE"


class TimeWindows(StrEnum):
    HARD = "HARD"
    SOFT = "SOFT"


class Weights(_Model):
    lateness_per_min: float = Field(50.0, ge=0)
    unserved: float = Field(1_000_000.0, ge=0)
    overtime_per_min: float = Field(20.0, ge=0)
    reassign: float = Field(500.0, ge=0)
    reorder: float = Field(100.0, ge=0)


class Objective(_Model):
    profile: str = "COST_BALANCED"
    weights: Weights = Weights()


class SolverConfig(_Model):
    algorithm: Algorithm = Algorithm.HYBRID_GLS
    time_limit_s: int = Field(15, ge=1, le=3600)
    allow_unassigned: bool = True
    seed: int = 0
    time_windows: TimeWindows = TimeWindows.SOFT


class Depot(_Model):
    id: str
    lat: float = Field(ge=-90, le=90)
    lon: float = Field(ge=-180, le=180)


class Vehicle(_Model):
    id: str
    driver_id: str | None = None
    start_depot_id: str
    end_depot_id: str | None = None  # defaults to start_depot_id
    cap_weight_g: NonNegInt
    cap_volume_cm3: NonNegInt
    cost_per_km: float = Field(ge=0)
    fixed_cost: float = Field(0.0, ge=0)
    shift_start_s: NonNegInt
    shift_end_s: NonNegInt
    max_overtime_s: int = Field(0, ge=0)
    break_s: int = Field(0, ge=0)

    @model_validator(mode="after")
    def _shift(self) -> Self:
        if self.shift_end_s - self.break_s <= self.shift_start_s:
            raise ValueError("shift_end_s - break_s must be after shift_start_s")
        return self


class Order(_Model):
    id: str
    depot_id: str | None = None  # restricts the order to vehicles starting at this depot
    lat: float = Field(ge=-90, le=90)
    lon: float = Field(ge=-180, le=180)
    weight_g: int = Field(0, ge=0)
    volume_cm3: int = Field(0, ge=0)
    tw_start_s: NonNegInt
    tw_end_s: NonNegInt
    service_s: int = Field(0, ge=0)
    priority: int = Field(1, ge=1)

    @model_validator(mode="after")
    def _window(self) -> Self:
        if self.tw_end_s < self.tw_start_s:
            raise ValueError("tw_end_s must be >= tw_start_s")
        return self


class SolveRequest(_Model):
    contract_version: Literal["1.0"] = CONTRACT_VERSION
    job_id: str
    kind: JobKind = JobKind.SOLVE
    planning_date: dt.date
    timezone: str
    objective: Objective = Objective()
    solver: SolverConfig = SolverConfig()
    depots: list[Depot] = Field(min_length=1)
    vehicles: list[Vehicle] = Field(min_length=1)
    orders: list[Order]

    @model_validator(mode="after")
    def _references(self) -> Self:
        collections = {"depot": self.depots, "vehicle": self.vehicles, "order": self.orders}
        for label, items in collections.items():
            ids = [i.id for i in items]
            if len(ids) != len(set(ids)):
                raise ValueError(f"duplicate {label} ids")
        depot_ids = {d.id for d in self.depots}
        refs = [v.start_depot_id for v in self.vehicles]
        refs += [v.end_depot_id for v in self.vehicles if v.end_depot_id]
        refs += [o.depot_id for o in self.orders if o.depot_id]
        if unknown := set(refs) - depot_ids:
            raise ValueError(f"unknown depot ids: {sorted(unknown)}")
        return self


# ---------------------------------------------------------------- result


class Status(StrEnum):
    FEASIBLE = "FEASIBLE"  # validated; never implies proven optimality
    INFEASIBLE = "INFEASIBLE"  # no solution satisfying the hard constraints was found
    TIME_LIMIT = "TIME_LIMIT"  # no solution within the time limit
    FAILED = "FAILED"  # invalid input or solver produced an invalid plan


class ObjectiveBreakdown(_Model):
    total: float
    travel: float
    lateness: float
    fleet: float
    overtime: float
    unserved: float
    stability: float


class Kpis(_Model):
    distance_m: int
    duration_s: int
    vehicles_used: int
    late_stops: int


class Stop(_Model):
    order_id: str
    seq: int = Field(ge=1)
    arrival_s: int
    service_start_s: int
    departure_s: int
    late_s: int


class ResultRoute(_Model):
    vehicle_id: str
    driver_id: str | None
    distance_m: int
    duration_s: int
    stops: list[Stop]


class UnassignedReason(StrEnum):
    NO_ELIGIBLE_VEHICLE = "NO_ELIGIBLE_VEHICLE"
    NOT_ROUTED = "NOT_ROUTED"  # solver left it out (capacity, time, or cost); detail comes later


class Unassigned(_Model):
    order_id: str
    reason: UnassignedReason


class ViolationOut(_Model):
    code: str
    message: str
    vehicle_id: str | None = None
    order_id: str | None = None


class Validation(_Model):
    passed: bool
    violations: list[ViolationOut]


class Meta(_Model):
    algorithm: str
    solver_version: str
    git_sha: str | None
    seed: int
    iterations: int | None
    matrix_source: str
    matrix_key: str | None
    execution_ms: int
    recommend_publish: bool | None = None


class SolveResult(_Model):
    contract_version: Literal["1.0"] = CONTRACT_VERSION
    job_id: str
    status: Status
    proven_optimal: bool = False
    objective: ObjectiveBreakdown | None
    kpis: Kpis | None
    routes: list[ResultRoute]
    unassigned: list[Unassigned]
    validation: Validation
    meta: Meta
