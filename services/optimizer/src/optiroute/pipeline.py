"""End-to-end solve: request → instance → solver → evaluate → validate → result.

Shared by the CLI and the Redis worker. ``on_stage`` is called on entering SOLVING and VALIDATING
(preprocessing is the default first stage); the worker uses it to record job status and to
abort cancelled jobs by raising.
"""

from __future__ import annotations

import os
import time
from collections.abc import Callable

from optiroute import __version__
from optiroute.contracts import v1
from optiroute.matrices import MatrixProvider
from optiroute.model import Instance
from optiroute.preprocessing import build_instance
from optiroute.solvers import SolveParams, SolverRun, get_solver
from optiroute.validation import Code, Evaluation, Violation, evaluate, validate

# Violations that make a plan impossible to schedule or score.
STRUCTURAL = {Code.UNKNOWN_VEHICLE, Code.UNKNOWN_NODE, Code.DEPOT_IN_ROUTE, Code.UNREACHABLE_ARC}


def solve_request(
    req: v1.SolveRequest,
    provider: MatrixProvider,
    algorithm: str | None = None,
    on_stage: Callable[[str], None] | None = None,
) -> v1.SolveResult:
    stage = on_stage or (lambda _: None)
    algorithm = algorithm or req.solver.algorithm.value
    solver = get_solver(algorithm)  # fail fast before building the matrix
    started = time.perf_counter()
    instance = build_instance(req, provider)
    stage("SOLVING")
    run = solver(instance, SolveParams(req.solver.time_limit_s, req.solver.seed))
    stage("VALIDATING")
    return score(req, instance, run, provider, algorithm=algorithm, started=started)


def score(
    req: v1.SolveRequest,
    instance: Instance,
    run: SolverRun,
    provider: MatrixProvider,
    *,
    algorithm: str,
    started: float,
) -> v1.SolveResult:
    """Validate and evaluate a plan (from a solver or from outside) into a SolveResult."""
    violations = validate(instance, run.solution)
    structural = any(v.code in STRUCTURAL for v in violations)
    evaluation = None if structural else evaluate(instance, run.solution)
    elapsed_ms = round((time.perf_counter() - started) * 1000)
    return to_result(
        req,
        instance,
        run,
        evaluation,
        violations,
        algorithm=algorithm,
        matrix_source=provider.source,
        matrix_key=getattr(provider, "last_key", None),
        execution_ms=elapsed_ms,
    )


def status_for(run: SolverRun, violations: list[Violation]) -> v1.Status:
    if run.hit_time_limit and not run.solution.routes:
        return v1.Status.TIME_LIMIT  # no plan at all within the limit
    if not violations:
        return v1.Status.FEASIBLE
    if all(v.code is Code.UNSERVED_NOT_ALLOWED for v in violations):
        return v1.Status.TIME_LIMIT if run.hit_time_limit else v1.Status.INFEASIBLE
    return v1.Status.FAILED  # any other violation means the solver produced an invalid plan


def to_result(
    req: v1.SolveRequest,
    instance: Instance,
    run: SolverRun,
    evaluation: Evaluation | None,
    violations: list[Violation],
    *,
    algorithm: str,
    matrix_source: str,
    execution_ms: int,
    matrix_key: str | None = None,
) -> v1.SolveResult:
    def vid(k: int | None) -> str | None:
        ok = k is not None and 0 <= k < len(instance.vehicles)
        return instance.vehicles[k].id if ok else None

    def oid(n: int | None) -> str | None:
        return instance.customer(n).id if n is not None and instance.is_customer(n) else None

    if evaluation is None:  # structurally broken plan: report violations only
        objective, kpis, routes, unassigned = None, None, [], []
    else:
        objective, kpis, routes, unassigned = _summarise(instance, evaluation)
    return v1.SolveResult(
        job_id=req.job_id,
        status=status_for(run, violations),
        proven_optimal=run.proven_optimal,
        objective=objective,
        kpis=kpis,
        routes=routes,
        unassigned=unassigned,
        validation=v1.Validation(
            passed=not violations,
            violations=[
                v1.ViolationOut(
                    code=v.code.value,
                    message=v.message,
                    vehicle_id=vid(v.vehicle),
                    order_id=oid(v.node),
                )
                for v in violations
            ],
        ),
        meta=v1.Meta(
            algorithm=algorithm,
            solver_version=__version__,
            git_sha=os.environ.get("OPTIROUTE_GIT_SHA"),
            seed=req.solver.seed,
            iterations=run.iterations,
            matrix_source=matrix_source,
            matrix_key=matrix_key,
            execution_ms=execution_ms,
        ),
    )


def _summarise(
    instance: Instance, evaluation: Evaluation
) -> tuple[v1.ObjectiveBreakdown, v1.Kpis, list[v1.ResultRoute], list[v1.Unassigned]]:
    c = evaluation.cost
    routes = [
        v1.ResultRoute(
            vehicle_id=instance.vehicles[r.vehicle].id,
            driver_id=instance.vehicles[r.vehicle].driver_id,
            distance_m=r.distance,
            duration_s=r.duration,
            stops=[
                v1.Stop(
                    order_id=instance.customer(s.node).id,
                    seq=i,
                    arrival_s=s.arrival,
                    service_start_s=s.start,
                    departure_s=s.departure,
                    late_s=s.late,
                )
                for i, s in enumerate(r.stops, start=1)
            ],
        )
        for r in evaluation.routes
        if r.used
    ]
    unassigned = [
        v1.Unassigned(
            order_id=instance.customer(n).id,
            reason=v1.UnassignedReason.NO_ELIGIBLE_VEHICLE
            if instance.customer(n).allowed_vehicles == frozenset()
            else v1.UnassignedReason.NOT_ROUTED,
        )
        for n in evaluation.unserved
    ]
    objective = v1.ObjectiveBreakdown(
        total=c.total,
        travel=c.travel,
        lateness=c.lateness,
        fleet=c.fleet,
        overtime=c.overtime,
        unserved=c.unserved,
        stability=c.stability,
    )
    kpis = v1.Kpis(
        distance_m=evaluation.distance,
        duration_s=evaluation.duration,
        vehicles_used=evaluation.vehicles_used,
        late_stops=evaluation.late_stops,
    )
    return objective, kpis, routes, unassigned
