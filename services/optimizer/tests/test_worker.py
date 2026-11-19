"""Worker integration tests against the dev Postgres + Redis (`make up`); skipped if unreachable."""

import json
import os
import re
import time
import uuid
from pathlib import Path

import pytest

psycopg = pytest.importorskip("psycopg")
redis = pytest.importorskip("redis")

from conftest import FIXTURES  # noqa: E402
from psycopg.types.json import Jsonb  # noqa: E402

from optiroute.matrices import HaversineMatrixProvider, OsrmError  # noqa: E402
from optiroute.model import Matrix  # noqa: E402
from optiroute.worker import Worker, WorkerConfig  # noqa: E402
from optiroute.worker.cache import RedisMatrixCache  # noqa: E402

DB_URL = os.environ.get(
    "TEST_DATABASE_URL", "postgres://optiroute:optiroute@localhost:15432/optiroute"
)
REDIS_URL = os.environ.get("TEST_REDIS_URL", "redis://localhost:16379/15")
MIGRATION = Path(__file__).resolve().parents[2] / "backend/migrations/00001_optimization_jobs.sql"
ORG = "00000000-0000-0000-0000-0000000000aa"
REQUEST = json.loads((FIXTURES / "solve-request.json").read_text())


def _up_section(sql: str) -> str:
    return re.search(r"-- \+goose Up(.*?)-- \+goose Down", sql, re.S).group(1)


@pytest.fixture(scope="module")
def db():
    try:
        conn = psycopg.connect(DB_URL, autocommit=True, connect_timeout=2)
        redis.Redis.from_url(REDIS_URL, socket_connect_timeout=2).ping()
    except Exception as e:
        pytest.skip(f"dev Postgres/Redis not reachable ({e}); run `make up`")
    exists = conn.execute("SELECT to_regclass('optimization.jobs')").fetchone()[0]
    if exists is None:
        conn.execute(_up_section(MIGRATION.read_text()))
    yield conn
    conn.execute("DELETE FROM optimization.jobs WHERE organization_id = %s", (ORG,))
    conn.close()


@pytest.fixture
def make_worker(db):
    streams = []

    def factory(provider=None, **overrides) -> Worker:
        tag = uuid.uuid4().hex[:8]
        cfg = WorkerConfig(
            database_url=DB_URL, redis_url=REDIS_URL, stream=f"test:{tag}:jobs",
            events=f"test:{tag}:events", dlq=f"test:{tag}:dlq", **{"block_ms": 100, **overrides},
        )  # fmt: skip
        streams.extend([cfg.stream, cfg.events, cfg.dlq])
        w = Worker(cfg, provider or HaversineMatrixProvider())
        w.ensure_group()
        return w

    yield factory
    if streams:
        redis.Redis.from_url(REDIS_URL).delete(*streams)


def submit(db, worker: Worker, request=None, algorithm="NEAREST_NEIGHBOUR") -> str:
    req = json.loads(json.dumps(request or REQUEST))
    req["solver"]["algorithm"] = algorithm
    (job_id,) = db.execute(
        "INSERT INTO optimization.jobs (organization_id, input_snapshot) "
        "VALUES (%s, %s) RETURNING id",
        (ORG, Jsonb(req)),
    ).fetchone()
    worker.redis.xadd(worker.cfg.stream, {"job_id": str(job_id)})
    return str(job_id)


def job(db, job_id):
    return db.execute(
        "SELECT status, attempts, result, error_message, total_distance_km "
        "FROM optimization.jobs WHERE id = %s",
        (job_id,),
    ).fetchone()


def events(worker: Worker):
    return [f for _, f in worker.redis.xrange(worker.cfg.events)]


def pending(worker: Worker) -> int:
    return worker.redis.xpending(worker.cfg.stream, worker.cfg.group)["pending"]


def test_job_completes_and_publishes_event(db, make_worker):
    w = make_worker()
    job_id = submit(db, w)
    assert w.run_once() == 1
    status, attempts, result, error, km = job(db, job_id)
    assert (status, attempts, error) == ("COMPLETED", 1, None)
    assert result["status"] == "FEASIBLE" and result["validation"]["passed"]
    assert km > 0
    assert events(w) == [{"job_id": job_id, "status": "COMPLETED"}]
    assert pending(w) == 0


