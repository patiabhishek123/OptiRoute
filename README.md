# OptiRoute

Cost-aware vehicle-routing and dispatch platform for regional logistics operators (5–50 vehicles, 500–1,000 orders/day).
It solves a heterogeneous capacitated VRP with time windows (HCVRPTW) using heuristics + Google OR-Tools + custom local search,
and supports dispatcher approval and dynamic re-optimization (new orders, breakdowns).

**Status:** pre-implementation. Start with Phase 1 in [docs/PLAN.md](docs/PLAN.md).

## Documents

| Doc | Purpose |
|---|---|
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Components, data flow, optimizer contract, and decisions that resolve gaps in the spec |
| [docs/PLAN.md](docs/PLAN.md) | Phased build plan with tasks and exit criteria (milestones M1–M6) |
| `OptiRoute_Technical_Architecture_Implementation_Plan.docx` | Full original specification (source of truth for scope) |
| `OR-Tools Dispatch Optimization Platform-*.pdf` | Component diagram |
| `Route Planning Data Model-*.pdf` | ER diagram |

## Stack

| Layer | Choice |
|---|---|
| Optimizer | Python 3.12, OR-Tools, pydantic, pytest |
| Backend | Go modular monolith, `net/http`, pgx + sqlc, goose migrations |
| Data | PostgreSQL 16 + PostGIS, Redis (Streams) |
| Routing | OSRM on an OpenStreetMap regional extract |
| Frontend | React + TypeScript (Vite), MapLibre GL, TanStack Query |
| Ops | Docker Compose (dev), GitHub Actions, Prometheus + Grafana; AWS ECS/Fargate later |

## Target repository layout

```
apps/dashboard/              React dispatcher dashboard (driver PWA added later)
services/optimizer/          Python: contracts, matrices, solvers, validation, worker, benchmarks
services/backend/            Go: cmd/api, internal/<module>, migrations
services/routing/osrm/       OSRM build scripts and extract config
packages/api-contracts/      JSON Schemas for the optimizer contract (shared by Go and Python)
infrastructure/              docker, monitoring, deployment
experiments/                 notebooks and benchmark reports
docker-compose.yml
```
