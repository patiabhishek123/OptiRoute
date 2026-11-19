"""Road-network matrices from an OSRM ``/table`` service (docs/ARCHITECTURE.md §8).

* Points are deduplicated after rounding to 5 decimals (~1 m) and sorted, so the same set of
  locations always produces the same request and cache key, whatever order the input is in.
* The matrix is fetched in ``chunk`` × ``chunk`` source/destination blocks. Keep ``chunk`` at or
  below the server's ``--max-table-size``.
* ``null`` cells (no route, e.g. an island) become :data:`optiroute.model.UNREACHABLE`.
* Transport errors and HTTP 5xx are retried with backoff. OSRM 4xx answers (bad query, too big)
  fail immediately, because retrying cannot fix them.
"""

from __future__ import annotations

import hashlib
import json
import time
import urllib.error
import urllib.request
from collections.abc import Sequence
from typing import Protocol

from optiroute.model import UNREACHABLE, Matrix

PRECISION = 5


class OsrmError(RuntimeError):
    def __init__(self, message: str, retryable: bool) -> None:
        super().__init__(message)
        self.retryable = retryable


class MatrixCache(Protocol):
    def get(self, key: str) -> Matrix | None: ...
    def set(self, key: str, matrix: Matrix) -> None: ...


class DictMatrixCache:
    """In-process cache, for tests and the CLI. The worker uses Redis (Phase 2)."""

    def __init__(self) -> None:
        self._data: dict[str, Matrix] = {}

    def get(self, key: str) -> Matrix | None:
        return self._data.get(key)

    def set(self, key: str, matrix: Matrix) -> None:
        self._data[key] = matrix


class OsrmMatrixProvider:
    source = "OSRM"

    def __init__(
        self,
        base_url: str,
        *,
        dataset_version: str = "unknown",
        profile: str = "driving",
        chunk: int = 500,
        timeout_s: float = 60.0,
        retries: int = 3,
        backoff_s: float = 1.0,
        cache: MatrixCache | None = None,
    ) -> None:
        if chunk < 1:
            raise ValueError("chunk must be >= 1")
        self.base_url = base_url.rstrip("/")
        self.dataset_version = dataset_version
        self.profile = profile
        self.chunk = chunk
        self.timeout_s = timeout_s
        self.retries = retries
        self.backoff_s = backoff_s
        self.cache = cache
        self.last_key: str | None = None
        self.requests = 0  # HTTP requests made, for metrics and tests

    def build(self, points: Sequence[tuple[float, float]]) -> Matrix:
        rounded = [(round(lat, PRECISION), round(lon, PRECISION)) for lat, lon in points]
        unique = sorted(set(rounded))
        position = {p: i for i, p in enumerate(unique)}
        key = self._key(unique)
        self.last_key = key

        canonical = self.cache.get(key) if self.cache else None
        if canonical is None:
            canonical = self._fetch_matrix(unique)
            if self.cache:
                self.cache.set(key, canonical)

        idx = [position[p] for p in rounded]
        cd, ct = canonical.distance, canonical.duration
        distance = [
            [0 if i == j else cd[a][b] for j, b in enumerate(idx)] for i, a in enumerate(idx)
        ]
        duration = [
            [0 if i == j else ct[a][b] for j, b in enumerate(idx)] for i, a in enumerate(idx)
        ]
        return Matrix(distance, duration)

    def _key(self, unique: list[tuple[float, float]]) -> str:
        h = hashlib.sha256(f"{self.dataset_version}|{self.profile}|".encode())
        h.update(json.dumps(unique).encode())
        return f"osrm:{h.hexdigest()}"

    def _fetch_matrix(self, unique: list[tuple[float, float]]) -> Matrix:
        n = len(unique)
        distance = [[UNREACHABLE] * n for _ in range(n)]
        duration = [[UNREACHABLE] * n for _ in range(n)]
        blocks = [range(s, min(s + self.chunk, n)) for s in range(0, n, self.chunk)]
        for src in blocks:
            for dst in blocks:
                d, t = self._table(unique, src, dst)
                for a, row_d, row_t in zip(src, d, t, strict=True):
                    for b, cell_d, cell_t in zip(dst, row_d, row_t, strict=True):
                        if cell_d is not None and cell_t is not None:
                            distance[a][b] = round(cell_d)
                            duration[a][b] = round(cell_t)
        for i in range(n):
            distance[i][i] = duration[i][i] = 0
        return Matrix(distance, duration)

    def _table(
        self, unique: list[tuple[float, float]], src: range, dst: range
    ) -> tuple[list[list[float | None]], list[list[float | None]]]:
        nodes = list(dict.fromkeys([*src, *dst]))  # each coordinate once per request
        local = {node: i for i, node in enumerate(nodes)}
        coords = ";".join(f"{unique[i][1]},{unique[i][0]}" for i in nodes)  # OSRM wants lon,lat
        url = (
            f"{self.base_url}/table/v1/{self.profile}/{coords}"
            f"?sources={';'.join(str(local[i]) for i in src)}"
            f"&destinations={';'.join(str(local[i]) for i in dst)}"
            "&annotations=distance,duration"
        )
        body = self._get(url)
        if body.get("code") != "Ok":
            raise OsrmError(f"OSRM {body.get('code')}: {body.get('message')}", retryable=False)
        return body["distances"], body["durations"]

    def _get(self, url: str) -> dict:
        for attempt in range(self.retries + 1):
            self.requests += 1
            try:
                with urllib.request.urlopen(url, timeout=self.timeout_s) as resp:
                    return json.loads(resp.read())
            except urllib.error.HTTPError as e:
                if e.code < 500:  # OSRM explains 4xx in a JSON body
                    try:
                        body = json.loads(e.read())
                    except ValueError:
                        body = {"code": f"HTTP{e.code}", "message": str(e)}
                    raise OsrmError(
                        f"OSRM {body.get('code')}: {body.get('message')}", retryable=False
                    ) from e
                error: Exception = e
            except (urllib.error.URLError, TimeoutError, ConnectionError) as e:
                error = e
            if attempt < self.retries:
                time.sleep(self.backoff_s * 2**attempt)
        raise OsrmError(
            f"OSRM unavailable after {self.retries + 1} attempts: {error}", retryable=True
        )
