# OptiRoute Architecture

This is a condensed version of the full specification
(`OptiRoute_Technical_Architecture_Implementation_Plan.docx`). Where the spec is silent or
contradicts itself, the decision taken here is marked **Decision**.

## 1. Components

```
React dashboard ──► Go API (modular monolith) ──► PostgreSQL + PostGIS   (source of truth)
                          │   ▲
              outbox ──►  │   │  job events
                          ▼   │
                     Redis Streams
                          │
                          ▼
                Python optimizer worker ──► OSRM (OpenStreetMap extract)
```

| Deployable | Owns | Talks to |
|---|---|---|
| `opti-api` (Go) | All business tables, auth, tenancy, job creation, plan materialization, dispatch | Postgres, Redis |
| `opti-optimizer` (Python) | Matrices, solving, validation; writes **only** solver fields on `optimization.jobs` | Postgres (jobs table only), Redis, OSRM |
| `opti-web` (TS) | Dispatcher UI | Go API only |
| `opti-routing` | OSRM `/table` and `/route` | — |

Go modules (`internal/`): `identity`, `orders`, `fleet`, `geography`, `planning`, `dispatch`, `tracking`, `analytics`.
Modules call each other through exported interfaces only. Only `planning` talks to the optimizer.

## 2. Main flow: static optimization

1. `POST /api/v1/optimization/jobs` → `planning` builds a **complete problem snapshot** (orders, vehicles, driver shifts, depots, weights).
2. In one transaction: insert the `optimization.jobs` row (`status=QUEUED`, `input_snapshot`, `idempotency_key`) and insert an outbox row.
3. The outbox relay publishes `{job_id}` to stream `opti:jobs`.
4. Worker `XREADGROUP` → conditional `UPDATE ... SET status='PREPROCESSING' WHERE status='QUEUED'`. If no row is updated, it acks and skips, which makes processing idempotent.
5. PREPROCESSING (validate input, build matrix) → SOLVING → VALIDATING → write `result` JSONB + metrics, then set status to `COMPLETED`, `INFEASIBLE`, `TIME_LIMIT` or `FAILED`.
6. Worker publishes `{job_id, status}` to `opti:job-events` and acks.
7. Go consumes the event, **materializes** `route_plans/routes/route_stops` (status `DRAFT`) in one transaction, and notifies the dashboard over SSE.
8. Dispatcher compares the plan with the baseline and approves it. Activation swaps assignments atomically (see §6).

**Decision:** the optimizer never writes `routing.*` tables. Go remains the only writer of business state, and the optimizer's output is plain data that Go can re-validate.

**Decision:** solve jobs go through the queue only. The optimizer's internal HTTP API is limited to `GET /health` and a synchronous `POST /validate`. A `solve` CLI is used for benchmarks and local development.

## 3. Dynamic re-optimization

Triggered by a new order, a breakdown (`POST /routes/:id/disruptions`) or a manual request. It uses the same job pipeline with `kind=REOPTIMIZE`. The snapshot adds:
- current vehicle positions, used as virtual start nodes with a start time of *now*
- completed and in-progress stops, which are frozen and excluded from the problem
- the previous active plan, used both for stability costs and as a warm start

The worker solves the remaining problem and evaluates the full objective including stability. It marks `recommend_publish = (improvement ≥ threshold)`.
Go enforces a cooldown and a route-version check, because the plan must not have changed since the snapshot was taken. Nothing reaches a driver without dispatcher approval.

## 4. Optimizer contract (v1)

JSON Schema in `packages/api-contracts/optimizer/v1/` is the source of truth. Python uses pydantic models and Go uses generated structs.
Regenerate the schemas with `make schemas`; a test fails if they drift from the pydantic models.
**Status semantics:**
- `INFEASIBLE`: the only violations are unserved orders while `allow_unassigned=false`.
- `FAILED`: any other violation (the solver produced an invalid plan), or invalid input.
- A structurally broken plan has `objective`, `kpis` and `routes` set to null or empty, and only its violations are reported.
**Units inside the contract are integers: metres, seconds since local midnight of `planning_date`, grams, and cm³.** OR-Tools requires integer costs, so conversion happens once, in Go.

