"""Job worker: consume ``opti:jobs``, solve, persist the result, publish ``opti:job-events``.

Delivery is at-least-once, so every step is written to be safe when repeated:

* claim: a conditional UPDATE only matches jobs in an active state. If nothing matches (the job
  is finished or cancelled), the message is acked and skipped.
* lease: unacked messages idle for longer than ``lease_ms`` are taken over by another worker
  (XAUTOCLAIM). A heartbeat re-claims the worker's own message during long solves, so a slow job
  is not stolen.
* retry: retryable failures (OSRM unavailable, unexpected errors) re-queue the job, up to
  ``max_attempts``. After that the job is FAILED and the message is copied to the DLQ. Invalid
  input fails immediately.
* cancellation: ``cancel_requested`` is checked when the job is claimed and at each stage change.
* the result is written before the ack. The optimizer writes only solver fields on
  ``optimization.jobs``; Go turns the result into route plans (docs/ARCHITECTURE.md §2).
"""

from __future__ import annotations

import logging
import os
import signal
import socket
import threading
from dataclasses import dataclass, field
from pathlib import Path

import psycopg
import redis
from psycopg.rows import dict_row
from psycopg.types.json import Jsonb
from pydantic import ValidationError

from optiroute import __version__
from optiroute.contracts import v1
from optiroute.matrices import (
    HaversineMatrixProvider,
    MatrixProvider,
    OsrmError,
    OsrmMatrixProvider,
)
from optiroute.pipeline import solve_request
from optiroute.worker.cache import RedisMatrixCache

log = logging.getLogger("optiroute.worker")

ACTIVE = ["QUEUED", "PREPROCESSING", "SOLVING", "VALIDATING"]
JOB_STATUS = {  # SolveResult.status -> optimization.jobs.status
    v1.Status.FEASIBLE: "COMPLETED",
    v1.Status.INFEASIBLE: "INFEASIBLE",
    v1.Status.TIME_LIMIT: "TIME_LIMIT",
    v1.Status.FAILED: "FAILED",
}


class Cancelled(Exception):
    pass


@dataclass(frozen=True)
class WorkerConfig:
    database_url: str
    redis_url: str
    osrm_url: str | None = None
    osrm_dataset_version: str = "unknown"
    stream: str = "opti:jobs"
    events: str = "opti:job-events"
    dlq: str = "opti:jobs:dlq"
    group: str = "optimizers"
    consumer: str = field(default_factory=lambda: f"{socket.gethostname()}-{os.getpid()}")
    max_attempts: int = 3
    lease_ms: int = 120_000
    block_ms: int = 5_000

    @classmethod
    def from_env(cls) -> WorkerConfig:
        env = os.environ
        version = env.get("OSRM_DATASET_VERSION")
        if not version and env.get("OSRM_DATASET_VERSION_FILE"):
            version = Path(env["OSRM_DATASET_VERSION_FILE"]).read_text().strip()
        return cls(
            database_url=env["DATABASE_URL"],
            redis_url=env["REDIS_URL"],
            osrm_url=env.get("OSRM_URL") or None,
            osrm_dataset_version=version or "unknown",
            max_attempts=int(env.get("WORKER_MAX_ATTEMPTS", 3)),
            lease_ms=int(env.get("WORKER_LEASE_MS", 120_000)),
        )