def test_duplicate_message_is_ignored(db, make_worker):
    w = make_worker()
    job_id = submit(db, w)
    w.run_once()
    w.redis.xadd(w.cfg.stream, {"job_id": job_id})
    w.run_once()
    assert job(db, job_id)[:2] == ("COMPLETED", 1)
    assert len(events(w)) == 1 and pending(w) == 0


def test_cancel_before_start(db, make_worker):
    w = make_worker()
    job_id = submit(db, w)
    db.execute("UPDATE optimization.jobs SET cancel_requested = true WHERE id = %s", (job_id,))
    w.run_once()
    assert job(db, job_id)[0] == "CANCELLED"


def test_cancel_during_preprocessing(db, make_worker):
    class CancellingProvider(HaversineMatrixProvider):
        def build(self, points):
            db.execute("UPDATE optimization.jobs SET cancel_requested = true WHERE id = %s", (jid,))
            return super().build(points)

    w = make_worker(CancellingProvider())
    jid = submit(db, w)
    w.run_once()
    assert job(db, jid)[0] == "CANCELLED"


def test_invalid_input_fails_without_retry(db, make_worker):
    w = make_worker()
    job_id = submit(db, w, request={**REQUEST, "vehicles": []})
    w.run_once()
    status, attempts, _, error, _ = job(db, job_id)
    assert (status, attempts) == ("FAILED", 1) and "invalid input" in error
    assert w.redis.xlen(w.cfg.stream) == 1  # not re-queued


def test_retryable_failure_requeues_then_dead_letters(db, make_worker):
    class DownProvider:
        source = "OSRM"

        def build(self, points):
            raise OsrmError("connection refused", retryable=True)

    w = make_worker(DownProvider(), max_attempts=2)
    job_id = submit(db, w)
    w.run_once()
    assert job(db, job_id)[:2] == ("QUEUED", 1)
    w.run_once()
    status, attempts, _, error, _ = job(db, job_id)
    assert (status, attempts) == ("FAILED", 2) and "giving up" in error
    assert w.redis.xlen(w.cfg.dlq) == 1
    assert events(w) == [{"job_id": job_id, "status": "FAILED"}]


def test_crashed_worker_message_is_reclaimed(db, make_worker):
    crashed = make_worker(lease_ms=200)
    job_id = submit(db, crashed)
    # The crashed consumer read the message and claimed the job, then died before acking.
    crashed.redis.xreadgroup(crashed.cfg.group, "dead-consumer", {crashed.cfg.stream: ">"}, count=1)
    db.execute(
        "UPDATE optimization.jobs SET status = 'SOLVING', attempts = 1 WHERE id = %s", (job_id,)
    )
    time.sleep(0.3)
    assert crashed.run_once() == 1  # this consumer takes it over
    assert job(db, job_id)[:2] == ("COMPLETED", 2)
    assert pending(crashed) == 0


def test_redis_matrix_cache_roundtrip(db):
    client = redis.Redis.from_url(REDIS_URL)
    cache = RedisMatrixCache(client, ttl_s=60)
    key = f"test:matrix:{uuid.uuid4().hex}"
    m = Matrix([[0, 5], [6, 0]], [[0, 1], [2, 0]])
    cache.set(key, m)
    assert cache.get(key) == m and 0 < client.ttl(key) <= 60
    assert cache.get(key + "x") is None
    client.delete(key)


def test_idle_read_longer_than_redis_default_timeout(db, make_worker):
    # Regression: redis-py 8 defaults socket_timeout to 5 s; a 6 s idle block must not raise.
    w = make_worker(block_ms=6000)
    assert w.run_once() == 0


def test_transient_redis_error_does_not_kill_the_loop(db, make_worker, monkeypatch):
    w = make_worker()
    calls = []

    def flaky():
        calls.append(1)
        if len(calls) == 1:
            raise redis.ConnectionError("redis restarted")
        w.stop()
        return 0

    monkeypatch.setattr(w, "run_once", flaky)
    monkeypatch.setattr(w._stop, "wait", lambda timeout: False)  # no real backoff sleep
    w.run_forever()
    assert len(calls) == 2
