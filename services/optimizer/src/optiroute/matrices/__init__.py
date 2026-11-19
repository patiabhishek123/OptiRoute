"""Travel distance/time matrices. ``OsrmMatrixProvider`` arrives in Phase 2."""

from collections.abc import Sequence
from typing import Protocol

from optiroute.matrices.haversine import HaversineMatrixProvider
from optiroute.model import Matrix


class MatrixProvider(Protocol):
    source: str  # recorded in result metadata, e.g. "HAVERSINE", "OSRM"

    def build(self, points: Sequence[tuple[float, float]]) -> Matrix:
        """Matrix over ``points`` given as (lat, lon), in the same order."""
        ...


__all__ = ["HaversineMatrixProvider", "MatrixProvider"]
