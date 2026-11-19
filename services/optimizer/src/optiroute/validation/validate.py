"""Independent hard-constraint validator (spec §18.4).

Deliberately self-contained: it imports nothing from ``optiroute.solvers`` and does not reuse the
evaluator's schedule simulation, so a bug shared by solvers and the evaluator cannot hide here.
"""

from __future__ import annotations

import itertools
from dataclasses import dataclass
from enum import StrEnum

from optiroute.model import UNREACHABLE, Instance, Solution, TimeWindowMode


class Code(StrEnum):
    UNKNOWN_VEHICLE = "UNKNOWN_VEHICLE"
    DUPLICATE_VEHICLE = "DUPLICATE_VEHICLE"
    UNKNOWN_NODE = "UNKNOWN_NODE"
    DEPOT_IN_ROUTE = "DEPOT_IN_ROUTE"  # invalid depot sequence
    DUPLICATE_ASSIGNMENT = "DUPLICATE_ASSIGNMENT"
    VEHICLE_NOT_ALLOWED = "VEHICLE_NOT_ALLOWED"  # eligibility / depot compatibility
    UNREACHABLE_ARC = "UNREACHABLE_ARC"  # disconnected route
    WEIGHT_CAPACITY = "WEIGHT_CAPACITY"
    VOLUME_CAPACITY = "VOLUME_CAPACITY"
    TIME_WINDOW = "TIME_WINDOW"  # only in HARD time-window mode
    SHIFT_EXCEEDED = "SHIFT_EXCEEDED"  # return after shift end + break + max overtime
    UNSERVED_NOT_ALLOWED = "UNSERVED_NOT_ALLOWED"


@dataclass(frozen=True, slots=True)
class Violation:
    code: Code
    message: str
    vehicle: int | None = None
    node: int | None = None


def validate(instance: Instance, solution: Solution) -> list[Violation]:
    """Return every hard-constraint violation; an empty list means the solution is feasible."""
    out: list[Violation] = []
    seen_vehicles: set[int] = set()
    assigned: dict[int, int] = {}

    for route in solution.routes:
        k = route.vehicle
        if not 0 <= k < len(instance.vehicles):
            out.append(Violation(Code.UNKNOWN_VEHICLE, f"vehicle index {k} does not exist", k))
            continue
        if k in seen_vehicles:
            out.append(Violation(Code.DUPLICATE_VEHICLE, f"vehicle {k} has several routes", k))
        seen_vehicles.add(k)

        structurally_sound = True
        for n in route.stops:
            if not 0 <= n < instance.num_nodes:
                out.append(Violation(Code.UNKNOWN_NODE, f"node {n} does not exist", k, n))
                structurally_sound = False
                continue
            if instance.is_depot(n):
                out.append(Violation(Code.DEPOT_IN_ROUTE, f"depot {n} inside route", k, n))
                structurally_sound = False
                continue
            if n in assigned:
                out.append(
                    Violation(
                        Code.DUPLICATE_ASSIGNMENT,
                        f"node {n} on vehicle {k} already served by vehicle {assigned[n]}",
                        k,
                        n,
                    )
                )
            assigned.setdefault(n, k)
            if not instance.customer(n).allows(k):
                out.append(Violation(Code.VEHICLE_NOT_ALLOWED, f"node {n} not allowed", k, n))

        if structurally_sound and route.stops:
            out.extend(_check_route(instance, k, route.stops))

    if not instance.allow_unassigned:
        out.extend(
            Violation(Code.UNSERVED_NOT_ALLOWED, f"node {n} is not served", node=n)
            for n in instance.customer_nodes
            if n not in assigned
        )
    return out


def _check_route(instance: Instance, k: int, stops: list[int]) -> list[Violation]:
    out: list[Violation] = []
    v = instance.vehicles[k]
    customers = [instance.customer(n) for n in stops]

    weight = sum(c.weight for c in customers)
    if weight > v.cap_weight:
        out.append(Violation(Code.WEIGHT_CAPACITY, f"load {weight} > {v.cap_weight}", k))
    volume = sum(c.volume for c in customers)
    if volume > v.cap_volume:
        out.append(Violation(Code.VOLUME_CAPACITY, f"volume {volume} > {v.cap_volume}", k))

    path = [v.start_node, *stops, v.end_node]
    for a, b in itertools.pairwise(path):
        if instance.matrix.distance[a][b] == UNREACHABLE or (
            instance.matrix.duration[a][b] == UNREACHABLE
        ):
            out.append(Violation(Code.UNREACHABLE_ARC, f"no arc {a}->{b}", k, b))
            return out  # timing is undefined past a missing arc

    clock = v.shift_start
    for a, b, c in zip(path, stops, customers, strict=False):
        clock = max(clock + instance.matrix.duration[a][b], c.tw_start)
        if instance.tw_mode is TimeWindowMode.HARD and clock > c.tw_end:
            out.append(
                Violation(Code.TIME_WINDOW, f"service starts {clock} > window end {c.tw_end}", k, b)
            )
        clock += c.service
    clock += instance.matrix.duration[stops[-1]][v.end_node]
    if clock > v.latest_finish:
        out.append(Violation(Code.SHIFT_EXCEEDED, f"returns {clock} > {v.latest_finish}", k))
    return out
