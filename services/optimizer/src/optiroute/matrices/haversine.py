"""Straight-line matrix for tests and offline runs: great-circle distance × detour factor."""

from __future__ import annotations

import math
from collections.abc import Sequence

from optiroute.model import Matrix

EARTH_RADIUS_M = 6_371_008.8


def haversine_m(a: tuple[float, float], b: tuple[float, float]) -> float:
    lat1, lon1, lat2, lon2 = map(math.radians, (*a, *b))
    h = (
        math.sin((lat2 - lat1) / 2) ** 2
        + math.cos(lat1) * math.cos(lat2) * math.sin((lon2 - lon1) / 2) ** 2
    )
    return 2 * EARTH_RADIUS_M * math.asin(math.sqrt(h))


class HaversineMatrixProvider:
    source = "HAVERSINE"

    def __init__(self, detour_factor: float = 1.3, speed_kmh: float = 30.0) -> None:
        if detour_factor < 1 or speed_kmh <= 0:
            raise ValueError("detour_factor must be >= 1 and speed_kmh > 0")
        self.detour_factor = detour_factor
        self.speed_ms = speed_kmh / 3.6

    def build(self, points: Sequence[tuple[float, float]]) -> Matrix:
        distance = [[round(haversine_m(p, q) * self.detour_factor) for q in points] for p in points]
        duration = [[round(d / self.speed_ms) for d in row] for row in distance]
        return Matrix(distance, duration)
