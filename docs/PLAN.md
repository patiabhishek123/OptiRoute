# OptiRoute Build Plan

This plan assumes about 12 weeks at 20–25 h/week, solo (spec §17). The order is **engine first, product second**: each phase ends in something runnable and tested.
A phase is done when its exit criteria pass, not when the code compiles.

Design references: [ARCHITECTURE.md](ARCHITECTURE.md). Full spec: `OptiRoute_Technical_Architecture_Implementation_Plan.docx`.

---

## Phase 0: Repository setup (2–3 days)

- [x] `git init`, monorepo layout from the README, `.gitignore`, `.editorconfig`
- [x] `services/optimizer`: Python 3.12 with `uv`, `ruff`, `pytest`, `ortools`, `pydantic`
- [x] `docker-compose.yml` with Postgres+PostGIS and Redis (OSRM is added in Phase 2), host ports 15432/16379
- [x] GitHub Actions: lint and test for Python (Go and TS jobs added in their phases)
- [x] Move the spec, diagrams and notebooks into `docs/spec/` and `experiments/notebooks/`

**Exit:** `make lint test` passes in CI (smoke tests: package import, CLI, OR-Tools solves a 4-node TSP).

## Phase 1: Optimization engine (weeks 1–2) → M1 ✅

All work is in `services/optimizer/`. No HTTP, no database.

- [x] `contracts/`: pydantic `SolveRequest` / `SolveResult` (ARCHITECTURE §4); export JSON Schema to `packages/api-contracts/optimizer/v1/`
- [x] `matrices/`: `MatrixProvider` interface + `HaversineMatrixProvider`
- [x] `validation/evaluate.py`: shared cost breakdown (travel, lateness, fleet, overtime, unserved)
- [x] `validation/validate.py`: independent hard-constraint checker. Write tests for **every** row of spec §18.4 (duplicate assignment, capacity, invalid vehicle, hard TW, shift exceeded, disconnected route, invalid depot sequence). Completed-stop reassignment waits for Phase 5
- [x] `solvers/baseline.py`: Solomon (1987) time-oriented nearest neighbour (S0); distance-only NN stranded customers on R1/RC1
- [x] `solvers/clarke_wright.py`: directional savings with O(1) merge checks for time windows and capacity, multi-depot clustering, real-vehicle assignment, insertion repair (S1). 56/56 feasible, median gap 20.0%
- [x] `solvers/ortools_gls.py`: full mapping from ARCHITECTURE §5, CW warm start, GLS, time limit (S2). OR-Tools has no seed, so time-limited runs are only reproducible on the same machine
- [x] `benchmarks/`: Solomon loader for all 56 instances, using DIMACS distances so the published C101 best-known solution validates and scores exactly. Checksum-pinned download (`make fetch-solomon`), best-known distances in `solomon_manifest.csv`, and a runner that writes CSV with the gap (`make bench`)
- [x] CLI: `optiroute solve | bench | fetch-solomon | schema`

**Baseline (S0, `experiments/reports/solomon-20261119-194312.csv`):** 56/56 feasible; gap to best-known distance is 0.00% best, 69.2% median, 69.7% mean.

**M1 met (2026-11-19):** S0/S1/S2 all 56/56 validator-clean. Median gap: S0 69.2%, S1 20.0%, S2 2.1% (10 s). See `experiments/reports/m1-solomon.md`.

**Exit (M1):** S0–S2 return validator-clean solutions on all Solomon 100-customer C1/R1/RC1 instances. Benchmark runs are reproducible with a fixed seed. Gap to best-known solutions is reported.

## Phase 2: Road network + optimizer service (weeks 3–4) → M2 ✅

- [x] `services/routing/osrm/build.sh` (`make osrm-build`): Odisha, clipped from the Geofabrik eastern zone. Memory/CPU-capped Docker steps, 58 s, peak 1.2 GB. OSRM compose service (`make osrm-up`, `--max-table-size 1000`, idles at ~300 MB)
- [x] `OsrmMatrixProvider`: deduplication, chunking, retries/timeouts, Redis cache (ARCHITECTURE §8)
- [x] Synthetic regional instance generator (`optiroute generate`): inland Odisha hubs, realistic windows and demands, van/truck fleet, seeded
- [x] Worker (`optiroute worker`): Redis Streams consumer, job state machine, conditional status transitions, cancellation checks, retry/DLQ rules, lease heartbeat (ARCHITECTURE §7). `optiroute submit` stands in for the Go API
- [x] Minimal FastAPI app (`optiroute serve`): `GET /internal/v1/health`, `POST /internal/v1/validate`
- [x] Result metadata: solver version, git SHA, matrix source and key, timings

**M2 met (2026-11-19).** On a synthetic Odisha instance (500 orders, 20 vehicles, 2 depots):
- the 502×502 OSRM matrix took 2.4 s (51 ms on a cache hit); road distance is 1.46× straight-line
- S2 within a 10 s limit: feasible, 2,740 km, every order served, about 4.5% cheaper than S1
- a worker SIGKILLed mid-solve was reclaimed after the lease and completed (attempts = 2)

**Exit (M2):** a job inserted by hand into Postgres plus a stream message produces a validated result using real OSRM distances. 500 orders on the regional extract solve within the time limit. Killing the worker mid-job leads to reclaim and completion.

## Phase 3: Go backend (weeks 5–6) → M3 ✅

