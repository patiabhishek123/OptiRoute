"""Load the demo CSVs into a fresh organization through the public API.

The dashboard only imports orders from CSV; depots, vehicles, drivers and driver assignments are
created here through the same API the dashboard uses. Needs `make api` running, or point --api
at a deployed instance.

    python3 optiroute-demo/load_demo.py [--date 2026-11-24] [--org kalinga-demo] [--rush]
    python3 optiroute-demo/load_demo.py --api https://13-233-182-99.sslip.io/api/v1

Orders loaded: orders-day.csv (140). With --rush, orders-rush.csv (12) is imported as well;
leave it out to upload it live from the Orders page as a mid-day re-optimization demo.
"""

from __future__ import annotations

import argparse
import csv
import json
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

API = "http://localhost:8080/api/v1"
HERE = Path(__file__).resolve().parent
EMAIL, PASSWORD = "dispatcher@kalinga.demo", "kalinga-demo-pass"


def call(method, path, body=None, token=None, ctype="application/json"):
    data = body if isinstance(body, bytes) else (json.dumps(body).encode() if body is not None else None)
    req = urllib.request.Request(API + path, data=data, method=method)
    req.add_header("Content-Type", ctype)
    if token:
        req.add_header("Authorization", "Bearer " + token)
    try:
        with urllib.request.urlopen(req) as r:
            return r.status, json.loads(r.read() or b"null")
    except urllib.error.HTTPError as e:
        return e.code, json.loads(e.read() or b"null")
    except urllib.error.URLError as e:
        sys.exit(f"cannot reach {API} ({e.reason}); is `make api` running?")


def ok(status, body, *want):
    if status not in want:
        sys.exit(f"expected {want}, got {status}: {json.dumps(body)[:600]}")
    return body


def rows(name):
    with open(HERE / name, newline="") as f:
        return list(csv.DictReader(f))


def main():
    global API
    ap = argparse.ArgumentParser()
    ap.add_argument("--date", default="2026-11-24", help="planning date for orders and assignments")
    ap.add_argument("--org", default=f"kalinga-{int(time.time())}", help="organization slug to create")
    ap.add_argument("--rush", action="store_true", help="also import orders-rush.csv")
    ap.add_argument("--api", default=API, help=f"API base URL (default {API})")
    a = ap.parse_args()
    API = a.api.rstrip("/")

    s = ok(*call("POST", "/organizations", {"name": "Kalinga Couriers", "slug": a.org,
              "admin_email": EMAIL, "admin_password": PASSWORD}), 201)
    tok = s["tokens"]["access_token"]

    depots = {d["name"]: ok(*call("POST", "/depots", {"name": d["name"], "lat": float(d["lat"]),
              "lon": float(d["lon"])}, tok), 201)["id"] for d in rows("depots.csv")}
    vehicles = {
        v["registration_number"]: ok(*call("POST", "/vehicles", {
            "depot_id": depots[v["depot"]], "registration_number": v["registration_number"],
            "vehicle_type": v["vehicle_type"], "capacity_weight_kg": float(v["capacity_weight_kg"]),
            "capacity_volume_m3": float(v["capacity_volume_m3"]), "cost_per_km": float(v["cost_per_km"]),
            "fixed_cost": float(v["fixed_cost"])}, tok), 201)["id"]
        for v in rows("vehicles.csv")
    }
    drivers = {
        d["name"]: ok(*call("POST", "/drivers", {
            "name": d["name"], "phone": d["phone"], "shift_start": d["shift_start"], "shift_end": d["shift_end"],
            "break_duration_minutes": int(d["break_duration_minutes"]),
            "max_overtime_minutes": int(d["max_overtime_minutes"])}, tok), 201)["id"]
        for d in rows("drivers.csv")
    }
    pairs = [{"vehicle_id": vehicles[r["registration_number"]], "driver_id": drivers[r["driver_name"]]}
             for r in rows("assignments.csv")]
    ok(*call("PUT", f"/driver-assignments/{a.date}", {"assignments": pairs}, tok), 200)

    files = ["orders-day.csv"] + (["orders-rush.csv"] if a.rush else [])
    for name in files:
        res = ok(*call("POST", f"/orders/bulk-import?planning_date={a.date}",
                       (HERE / name).read_bytes(), tok, "text/csv"), 200, 201)
        print(f"imported {name}: {res}")

    print(f"\n{len(depots)} depots, {len(vehicles)} vehicles, {len(drivers)} drivers, "
          f"{len(pairs)} assignments for {a.date}")
    site = API.removesuffix("/api/v1") if API != "http://localhost:8080/api/v1" else "http://localhost:5173"
    print(f"Log in at {site}  organization={a.org}  email={EMAIL}  password={PASSWORD}")


if __name__ == "__main__":
    main()