class Worker:
    def __init__(self, config: WorkerConfig, provider: MatrixProvider | None = None) -> None:
        self.cfg = config
        # redis-py >= 8 defaults socket_timeout to 5 s, equal to the XREADGROUP block time, so an
        # idle worker would time out on every empty read. Allow the block plus a margin.
        self.redis = redis.Redis.from_url(
            config.redis_url,
            decode_responses=True,
            socket_timeout=config.block_ms / 1000 + 10,
            health_check_interval=30,
        )
        self.db = psycopg.connect(config.database_url, autocommit=True, row_factory=dict_row)
        self.provider = provider or self._default_provider()
        self._stop = threading.Event()

    def _default_provider(self) -> MatrixProvider:
        if not self.cfg.osrm_url:
            log.warning("OSRM_URL not set: using straight-line (haversine) matrices")
            return HaversineMatrixProvider()
        cache_client = redis.Redis.from_url(self.cfg.redis_url)  # binary values
        return OsrmMatrixProvider(
            self.cfg.osrm_url,
            dataset_version=self.cfg.osrm_dataset_version,
            cache=RedisMatrixCache(cache_client),
        )

    # ------------------------------------------------------------------ loop

    def ensure_group(self) -> None:
        try:
            self.redis.xgroup_create(self.cfg.stream, self.cfg.group, id="0", mkstream=True)
        except redis.ResponseError as e:
            if "BUSYGROUP" not in str(e):
                raise

    def stop(self, *_: object) -> None:
        self._stop.set()

    def run_forever(self) -> None:
        self.ensure_group()
        signal.signal(signal.SIGTERM, self.stop)
        signal.signal(signal.SIGINT, self.stop)
        log.info("worker %s consuming %s", self.cfg.consumer, self.cfg.stream)
        backoff = 1.0
        while not self._stop.is_set():
            try:
                self.run_once()
                backoff = 1.0
            except (redis.ConnectionError, redis.TimeoutError, psycopg.OperationalError) as e:
                # Infrastructure blip: the unacked message stays pending and is retried or
                # reclaimed, so the worker only needs to stay alive and reconnect.
                log.warning("transient error, retrying in %.0fs: %s", backoff, e)
                self._stop.wait(backoff)
                backoff = min(backoff * 2, 30.0)
                if self.db.closed:
                    self.db = psycopg.connect(
                        self.cfg.database_url, autocommit=True, row_factory=dict_row
                    )

    def run_once(self) -> int:
        """Process at most one message (a reclaimed stale one first). Returns messages handled."""
        _, claimed, _ = self.redis.xautoclaim(
            self.cfg.stream,
            self.cfg.group,
            self.cfg.consumer,
            min_idle_time=self.cfg.lease_ms,
            start_id="0-0",
            count=1,
        )
        if claimed:
            log.warning("reclaimed stale message %s", claimed[0][0])
            messages = claimed
        else:
            reply = self.redis.xreadgroup(
                self.cfg.group,
                self.cfg.consumer,
                {self.cfg.stream: ">"},
                count=1,
                block=self.cfg.block_ms,
            )
            messages = reply[0][1] if reply else []
        for msg_id, fields in messages:
            self.handle(msg_id, fields)
        return len(messages)

    # ------------------------------------------------------------------ one job

    def handle(self, msg_id: str, fields: dict[str, str]) -> None:
        job_id = fields.get("job_id")
        heartbeat = _Heartbeat(self, msg_id)
        heartbeat.start()
        try:
            status = self._process(job_id) if job_id else None
        finally:
            heartbeat.stop()
        if status:
            self.redis.xadd(self.cfg.events, {"job_id": job_id, "status": status})
        self.redis.xack(self.cfg.stream, self.cfg.group, msg_id)

    def _process(self, job_id: str) -> str | None:
        row = self.db.execute(
            """UPDATE optimization.jobs
               SET status = 'PREPROCESSING', attempts = attempts + 1,
                   started_at = coalesce(started_at, now())
               WHERE id = %s AND status = ANY(%s)
               RETURNING input_snapshot, cancel_requested, attempts""",
            (job_id, ACTIVE),
        ).fetchone()
        if row is None:
            log.info("job %s is not active; skipping", job_id)
            return None
        if row["cancel_requested"]:
            return self._finish(job_id, "CANCELLED", error="cancelled before start")
        if row["attempts"] > self.cfg.max_attempts:
            return self._dead_letter(job_id, "exceeded max attempts")

        try:
            req = v1.SolveRequest.model_validate(row["input_snapshot"])
            result = solve_request(req, self.provider, on_stage=lambda s: self._stage(job_id, s))
        except Cancelled:
            return self._finish(job_id, "CANCELLED", error="cancelled")
        except OsrmError as e:
            if e.retryable:
                return self._retry(job_id, row["attempts"], str(e))
            return self._finish(job_id, "FAILED", error=str(e))
        except (ValidationError, ValueError, NotImplementedError) as e:
            return self._finish(job_id, "FAILED", error=f"invalid input: {e}")
        except Exception as e:  # bug or infrastructure fault: retry, bounded by attempts
            log.exception("job %s crashed", job_id)
            return self._retry(job_id, row["attempts"], f"{type(e).__name__}: {e}")
        return self._complete(job_id, result)

    def _stage(self, job_id: str, stage: str) -> None:
        row = self.db.execute(
            """UPDATE optimization.jobs SET status = %s
               WHERE id = %s AND status = ANY(%s) RETURNING cancel_requested""",
            (stage, job_id, ACTIVE),
        ).fetchone()
        if row is None or row["cancel_requested"]:
            raise Cancelled

    def _complete(self, job_id: str, result: v1.SolveResult) -> str:
        status = JOB_STATUS[result.status]
        self.db.execute(
            """UPDATE optimization.jobs
               SET status = %s, result = %s, objective_value = %s, total_distance_km = %s,
                   execution_time_ms = %s, solver_name = %s, solver_version = %s,
                   error_message = NULL, completed_at = now()
               WHERE id = %s AND status = ANY(%s)""",
            (
                status,
                Jsonb(result.model_dump(mode="json")),
                result.objective.total if result.objective else None,
                result.kpis.distance_m / 1000 if result.kpis else None,
                result.meta.execution_ms,
                result.meta.algorithm,
                __version__,
                job_id,
                ACTIVE,
            ),
        )
        log.info("job %s -> %s in %d ms", job_id, status, result.meta.execution_ms)
        return status

    def _finish(self, job_id: str, status: str, *, error: str) -> str:
        self.db.execute(
            """UPDATE optimization.jobs
               SET status = %s, error_message = %s, completed_at = now()
               WHERE id = %s AND status = ANY(%s)""",
            (status, error, job_id, ACTIVE),
        )
        log.info("job %s -> %s (%s)", job_id, status, error)
        return status

    def _retry(self, job_id: str, attempts: int, error: str) -> str | None:
        if attempts >= self.cfg.max_attempts:
            return self._dead_letter(job_id, f"giving up after {attempts} attempts: {error}")
        self.db.execute(
            "UPDATE optimization.jobs SET status = 'QUEUED', error_message = %s WHERE id = %s",
            (error, job_id),
        )
        self.redis.xadd(self.cfg.stream, {"job_id": job_id})
        log.warning("job %s re-queued (attempt %d): %s", job_id, attempts, error)
        return None  # still in flight: no event yet

    def _dead_letter(self, job_id: str, error: str) -> str:
        self.redis.xadd(self.cfg.dlq, {"job_id": job_id, "error": error})
        return self._finish(job_id, "FAILED", error=error)


class _Heartbeat:
    """Keeps this worker's claim on a message fresh while a long solve runs."""

    def __init__(self, worker: Worker, msg_id: str) -> None:
        self.worker, self.msg_id = worker, msg_id
        self._done = threading.Event()
        self._thread = threading.Thread(target=self._beat, daemon=True)

    def start(self) -> None:
        self._thread.start()

    def stop(self) -> None:
        self._done.set()
        self._thread.join()

    def _beat(self) -> None:
        cfg = self.worker.cfg
        while not self._done.wait(cfg.lease_ms / 3000):
            self.worker.redis.xclaim(
                cfg.stream,
                cfg.group,
                cfg.consumer,
                min_idle_time=0,
                message_ids=[self.msg_id],
                justid=True,
            )
