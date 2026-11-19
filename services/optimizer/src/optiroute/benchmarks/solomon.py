"""Solomon VRPTW benchmark instances (56 × 100 customers).

Conventions match the published best-known solutions (DIMACS): Euclidean distance truncated to
one decimal, travel time equal to distance, hard time windows, distance-only objective. Everything
is scaled by ``SCALE`` into the integer units of :mod:`optiroute.model`, so
``evaluation.distance / SCALE`` is directly comparable with the BKS.

Instance files come from a pinned commit of the iRB-Lab/py-ga-VRPTW mirror (MIT), BKS values from
PyVRP/Instances (MIT, taken from CVRPLIB). ``solomon_manifest.csv`` pins every file's SHA-256.
"""

from __future__ import annotations

import csv
import hashlib
import math
import re
import urllib.request
from collections.abc import Iterable
from dataclasses import dataclass
from importlib import resources
from pathlib import Path

from optiroute.model import Customer, Instance, Matrix, TimeWindowMode, Vehicle

SCALE = 1000
MIRROR = (
    "https://raw.githubusercontent.com/iRB-Lab/py-ga-VRPTW/"
    "a853ce2a5bce7ca04a33e8d8293b3ebab913e725/data/text/{name}.txt"
)


@dataclass(frozen=True, slots=True)
class BksEntry:
    sha256: str
    vehicles: int
    distance: float


def manifest() -> dict[str, BksEntry]:
    text = resources.files(__package__).joinpath("solomon_manifest.csv").read_text()
    return {
        row["instance"]: BksEntry(
            row["sha256"], int(row["bks_vehicles"]), float(row["bks_distance"])
        )
        for row in csv.DictReader(text.splitlines())
    }


def dimacs_distance(x1: float, y1: float, x2: float, y2: float) -> int:
    """Euclidean distance truncated to one decimal, scaled by SCALE (exact for integer coords)."""
    dx, dy = x1 - x2, y1 - y2
    if all(float(v).is_integer() for v in (dx, dy)):
        tenths = math.isqrt(int(100 * (dx * dx + dy * dy)))
    else:
        tenths = math.floor(10 * math.hypot(dx, dy))
    return tenths * (SCALE // 10)


def parse(text: str, *, fixed_cost: float = 0.0) -> Instance:
    lines = [ln.strip() for ln in text.splitlines()]
    name = next(ln for ln in lines if ln)
    numeric = [ln.split() for ln in lines if re.fullmatch(r"[\d.\s]+", ln) and ln]
    fleet = next(t for t in numeric if len(t) == 2)
    rows = [[float(x) for x in t] for t in numeric if len(t) == 7]
    num_vehicles, capacity = int(fleet[0]), int(fleet[1])
    if [int(r[0]) for r in rows] != list(range(len(rows))):
        raise ValueError(f"{name}: customer numbers must run 0..n with the depot first")

    coords = [(r[1], r[2]) for r in rows]
    dist = [[dimacs_distance(*a, *b) for b in coords] for a in coords]
    s = SCALE
    depot = rows[0]
    customers = tuple(
        Customer(
            id=str(int(r[0])),
            weight=int(r[3]),
            volume=0,
            tw_start=round(r[4] * s),
            tw_end=round(r[5] * s),
            service=round(r[6] * s),
        )
        for r in rows[1:]
    )
    vehicles = tuple(
        Vehicle(
            id=f"v{k + 1}",
            start_node=0,
            end_node=0,
            cap_weight=capacity,
            cap_volume=0,
            cost_per_km=1.0,  # travel cost = distance / SCALE = Solomon distance
            fixed_cost=fixed_cost,
            shift_start=round(depot[4] * s),
            shift_end=round(depot[5] * s),
        )
        for k in range(num_vehicles)
    )
    return Instance(
        name=name,
        num_depots=1,
        customers=customers,
        vehicles=vehicles,
        matrix=Matrix(dist, [row[:] for row in dist]),
        tw_mode=TimeWindowMode.HARD,
        allow_unassigned=False,
    )


def load(path: Path, *, fixed_cost: float = 0.0) -> Instance:
    return parse(Path(path).read_text(), fixed_cost=fixed_cost)


def parse_solution(text: str) -> tuple[list[list[int]], float]:
    """Parse a CVRPLIB-style ``.sol`` file: ``Route #k: 5 3 7 ...`` lines and ``Cost 827.3``."""
    routes = [
        [int(x) for x in m.group(1).split()]
        for m in re.finditer(r"^Route\s*#\d+:\s*(.*)$", text, re.M)
    ]
    cost = float(re.search(r"^Cost\s+([\d.]+)", text, re.M).group(1))
    return routes, cost


def fetch(dest: Path, names: Iterable[str] | None = None, timeout: float = 30.0) -> list[Path]:
    """Download pinned instance files into ``dest``, verifying SHA-256. Skips verified files."""
    entries = manifest()
    dest.mkdir(parents=True, exist_ok=True)
    paths = []
    for name in names or entries:
        if name not in entries:
            raise KeyError(f"unknown Solomon instance {name!r}")
        path = dest / f"{name}.txt"
        if not (path.exists() and _sha256(path.read_bytes()) == entries[name].sha256):
            req = urllib.request.Request(
                MIRROR.format(name=name), headers={"User-Agent": "optiroute-bench"}
            )
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                data = resp.read()
            if _sha256(data) != entries[name].sha256:
                raise ValueError(f"{name}: checksum mismatch, refusing to save")
            path.write_bytes(data)
        paths.append(path)
    return paths


def _sha256(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()
