from __future__ import annotations

from collections.abc import Callable
from dataclasses import dataclass

from optiroute.model import Instance, Solution


@dataclass(frozen=True, slots=True)
class SolveParams:
    time_limit_s: float = 15.0
    seed: int = 0


@dataclass(slots=True)
class SolverRun:
    solution: Solution
    iterations: int | None = None
    hit_time_limit: bool = False
    proven_optimal: bool = False  # never claimed by heuristics (spec §10.6)


Solver = Callable[[Instance, SolveParams | None], SolverRun]
