"""Redis-backed matrix cache: zlib-compressed JSON, with a TTL."""

from __future__ import annotations

import json
import zlib

import redis

from optiroute.model import Matrix

WEEK_S = 7 * 24 * 3600


class RedisMatrixCache:
    def __init__(self, client: redis.Redis, ttl_s: int = WEEK_S) -> None:
        self.client = client
        self.ttl_s = ttl_s

    def get(self, key: str) -> Matrix | None:
        raw = self.client.get(key)
        if raw is None:
            return None
        data = json.loads(zlib.decompress(raw))
        return Matrix(data["distance"], data["duration"])

    def set(self, key: str, matrix: Matrix) -> None:
        payload = json.dumps({"distance": matrix.distance, "duration": matrix.duration})
        self.client.set(key, zlib.compress(payload.encode(), 6), ex=self.ttl_s)