- [x] `services/backend`: `cmd/api` (`serve` and `migrate`), env config, `slog` JSON logs, goose migrations embedded in the binary, sqlc. Go 1.25 with pinned dependencies
- [x] Migrations 00001–00003: spec §8 tables plus the ARCHITECTURE §6 additions. Tenant isolation and other invariants are enforced by the database
- [x] `identity`: signup, login, rotating refresh tokens with reuse detection, logout, users (ADMIN), org-scoped middleware
- [x] `geography` / `fleet`: depots (PostGIS), vehicles, drivers, transactional daily vehicle↔driver pairing
- [x] `orders`: CRUD, all-or-nothing CSV import with errors per row (1,000 rows in ~100 ms), PENDING-only edit/cancel
- [x] `planning`: snapshot builder (REPEATABLE READ, unit conversion), jobs with `Idempotency-Key`, outbox relay, status/result/cancel
- [x] Job-event consumer → DRAFT plan materialization (idempotent, poison events dead-lettered)
- [x] `dispatch`: plan reads, approve/reject/activate (`order_assignments` swap, stale-plan check, explicit `replace_active`)
- [x] Integration tests against real Postgres/Redis: each test package gets its own throwaway migrated database instead of testcontainers (same isolation, no Docker-in-Docker in CI). All Go tests run with `-race`

**M3 met (2026-11-19).** `make e2e` (API + worker + OSRM running) goes through the public API: signup → depot → 6 paired vans → CSV import of 100 orders → job (idempotent retry returns the same job) → worker on OSRM → DRAFT plan v1 (4 routes, 100 stops, 347 km, 0 late, 0 unassigned) → approve → activate (100 orders PLANNED, one ACTIVE plan) → a new job answers 422 NO_ORDERS.
Two bugs were found and fixed on the way: idle-at-first-stop schedules, and a poison event that could block the plan consumer.

**Exit (M3):** create org → depots → vehicles → import 100 orders via CSV → `POST /optimization/jobs` → plan available via the API. Duplicate requests with the same idempotency key create one job.

## Phase 4: Dashboard + dispatch (weeks 7–8) → M4 (MVP)

- [ ] Vite + React + TS app; auth; org-scoped API client (TanStack Query)
- [ ] Order list + CSV upload with error report; fleet and driver pages
- [ ] Run optimization (choose algorithm and time limit), live job status over SSE
- [ ] MapLibre map: depots, orders, routes coloured by vehicle (OSRM `/route` geometry)
- [ ] Plan comparison: baseline (S0) vs optimized, showing cost breakdown, distance, vehicles, late and unassigned orders
- [ ] Approve → activate; CSV/PDF route export per driver

**Exit (M4):** the full spec §22 MVP checklist works end to end with 5–10 vehicles and 50–100 orders. Playwright covers the happy path.

## Phase 5: Dynamic routing (weeks 9–10) → M5

- [ ] Tracking: `POST /vehicles/:id/location`, stop arrive/complete, route progress
- [ ] GPS simulator that drives the active plan along OSRM geometry at a configurable speed
- [ ] `kind=REOPTIMIZE` snapshot: vehicle states, frozen stops, previous routes
- [ ] Optimizer: virtual start nodes, stability cost (λ₁, λ₂) in the evaluator, warm start from the previous plan, `recommend_publish` threshold
- [ ] Triggers: new-order insertion, breakdown (`/routes/:id/disruptions`), cooldown, version check
- [ ] Dashboard: diff view (current vs proposed, with changed stops highlighted) → approve

**Exit (M5):** in simulation, a mid-day breakdown redistributes its remaining orders and a 10% order spike gets inserted. Completed stops never move, and nothing activates without approval.

## Phase 6: Research + production readiness (weeks 11–12) → M6

- [ ] `solvers/local_search/`: 2-opt, Or-opt, relocate, swap, cross-exchange on top of the OR-Tools result (S3)
- [ ] Experiments A–E (spec §19) with multiple seeds; report best, median and mean in `experiments/reports/`
- [ ] Prometheus metrics (API latency, queue depth, solve time, success rate, unassigned count) + one Grafana dashboard
- [ ] Failure tests: OSRM down, worker crash, Postgres restart, duplicate events
- [ ] Deploy the pilot: ECS/Fargate (api, worker), RDS, ElastiCache, OSRM container, S3 + CloudFront for the web app

**Exit (M6):** published benchmark report, a dashboard showing the optimization success rate, and a pilot environment reachable over TLS.

---

## Deferred (after M6)

S4 Tabu Search and S5 hybrid stability solver, driver PWA, break placement, time-dependent travel times, sparse matrices for more than 1,000 nodes, Kafka, automatic driver pairing, explainability.

## Top risks

| Risk | Mitigation |
|---|---|
| OSRM matrix for 1,000 nodes is slow or memory-heavy | Benchmark in Phase 2; cache aggressively; partition by depot |
| OR-Tools with soft TWs + two capacities + multi-depot struggles at 1,000 orders in 15 s | Measure in Experiment B early (end of Phase 2); allow longer time limits for daily plans |
| Scope creep in the backend/UI delays dynamic routing | Keep the Phase 3–4 surface to the spec §22 checklist; no extra CRUD |
| No real customer data | Synthetic generator now; seek one pilot operator for anonymized data before claiming savings |

## Start here

1. Phase 0 setup.
2. Write the `SolveRequest`/`SolveResult` models and the validator **before** any solver.
3. Nearest neighbour + evaluator + Solomon loader, so the benchmark loop runs end to end on day 3–4.
