import json

from conftest import FIXTURES
from fastapi.testclient import TestClient

from optiroute.api import create_app
from optiroute.matrices import HaversineMatrixProvider, OsrmMatrixProvider

REQUEST = json.loads((FIXTURES / "solve-request.json").read_text())
client = TestClient(create_app(HaversineMatrixProvider()))


def plan(*routes):
    return {"request": REQUEST, "routes": [{"vehicle_id": v, "order_ids": o} for v, o in routes]}


def test_health_without_osrm():
    body = client.get("/internal/v1/health").json()
    assert body["status"] == "ok" and body["osrm"] == "not_configured"
    assert body["matrix_source"] == "HAVERSINE"


def test_health_reports_unreachable_osrm():
    down = TestClient(create_app(OsrmMatrixProvider("http://127.0.0.1:9")))
    body = down.get("/internal/v1/health").json()
    assert (body["status"], body["osrm"]) == ("degraded", "unreachable")


def test_valid_plan_is_scored():
    resp = client.post(
        "/internal/v1/validate",
        json=plan(("van-1", ["o-2", "o-1", "o-3", "o-6"]), ("van-2", ["o-4", "o-5"])),
    )
    assert resp.status_code == 200
    body = resp.json()
    assert body["status"] == "FEASIBLE" and body["validation"]["passed"]
    assert body["meta"]["algorithm"] == "EXTERNAL"
    assert body["kpis"]["vehicles_used"] == 2 and body["unassigned"] == []


def test_plan_violating_depot_compatibility_and_duplicates_is_rejected():
    resp = client.post(
        "/internal/v1/validate",
        json=plan(("van-1", ["o-4", "o-1"]), ("van-2", ["o-1", "o-5"])),
    )
    body = resp.json()
    codes = {v["code"] for v in body["validation"]["violations"]}
    assert body["status"] == "FAILED"
    assert {"VEHICLE_NOT_ALLOWED", "DUPLICATE_ASSIGNMENT"} <= codes
    offending = {v["order_id"] for v in body["validation"]["violations"]}
    assert {"o-4", "o-1"} <= offending


def test_unknown_ids_are_a_422():
    resp = client.post("/internal/v1/validate", json=plan(("van-9", ["o-1", "o-99"])))
    assert resp.status_code == 422
    assert resp.json()["detail"] == {
        "unknown_vehicle_ids": ["van-9"],
        "unknown_order_ids": ["o-99"],
    }
