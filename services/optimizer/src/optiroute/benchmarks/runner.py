"""Benchmark runner (spec §19): same instances, objective and machine for every algorithm."""

from __future__ import annotations

import csv
import statistics
import time
from collections.abc import Iterable, Sequence
from dataclasses import asdict, dataclass, fields
from pathlib import Path
from typing import TextIO

from optiroute.benchmarks import solomon
from optiroute.solvers import SolveParams, get_solver
from optiroute.validation import evaluate, validate


@dataclass(frozen=True, slots=True)
class BenchRow:
    instance: str
    algorithm: str
    seed: int
    feasible: bool
    violations: int
    vehicles: int
    distance: float
    objective: float
    late_stops: int
    unserved: int
    ms: int
    bks_vehicles: int | None
    bks_distance: float | None
    gap_pct: float | None  # distance gap vs best-known solution


def run(
    paths: Iterable[Path],
    algorithms: Sequence[str],
    seeds: Sequence[int] = (0,),
    time_limit_s: float = 15.0,
) -> list[BenchRow]:
    bks = solomon.manifest()
    rows = []
    for path in sorted(paths):
        instance = solomon.load(path)
        ref = bks.get(path.stem)
        for algorithm in algorithms:
            solver = get_solver(algorithm)
            for seed in seeds:
                started = time.perf_counter()
                result = solver(instance, SolveParams(time_limit_s, seed))
                ms = round((time.perf_counter() - started) * 1000)
                violations = validate(instance, result.solution)
                ev = evaluate(instance, result.solution)
                distance = ev.distance / solomon.SCALE
                gap = (
                    round((distance - ref.distance) / ref.distance * 100, 2)
                    if ref and not violations
                    else None
                )
                rows.append(
                    BenchRow(
                        instance=path.stem,
                        algorithm=algorithm,
                        seed=seed,
                        feasible=not violations,
                        violations=len(violations),
                        vehicles=ev.vehicles_used,
                        distance=round(distance, 1),
                        objective=round(ev.cost.total, 1),
                        late_stops=ev.late_stops,
                        unserved=len(ev.unserved),
                        ms=ms,
                        bks_vehicles=ref.vehicles if ref else None,
                        bks_distance=ref.distance if ref else None,
                        gap_pct=gap,
                    )
                )
    return rows


def write_csv(rows: Sequence[BenchRow], out: TextIO) -> None:
    writer = csv.DictWriter(out, fieldnames=[f.name for f in fields(BenchRow)])
    writer.writeheader()
    writer.writerows(asdict(r) for r in rows)


def summary(rows: Sequence[BenchRow]) -> str:
    """Per-algorithm feasibility rate and gap statistics (best / median / mean)."""
    lines = [f"{'algorithm':<20} {'runs':>5} {'feasible':>9} {'best':>7} {'median':>7} {'mean':>7}"]
    for algorithm in dict.fromkeys(r.algorithm for r in rows):
        sub = [r for r in rows if r.algorithm == algorithm]
        gaps = [r.gap_pct for r in sub if r.gap_pct is not None]
        feasible = sum(r.feasible for r in sub)
        stats = (
            (min(gaps), statistics.median(gaps), statistics.fmean(gaps)) if gaps else (None,) * 3
        )
        cells = " ".join(f"{g:>6.2f}%" if g is not None else f"{'-':>7}" for g in stats)
        lines.append(f"{algorithm:<20} {len(sub):>5} {feasible:>4}/{len(sub):<4} {cells}")
    return "\n".join(lines)
