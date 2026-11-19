"""M3 end-to-end check through the public API (spec 18.3), run with `make e2e`.

Needs `make up`, `make osrm-up`, `make api` and `make worker` running. Creates a fresh organization
each run: signup -> depot -> 6 paired vans -> CSV import of 100 orders -> job (with an idempotent
retry) -> worker on OSRM -> DRAFT plan -> approve -> activate -> a new job finds nothing to plan.
Standard library only; exits non-zero on the first failed step.
"""

import json
import math
import random
import sys
import time
import urllib.error
import urllib.request

API = "http://localhost:8080/api/v1"


def call(method, path, body=None, token=None, ctype="application/json", headers=None):
    data = body if isinstance(body, bytes) else (json.dumps(body).encode() if body is not None else None)
    req = urllib.request.Request(API + path, data=data, method=method)
    req.add_header("Content-Type", ctype)
    if token:
        req.add_header("Authorization", "Bearer " + token)
    for k, v in (headers or {}).items():
        req.add_header(k, v)
    try:
        with urllib.request.urlopen(req) as r:
            return r.status, json.loads(r.read() or b"null")
    except urllib.error.HTTPError as e:
        return e.code, json.loads(e.read() or b"null")


def ok(status, body, want):
    if status != want:
        sys.exit(f"expected {want}, got {status}: {json.dumps(body)[:600]}")
    return body


slug = f"e2e-{int(time.time())}"
s = ok(*call("POST", "/organizations", {"name": "E2E Couriers", "slug": slug,
       "admin_email": "ops@e2e.test", "admin_password": "end-to-end-pass"}), 201)
tok = s["tokens"]["access_token"]
print("1. org", slug)

depot = ok(*call("POST", "/depots", {"name": "Bhubaneswar Hub", "lat": 20.2961, "lon": 85.8245}, tok), 201)
pairs = []
for i in range(6):
    v = ok(*call("POST", "/vehicles", {"depot_id": depot["id"], "registration_number": f"OD-02-E2E-{i:02d}",
           "vehicle_type": "van", "capacity_weight_kg": 400, "capacity_volume_m3": 3,
           "cost_per_km": 12, "fixed_cost": 600}, tok), 201)
    d = ok(*call("POST", "/drivers", {"name": f"Driver {i}", "shift_start": "08:00", "shift_end": "18:00",
           "break_duration_minutes": 30, "max_overtime_minutes": 60}, tok), 201)
    pairs.append({"vehicle_id": v["id"], "driver_id": d["id"]})
ok(*call("PUT", "/driver-assignments/2026-11-02", {"assignments": pairs}, tok), 200)
print("2. depot + 6 paired vehicles")

rng = random.Random(42)
rows = ["order_id,customer,lat,lon,weight_kg,from,to,service_minutes"]
for i in range(100):
    r, th = abs(rng.gauss(0, 6)), rng.uniform(0, 2 * math.pi)
    lat = 20.2961 + r * math.cos(th) / 111.32
    lon = 85.8245 + r * math.sin(th) / (111.32 * math.cos(math.radians(20.3)))
    frm, to = rng.choice([("09:00", "13:00"), ("13:00", "18:00"), ("09:00", "18:00")])
    rows.append(f"E2E-{i:03d},Customer {i},{lat:.5f},{lon:.5f},{rng.uniform(2, 25):.1f},{frm},{to},5")
imp = ok(*call("POST", "/orders/bulk-import?planning_date=2026-11-02", "\n".join(rows).encode(), tok, "text/csv"), 201)
print("3. imported", imp)

body = {"planning_date": "2026-11-02", "solver": {"algorithm": "ORTOOLS_GLS", "time_limit_seconds": 10}}
job = ok(*call("POST", "/optimization/jobs", body, tok, headers={"Idempotency-Key": f"{slug}-run1"}), 202)
again = ok(*call("POST", "/optimization/jobs", body, tok, headers={"Idempotency-Key": f"{slug}-run1"}), 200)
assert again["id"] == job["id"], "idempotency broken"
print("4. job", job["id"], job["status"], f"orders={job['order_count']} vehicles={job['vehicle_count']}")

seen = []
for _ in range(120):
    j = ok(*call("GET", f"/optimization/jobs/{job['id']}", None, tok), 200)
    if not seen or seen[-1] != j["status"]:
        seen.append(j["status"])
    if j["status"] in ("COMPLETED", "FAILED", "INFEASIBLE", "TIME_LIMIT", "CANCELLED"):
        break
    time.sleep(0.5)
print("5. status path:", " -> ".join(seen))
if j["status"] != "COMPLETED":
    sys.exit(f"job ended {j['status']}: {j['error_message']}")

res = ok(*call("GET", f"/optimization/jobs/{job['id']}/result", None, tok), 200)
k, m = res["kpis"], res["meta"]
print(f"6. result {res['status']} validation={res['validation']['passed']} vehicles={k['vehicles_used']} "
      f"km={k['distance_m']/1000:.1f} late={k['late_stops']} unassigned={len(res['unassigned'])} "
      f"matrix={m['matrix_source']} solve_ms={m['execution_ms']}")

for _ in range(40):
    _, plans = call("GET", "/route-plans?planning_date=2026-11-02", None, tok)
    mine = [p for p in plans["items"] if p["optimization_job_id"] == job["id"]]
    if mine:
        break
    time.sleep(0.25)
else:
    sys.exit("no plan was materialized for the job")
plan = ok(*call("GET", f"/route-plans/{mine[0]['id']}", None, tok), 200)
stops = sum(len(r["stops"]) for r in plan["routes"])
first = plan["routes"][0]
print(f"7. plan v{plan['version']} {plan['status']} routes={len(plan['routes'])} stops={stops} "
      f"km={plan['total_distance_km']:.1f} unassigned={plan['unassigned_count']}; "
      f"{first['registration_number']}/{first['driver_name']}: first stop {first['stops'][0]['external_order_id']} "
      f"at {first['stops'][0]['planned_arrival']}")
_, o = call("GET", "/orders?planning_date=2026-11-02&status=PENDING&limit=500", None, tok)
print(f"8. orders still PENDING after draft: {len(o['items'])}")
assert len(o["items"]) == 100, "a DRAFT plan must not change order state"

pid = plan["id"]
ok(*call("POST", f"/route-plans/{pid}/approve", None, tok), 200)
act = ok(*call("POST", f"/route-plans/{pid}/activate", None, tok), 200)
_, planned = call("GET", "/orders?planning_date=2026-11-02&status=PLANNED&limit=500", None, tok)
_, active = call("GET", "/route-plans?planning_date=2026-11-02&status=ACTIVE", None, tok)
print(f"9. approved + activated: plan {act['plan']['status']}, orders PLANNED={len(planned['items'])}, "
      f"active plans for the day={len(active['items'])}")
assert act["plan"]["status"] == "ACTIVE" and len(planned["items"]) == 100 and len(active["items"]) == 1
st, again = call("POST", "/optimization/jobs", body, tok)
print(f"10. new job with everything planned -> {st} {again['error']['code']}")
assert st == 422 and again["error"]["code"] == "NO_ORDERS"
print("M3 end-to-end: OK")
