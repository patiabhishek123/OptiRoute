"""Solver registry. Keys match the contract's ``Algorithm`` values; S-codes are spec §10.5 IDs."""

from optiroute.solvers import baseline
from optiroute.solvers.base import SolveParams, Solver, SolverRun

SOLVERS: dict[str, Solver] = {
    "NEAREST_NEIGHBOUR": baseline.solve,  # S0
    # "CLARKE_WRIGHT": S1, "ORTOOLS_GLS": S2, "HYBRID_GLS": S3 — later in Phase 1 / Phase 6
}
ALIASES = {
    "S0": "NEAREST_NEIGHBOUR",
    "S1": "CLARKE_WRIGHT",
    "S2": "ORTOOLS_GLS",
    "S3": "HYBRID_GLS",
}


def get_solver(name: str) -> Solver:
    key = ALIASES.get(name.upper(), name.upper())
    if key not in SOLVERS:
        raise NotImplementedError(f"algorithm {name!r} is not implemented yet ({sorted(SOLVERS)})")
    return SOLVERS[key]


__all__ = ["SOLVERS", "SolveParams", "Solver", "SolverRun", "get_solver"]
