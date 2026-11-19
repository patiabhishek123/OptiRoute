"""OsrmMatrixProvider against a fake /table endpoint (plus an opt-in live test)."""

import os
import urllib.error
import urllib.parse
import urllib.request

import pytest

from optiroute.matrices.haversine import haversine_m
from optiroute.matrices.osrm import DictMatrixCache, OsrmError, OsrmMatrixProvider
from optiroute.model import UNREACHABLE

# Bhubaneswar, Cuttack, Puri, Khordha (lat, lon)
POINTS = [(20.2961, 85.8245), (20.4625, 85.8830), (19.8135, 85.8312), (20.1820, 85.6200)]


class FakeOsrm:
    """Mimics /table: distance = haversine, duration = distance / 10 m/s."""

    def __init__(self, unreachable: set[tuple[int, int]] = frozenset()):
        self.unreachable = unreachable  # (from, to) pairs of (lat, lon) points
        self.calls: list[int] = []

    def __call__(self, url: str) -> dict:
        parsed = urllib.parse.urlsplit(url)  # urlparse would split ";" off as params
        coords = parsed.path.rsplit("/", 1)[1].split(";")
        pts = [(float(c.split(",")[1]), float(c.split(",")[0])) for c in coords]
        q = urllib.parse.parse_qs(parsed.query)
        src = [int(i) for i in q["sources"][0].split(";")]
        dst = [int(i) for i in q["destinations"][0].split(";")]
        self.calls.append(len(pts))

        def cell(a, b):
            if (pts[a], pts[b]) in self.unreachable:
                return None
            return haversine_m(pts[a], pts[b])

        dist = [[cell(a, b) for b in dst] for a in src]
        dur = [[None if d is None else d / 10 for d in row] for row in dist]
        return {"code": "Ok", "distances": dist, "durations": dur}


def provider(fake, **kw) -> OsrmMatrixProvider:
    p = OsrmMatrixProvider("http://osrm.test", backoff_s=0, **kw)
    p._get = fake  # type: ignore[method-assign]
    return p


def test_matrix_matches_point_order_and_units():
    m = provider(FakeOsrm()).build(POINTS)
    assert m.size == 4
    assert m.distance[0][0] == 0
    assert m.distance[0][2] == round(haversine_m(POINTS[0], POINTS[2]))
    assert m.duration[0][2] == round(haversine_m(POINTS[0], POINTS[2]) / 10)


def test_chunking_gives_the_same_matrix():
    whole = provider(FakeOsrm()).build(POINTS)
    fake = FakeOsrm()
    chunked = provider(fake, chunk=2).build(POINTS)
    assert chunked == whole
    assert len(fake.calls) == 4  # 2 source blocks x 2 destination blocks
    assert max(fake.calls) <= 4


def test_duplicates_are_requested_once_and_input_order_is_kept():
    fake = FakeOsrm()
    pts = [POINTS[1], POINTS[0], (POINTS[1][0] + 1e-7, POINTS[1][1]), POINTS[0]]
    m = provider(fake).build(pts)
    assert fake.calls == [2]  # only two distinct locations
    assert m.distance[0][2] == 0 and m.distance[1][3] == 0
    assert m.distance[0][1] == m.distance[2][3] > 0


def test_null_cells_become_unreachable():
    island = POINTS[2]
    fake = FakeOsrm(unreachable={(POINTS[0], island)})
    m = provider(fake).build(POINTS)
    assert m.distance[0][2] == UNREACHABLE and m.duration[0][2] == UNREACHABLE
    assert m.distance[2][0] > 0


def test_cache_key_ignores_input_order_and_skips_requests():
    cache = DictMatrixCache()
    fake = FakeOsrm()
    p = provider(fake, cache=cache, dataset_version="odisha|2026-11-18")
    first = p.build(POINTS)
    key = p.last_key
    second = p.build(list(reversed(POINTS)))
    assert p.last_key == key and len(fake.calls) == 1
    assert second.distance[0][3] == first.distance[3][0]
    other = provider(FakeOsrm(), cache=cache, dataset_version="odisha|2026-12-01")
    other.build(POINTS)
    assert other.last_key != key  # new map data invalidates the cache


def test_osrm_error_code_is_not_retryable():
    p = provider(lambda url: {"code": "TooBig", "message": "too many table coordinates"})
    with pytest.raises(OsrmError, match="TooBig") as exc:
        p.build(POINTS)
    assert not exc.value.retryable


def test_connection_failures_are_retried_then_reported(monkeypatch):
    attempts = []

    def refuse(url, timeout):
        attempts.append(url)
        raise urllib.error.URLError("connection refused")

    monkeypatch.setattr(urllib.request, "urlopen", refuse)
    p = OsrmMatrixProvider("http://osrm.test", retries=2, backoff_s=0)
    with pytest.raises(OsrmError) as exc:
        p.build(POINTS)
    assert exc.value.retryable and len(attempts) == 3


@pytest.mark.skipif(not os.environ.get("OSRM_URL"), reason="set OSRM_URL to run against OSRM")
def test_live_osrm_odisha():
    m = OsrmMatrixProvider(os.environ["OSRM_URL"]).build(POINTS)
    # Road distance must be at least the straight line and plausibly close to it.
    for i in range(4):
        for j in range(4):
            if i != j:
                assert (
                    haversine_m(POINTS[i], POINTS[j])
                    <= m.distance[i][j]
                    < 3 * haversine_m(POINTS[i], POINTS[j])
                )
