"""Travel distance/time matrices: OSRM road network, or haversine for tests/offline."""

from collections.abc import Sequence
from typing import Protocol

from optiroute.matrices.haversine import HaversineMatrixProvider
from optiroute.matrices.osrm import DictMatrixCache, MatrixCache, OsrmError, OsrmMatrixProvider
from optiroute.model import Matrix


class MatrixProvider(Protocol):
    source: str  # recorded in result metadata, e.g. "HAVERSINE", "OSRM"

    def build(self, points: Sequence[tuple[float, float]]) -> Matrix:
        """Matrix over ``points`` given as (lat, lon), in the same order."""
        ...


__all__ = [
    "DictMatrixCache",
    "HaversineMatrixProvider",
    "MatrixCache",
    "MatrixProvider",
    "OsrmError",
    "OsrmMatrixProvider",
]