```jsonc
// SolveRequest
{
  "contract_version": "1.0", "job_id": "...", "kind": "SOLVE",            // or REOPTIMIZE
  "planning_date": "2026-11-02", "timezone": "Asia/Kolkata",
  "objective": { "profile": "COST_BALANCED", "weights": { "lateness_per_min": 50, "unserved": 1000000,
                 "overtime_per_min": 20, "reassign": 500, "reorder": 100 } },
  "solver": { "algorithm": "HYBRID_GLS", "time_limit_s": 15, "allow_unassigned": true, "seed": 42,
              "time_windows": "SOFT" },                                    // HARD | SOFT
  "depots":   [{ "id": "...", "lat": 0, "lon": 0 }],
  "vehicles": [{ "id": "...", "driver_id": "...", "start_depot_id": "...", "end_depot_id": "...",
                 "cap_weight_g": 0, "cap_volume_cm3": 0, "cost_per_km": 0, "fixed_cost": 0,
                 "shift_start_s": 0, "shift_end_s": 0, "max_overtime_s": 0, "break_s": 0 }],
  "orders":   [{ "id": "...", "depot_id": "...", "lat": 0, "lon": 0, "weight_g": 0, "volume_cm3": 0,
                 "tw_start_s": 0, "tw_end_s": 0, "service_s": 0, "priority": 1 }],
  "dynamic":  null   // REOPTIMIZE: { "vehicle_states": [...], "frozen_order_ids": [...], "previous_routes": [...] }
}

// SolveResult
{
  "contract_version": "1.0", "job_id": "...", "status": "FEASIBLE",       // FEASIBLE | INFEASIBLE | TIME_LIMIT | FAILED
  "proven_optimal": false,
  "objective": { "total": 0, "travel": 0, "lateness": 0, "fleet": 0, "overtime": 0, "unserved": 0, "stability": 0 },
  "kpis": { "distance_m": 0, "duration_s": 0, "vehicles_used": 0, "late_stops": 0 },
  "routes": [{ "vehicle_id": "...", "driver_id": "...", "distance_m": 0, "duration_s": 0,
               "stops": [{ "order_id": "...", "seq": 1, "arrival_s": 0, "service_start_s": 0, "departure_s": 0, "late_s": 0 }] }],
  "unassigned": [{ "order_id": "...", "reason": "NOT_ROUTED" }],                  // or NO_ELIGIBLE_VEHICLE
  "validation": { "passed": true, "violations": [] },
  "meta": { "algorithm": "...", "solver_version": "...", "git_sha": "...", "seed": 42, "iterations": 0,
            "matrix_source": "OSRM", "matrix_key": "...", "execution_ms": 0, "recommend_publish": null }
}
```

## 5. Optimization model → implementation

The problem is HCVRPTW, delivery-only, with multiple depots. Objective: `Z = travel + lateness + fleet + overtime + unserved + stability` (spec §9.3).

