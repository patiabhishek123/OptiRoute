"""Internal optimizer HTTP API (spec §12.8): health and synchronous plan validation.

Solving is never done over HTTP: it goes through the Redis queue (``optiroute worker``).
``POST /internal/v1/validate`` lets the Go backend re-check any plan, e.g. a dispatcher-edited
one, with the same independent validator and cost function the solvers are judged by.

Run: ``optiroute serve`` (OSRM_URL set -> road matrices, otherwise haversine).
"""

from __future__ import annotations

import os
import time
import urllib.error
import urllib.request

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel, ConfigDict

from optiroute import __version__
from optiroute.contracts import v1
from optiroute.matrices import (
    DictMatrixCache,
    HaversineMatrixProvider,
    MatrixProvider,
    OsrmError,
    OsrmMatrixProvider,
)
from optiroute.model import Route, Solution
from optiroute.pipeline import score
from optiroute.preprocessing import UnsupportedRequestError, build_instance
from optiroute.solvers import SolverRun


class PlannedRoute(BaseModel):
    model_config = ConfigDict(extra="forbid")
    vehicle_id: str
    order_ids: list[str]


class ValidateRequest(BaseModel):
    model_config = ConfigDict(extra="forbid")
    contract_version: str = v1.CONTRACT_VERSION
    request: v1.SolveRequest
    routes: list[PlannedRoute]


def provider_from_env() -> MatrixProvider:
    url = os.environ.get("OSRM_URL")
    if not url:
        return HaversineMatrixProvider()
    version = os.environ.get("OSRM_DATASET_VERSION", "unknown")
    return OsrmMatrixProvider(url, dataset_version=version, cache=DictMatrixCache())


def create_app(provider: MatrixProvider | None = None) -> FastAPI:
    provider = provider or provider_from_env()
    app = FastAPI(title="OptiRoute optimizer (internal)", version=__version__)

    @app.get("/internal/v1/health")
    def health() -> dict:
        osrm = "not_configured"
        if isinstance(provider, OsrmMatrixProvider):
            osrm = "ok" if _osrm_alive(provider.base_url) else "unreachable"
        return {
            "status": "ok" if osrm != "unreachable" else "degraded",
            "version": __version__,
            "contract_version": v1.CONTRACT_VERSION,
            "matrix_source": provider.source,
            "osrm": osrm,
        }

    @app.post("/internal/v1/validate", response_model=v1.SolveResult)
    def validate_plan(body: ValidateRequest) -> v1.SolveResult:
        req = body.request
        vehicle_idx = {v.id: k for k, v in enumerate(req.vehicles)}
        order_node = {o.id: len(req.depots) + i for i, o in enumerate(req.orders)}
        unknown_vehicles = sorted({r.vehicle_id for r in body.routes} - vehicle_idx.keys())
        unknown_orders = sorted({o for r in body.routes for o in r.order_ids} - order_node.keys())
        if unknown_vehicles or unknown_orders:
            raise HTTPException(
                422,
                {"unknown_vehicle_ids": unknown_vehicles, "unknown_order_ids": unknown_orders},
            )
        started = time.perf_counter()
        try:
            instance = build_instance(req, provider)
        except UnsupportedRequestError as e:
            raise HTTPException(422, str(e)) from e
        except OsrmError as e:
            raise HTTPException(503 if e.retryable else 422, str(e)) from e
        solution = Solution(
            [
                Route(vehicle_idx[r.vehicle_id], [order_node[o] for o in r.order_ids])
                for r in body.routes
            ]
        )
        return score(
            req, instance, SolverRun(solution), provider, algorithm="EXTERNAL", started=started
        )

    return app


def _osrm_alive(base_url: str) -> bool:
    try:
        urllib.request.urlopen(f"{base_url}/nearest/v1/driving/85.82,20.29", timeout=2).close()
    except urllib.error.HTTPError:
        return True  # the router answered, even if the probe point is off-map
    except (urllib.error.URLError, TimeoutError, ConnectionError):
        return False
    return True
