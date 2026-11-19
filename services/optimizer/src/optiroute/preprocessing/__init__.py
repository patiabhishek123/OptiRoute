"""Problem builder: wire contract → index-based :class:`optiroute.model.Instance`."""

from __future__ import annotations

from optiroute.contracts.v1 import JobKind, SolveRequest
from optiroute.matrices import MatrixProvider
from optiroute.model import Customer, Instance, TimeWindowMode, Vehicle, Weights


class UnsupportedRequestError(ValueError):
    """Valid v1 request this optimizer version cannot handle yet (non-retryable)."""


def build_instance(req: SolveRequest, provider: MatrixProvider) -> Instance:
    if req.kind is JobKind.REOPTIMIZE:
        raise UnsupportedRequestError("REOPTIMIZE is not supported until Phase 5")

    depot_node = {d.id: i for i, d in enumerate(req.depots)}
    vehicles = tuple(
        Vehicle(
            id=v.id,
            driver_id=v.driver_id,
            start_node=depot_node[v.start_depot_id],
            end_node=depot_node[v.end_depot_id or v.start_depot_id],
            cap_weight=v.cap_weight_g,
            cap_volume=v.cap_volume_cm3,
            cost_per_km=v.cost_per_km,
            fixed_cost=v.fixed_cost,
            shift_start=v.shift_start_s,
            shift_end=v.shift_end_s,
            max_overtime=v.max_overtime_s,
            break_s=v.break_s,
        )
        for v in req.vehicles
    )
    by_depot: dict[str, frozenset[int]] = {
        d: frozenset(k for k, v in enumerate(req.vehicles) if v.start_depot_id == d)
        for d in depot_node
    }
    customers = tuple(
        Customer(
            id=o.id,
            weight=o.weight_g,
            volume=o.volume_cm3,
            tw_start=o.tw_start_s,
            tw_end=o.tw_end_s,
            service=o.service_s,
            priority=o.priority,
            allowed_vehicles=by_depot[o.depot_id] if o.depot_id else None,
        )
        for o in req.orders
    )
    points = [(d.lat, d.lon) for d in req.depots] + [(o.lat, o.lon) for o in req.orders]
    w = req.objective.weights
    return Instance(
        name=req.job_id,
        num_depots=len(req.depots),
        customers=customers,
        vehicles=vehicles,
        matrix=provider.build(points),
        weights=Weights(w.lateness_per_min, w.unserved, w.overtime_per_min),
        tw_mode=TimeWindowMode(req.solver.time_windows.value),
        allow_unassigned=req.solver.allow_unassigned,
    )


__all__ = ["UnsupportedRequestError", "build_instance"]