| Spec element | OR-Tools mapping |
|---|---|
| Heterogeneous cost per km | `SetArcCostEvaluatorOfVehicle` (one evaluator per distinct cost rate) |
| Fixed cost f_k | `SetFixedCostOfVehicle` |
| Weight and volume capacity (hard) | Two `AddDimensionWithVehicleCapacity` dimensions |
| Time windows (configurable) | Time dimension: HARD → `CumulVar.SetRange`; SOFT → `SetCumulVarSoftUpperBound(p_i)` |
| Shift + overtime | Vehicle end cumul: soft upper bound at `shift_end` (overtime cost), hard limit at `shift_end + max_overtime` |
| Unserved orders | `AddDisjunction([node], M_i)` |
| Order ↔ depot compatibility | `SetAllowedVehiclesForIndex` (vehicles of the order's depot) |
| Initial solution (Clarke-Wright) | `ReadAssignmentFromRoutes` → `SolveFromAssignmentWithParameters` |
| Guided local search | `local_search_metaheuristic = GUIDED_LOCAL_SEARCH`, `time_limit` |
| Stability (λ₁ reassigned, λ₂ reordered) | Not native: warm start from the previous plan, then score with the shared evaluator; custom local search optimizes full Z |

**Decision: shared evaluator.** `validation/evaluate.py` computes the cost breakdown from (problem, matrix, routes) for **every** algorithm.
This is the only valid basis for comparison (spec §10.5). The validator (`validation/validate.py`) checks hard constraints from scratch and imports nothing from `solvers/`.

**Decision: driver ↔ vehicle pairing happens before solving.** The model indexes driver-hour limits by vehicle (H_k), and a fleet can have more vehicles than drivers (10 vs 5 in the spec's example).
Go therefore sends one *resource* per (vehicle, driver) pair for the day, and the solver never assigns drivers. Automatic pairing is deferred.

**Decision: breaks.** In V1, `break_s` is subtracted from the usable shift. Proper break placement (`SetBreakIntervalsOfVehicle`) comes later.

**Spec conflict:** §9.5 calls the driver shift *hard*, while §9.3 charges overtime. Resolution: overtime within `max_overtime` is allowed and charged; anything beyond it is hard-infeasible.

## 6. Data model

Use the four Postgres schemas and tables from spec §8. Changes and additions:

- **`core.depots`**: the spec lists depots under `core` in the schema table but writes `operations.depots` in the table definitions. Pick `core`.
- **`operations.orders`**: add `planning_date DATE` and an optional `batch_id`, so jobs can select orders by date or batch instead of by ID list (spec §12.7). Index on `(organization_id, planning_date, status)`.
- **`optimization.jobs`**: add `kind`, `planning_date`, `idempotency_key` (unique per org), `result JSONB`, `cancel_requested BOOL`, `attempts INT`, `parent_route_plan_id`.
- **`core.outbox`**: `(id, topic, payload JSONB, created_at, published_at)`.
- **`routing.order_assignments`** (`order_id PRIMARY KEY, route_id, route_plan_id`) enforces "one active route per order". Activation deletes and reinserts the plan's rows in one transaction, and the PK rejects duplicates.
- Published plans are immutable. Any change creates a new `route_plans.version`.
- Max overtime is per driver (`drivers.max_overtime_minutes`); the overtime *cost* is an objective-profile weight.
- **Tenant isolation is enforced by the database.** Every tenant table has `UNIQUE (organization_id, id)`, and children reference parents through composite foreign keys `(organization_id, parent_id)`, so a cross-organization reference is impossible even with a buggy query. Every query is also org-scoped.
- Other DB-enforced invariants: one ACTIVE plan per org and day (partial unique index), one plan per optimization job, no double-booked driver or vehicle per day, CHECK constraints on statuses, capacities and windows.
- `routing.route_plans` also stores `algorithm`, the `objective` cost breakdown and `unassigned_order_ids` (migration 00003), so plans can be compared without the job's raw result.

## 7. Job reliability

- Queue: Redis Streams with consumer group `optimizers`. Leases use pending entries plus `XAUTOCLAIM` after `time_limit + 60s`.
- Retries: matrix/OSRM timeouts and worker crashes are retryable (max 3 attempts). Invalid input and infeasibility are final.
  After 3 attempts the job becomes `FAILED` and is copied to `opti:jobs:dlq`.
- Cancellation: Go sets `cancel_requested`. The worker checks it between stages and inside the OR-Tools solution callback.
- Every result stores the snapshot, the config, the seed, `solver_version` and `git_sha`, so any run can be reproduced.

## 8. Matrices and routing

- OSRM `/table?annotations=distance,duration`. Raise `--max-table-size` (the default of 100 is too small) and still chunk requests at about 500×500.
- Deduplicate coordinates (rounded to 5 decimal places). Cache key: `hash(sorted coords, osrm_dataset_version, profile)`. The cache lives in Redis with a TTL, plus `optimization.matrix_cache` once it is needed.
- `HaversineMatrixProvider` (straight-line distance × detour factor, fixed speed) is used for unit tests and Solomon instances.
- Traffic: a global or time-band multiplier on durations. Time-dependent `t_ij(τ)` is deferred.

## 8a. Persistence tooling

**No ORM.** Schema changes are plain SQL **goose** migrations in `services/backend/migrations/` (`-- +goose Up/Down`).
The Go service runs them from Phase 3; until then `make db-migrate` applies the Up sections.
Go queries use **sqlc** (SQL in, typed Go out) on **pgx**. The Python worker uses **psycopg** for its few `optimization.jobs` statements.

Reasons:
- The critical logic is SQL-shaped: conditional claims (`UPDATE … WHERE status = ANY(…) RETURNING`), partial indexes, invariant constraints and PostGIS `GEOGRAPHY` columns.
- The same schema is shared by Go and Python.
- Every schema change is a reviewed SQL diff, with no auto-migrate drift.
- If an ORM is ever wanted, the Go-native option is ent with Atlas migrations. Decide before Phase 3.

## 8b. Go backend (Phase 3)

Modules in `services/backend/internal/`: `identity`, `geography`, `fleet`, `orders`, `planning`, `dispatch`, with shared code in `platform/` (auth, httpx, database, outbox, testdb).
Each module owns its `queries.sql` and its sqlc-generated `store` package.

- **API conventions:**
  - Errors use one envelope, `{"error": {"code", "message", "details"}}`.
  - Validation failures return 422 with details per field (per row for CSV).
  - Another organization's resource is a 404, indistinguishable from a missing one.
  - Lists take `limit` (max 500) and `offset`.
  - Writes need ADMIN or DISPATCHER; reads any role.
- **Auth (decided: in-house JWT):**
  - Access tokens are 15-minute HS256 JWTs carrying user, org and role.
  - Refresh tokens are opaque and stored only as SHA-256 hashes. They rotate on every use, and replaying a rotated token revokes its whole family.
  - Passwords use bcrypt, cost 12.
- **Order lifecycle:** `PENDING → PLANNED` (on activation) `→ DISPATCHED → DELIVERED | FAILED` (tracking, Phase 5). `CANCELLED` is only reachable from `PENDING`, and only `PENDING` orders are editable. A `PLANNED` order is a stop on the active plan, so changing it means re-optimizing.
- **Planning reads across modules (deliberate exception to spec §6.1):** the snapshot builder reads orders, vehicles, drivers and depots directly (read-only) in one `REPEATABLE READ` transaction. A snapshot needs one consistent view of the day, which separate interface calls cannot give. All writes stay inside their owning module.
- **Jobs:**
  - `POST /optimization/jobs` writes the job and an outbox row in one transaction. `platform/outbox.Relay`, running in `api serve`, publishes the row to `opti:jobs` (`FOR UPDATE SKIP LOCKED`, at-least-once).
  - An `Idempotency-Key` makes retries return the original job.
  - Validation errors are specific: `NO_VEHICLES`, `NO_ORDERS`, `INVALID_SHIFT`, `WINDOW_BEFORE_PLANNING_DATE`.
- **Plans:**
  - `dispatch.Consumer` (also in `api serve`) turns `COMPLETED` job events into DRAFT plans, idempotently.
  - Malformed results, constraint violations, and events still failing after 5 deliveries go to `opti:job-events:dlq`, so one bad event can never block the stream.
  - **Drafts never change order state:** several drafts can compete for one day.
- **Activation:**
  - Locking: `DRAFT → APPROVED → ACTIVE` in one transaction, under a per-org/day advisory lock.
  - 409 `STALE_PLAN` if any order changed since the solve.
  - An existing ACTIVE plan is replaced only with `{"replace_active": true}`, and never once its routes have started (that is Phase 5 re-optimization). Orders the new plan drops go back to `PENDING` and are listed in the response.
  - On success: `order_assignments` rows are swapped, orders become `PLANNED`, and an `opti:plan-events` outbox event is written.
- **Testing:** each Go test package gets its own throwaway migrated PostgreSQL database (`platform/testdb`). Tests run with `-race`, and CI runs them against Postgres and Redis service containers. `sqlc diff` in CI catches stale generated code, and a Go test validates every job snapshot against the optimizer's JSON Schema.

## 8c. Dispatcher dashboard (Phase 4)

`apps/dashboard`: Vite, React 19, TypeScript, React Router, TanStack Query, Tailwind, MapLibre GL. All versions are pinned.

- **Same origin:** the dev server proxies `/api` to the Go API, so there is no CORS. In production the API and the static build go behind one origin.
- **Tokens:**
  - The access token lives in memory; the refresh token in `sessionStorage`, so it survives a reload but not closing the tab.
  - A 401 triggers one refresh and a retry. Concurrent requests share that refresh, because two parallel refreshes would replay a rotated token and trip reuse detection.
  - Follow-up: an HttpOnly refresh cookie set by the API would keep the token out of JavaScript entirely.
- **Times:** all times are shown in the organization's timezone, not the browser's.
- **Live job status:** by polling, only while a job is running. SSE is deferred.
- **Map:**
  - Light OpenFreeMap basemap, with no key; override with `VITE_MAP_STYLE_URL`.
  - Road lines come from `GET /route-plans/{id}/geometry`.
  - MapLibre's web worker is loaded via `setWorkerUrl` as a bundled asset: Vite's pre-bundling otherwise drops it, and the map stays blank.
  - The map records what it actually drew on `idle` (`data-rendered-routes` / `data-rendered-stops`), and the browser tests assert on that.
- **Route colours** follow the dataviz method:
  - The validated 8-hue categorical palette is assigned in fixed order by vehicle registration.
  - The validator shows that no 8-hue palette separates all pairs for colour-vision deficiency on a map (worst ΔE 3.2), so colour is never the only cue. Routes 9–16 are dashed and 17+ are neutral. A legend list, selection highlighting, numbered stops and tooltips carry identity.
  - With 3 or fewer routes, the hues validate even for all pairs.
- **Planned stop times are service starts** (`max(arrival, service_start)`): a van early for a window waits before reaching the customer, so no ETA precedes its window.
- **Browser tests:** `make dashboard-e2e` locally. CI job `e2e` runs the real API, worker, Postgres and Redis without OSRM (haversine matrices, straight-line geometry), which exercises the degraded modes.
  - Locally, restart `make dashboard` after switching git branches. A long-running Vite dev server can miss files that git replaced by rename, and Playwright reuses it.
- **Follow-ups:**
  - Optimizer: price idle time too (e.g. a span cost), because the optimized plan currently trades waiting for distance (route time +44% in the M4 comparison).
  - SSE for job status.
  - HttpOnly refresh cookie.
  - Driver PWA (Phase 5).

## 9. Out of scope for V1

Pickup-and-delivery, live traffic, Kafka, Kubernetes, the driver app ecosystem, enterprise integrations, LLM explanations, and automatic driver-vehicle pairing.

## 10. Open questions

1. ~~**Region**~~: decided as **Odisha** (clipped from the Geofabrik eastern zone; timezone `Asia/Kolkata`). Other regions: set `REGION`, `ZONE_URL` and `BBOX` for `build.sh`.
2. ~~**Auth**~~: decided as **in-house JWT** (§8b). An external IdP could later be added behind the same `auth.Verifier` interface.
3. **Default time-window mode** for pilot customers: hard or soft?
4. **Real pilot data:** the Kaggle notebooks in this folder lack coordinates and time windows, so they can't serve as routing instances. Use Solomon/CVRPLIB plus a synthetic regional generator until a pilot operator shares anonymized data.
