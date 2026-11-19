# OptiRoute Build Plan

This plan assumes about 12 weeks at 20–25 h/week, solo (spec §17). The order is **engine first, product second**: each phase ends in something runnable and tested.
A phase is done when its exit criteria pass, not when the code compiles.

Design references: [ARCHITECTURE.md](ARCHITECTURE.md). Full spec: `OptiRoute_Technical_Architecture_Implementation_Plan.docx`.

---

## Phase 0: Repository setup (2–3 days)

- [ ] `git init`, monorepo layout from the README, `.gitignore`, `.editorconfig`
- [ ] `services/optimizer`: Python 3.12 with `uv`, `ruff`, `pytest`, `ortools`, `pydantic`
- [ ] `docker-compose.yml` with Postgres+PostGIS and Redis (OSRM is added in Phase 2)
- [ ] GitHub Actions: lint and test for Python (Go and TS jobs added in their phases)
- [ ] Move the spec, diagrams and notebooks into `docs/spec/` and `experiments/notebooks/`

**Exit:** `make test` passes in CI on an empty test suite.

## Phase 1: Optimization engine (weeks 1–2) → M1

All work is in `services/optimizer/`. No HTTP, no database.

- [ ] `contracts/`: pydantic `SolveRequest` / `SolveResult` (ARCHITECTURE §4); export JSON Schema to `packages/api-contracts/optimizer/v1/`
- [ ] `matrices/`: `MatrixProvider` interface + `HaversineMatrixProvider`
- [ ] `validation/evaluate.py`: shared cost breakdown (travel, lateness, fleet, overtime, unserved)
- [ ] `validation/validate.py`: independent hard-constraint checker. Write tests for **every** row of spec §18.4 (duplicate assignment, capacity, invalid vehicle, hard TW, shift exceeded, disconnected route, invalid depot sequence)
- [ ] `solvers/baseline/`: nearest neighbour (S0)
- [ ] `solvers/clarke_wright/`: savings with capacity and TW feasibility checks (S1)
- [ ] `solvers/ortools/`: full mapping from ARCHITECTURE §5, CW warm start, GLS, time limit, seed (S2)
- [ ] `benchmarks/`: Solomon loader (C1, R1, RC1 sets) + a runner that writes CSV (instance, algorithm, seed, Z, distance, vehicles, late, feasible, ms)
- [ ] CLI: `optiroute solve problem.json --algorithm S2 --time-limit 15`

**Exit (M1):** S0–S2 return validator-clean solutions on all Solomon 100-customer C1/R1/RC1 instances. Benchmark runs are reproducible with a fixed seed. Gap to best-known solutions is reported.

## Phase 2: Road network + optimizer service (weeks 3–4) → M2

- [ ] `services/routing/osrm/`: script to download the regional extract and run `osrm-extract/partition/customize` (MLD); add the OSRM service to compose with a raised `--max-table-size`
- [ ] `OsrmMatrixProvider`: deduplication, chunking, retries/timeouts, Redis cache (ARCHITECTURE §8)
- [ ] Synthetic regional instance generator (depots + orders inside the extract's bounding box, realistic windows and demands), seeded
- [ ] Worker: Redis Streams consumer, job state machine, conditional status transitions, cancellation checks, retry/DLQ rules (ARCHITECTURE §7)
- [ ] Minimal FastAPI app: `GET /health`, `POST /validate`
- [ ] Result metadata: solver version, git SHA, matrix source and key, timings

**Exit (M2):** a job inserted by hand into Postgres plus a stream message produces a validated result using real OSRM distances. 500 orders on the regional extract solve within the time limit. Killing the worker mid-job leads to reclaim and completion.

## Phase 3: Go backend (weeks 5–6) → M3

- [ ] `services/backend`: `cmd/api`, config from env, structured logging (`slog`), goose migrations, sqlc
- [ ] Migrations: schemas and tables from spec §8 plus the additions in ARCHITECTURE §6
- [ ] `identity`: orgs, users, login/refresh (JWT), org-scoped middleware
- [ ] `geography` / `fleet`: depots, vehicles, drivers, daily vehicle↔driver pairing
- [ ] `orders`: CRUD, CSV bulk import (validate and report per row), cancel
- [ ] `planning`: snapshot builder (unit conversion happens here), job create/get/cancel, idempotency keys, outbox relay, job-event consumer → plan materialization
- [ ] `dispatch`: get plans, approve/reject/activate (`order_assignments` swap, version check)
- [ ] Integration tests with testcontainers: Go→Postgres, Go→Redis, full job round-trip with a real worker

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
