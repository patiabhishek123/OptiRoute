# OptiRoute

Cost-aware vehicle-routing and dispatch platform for regional logistics operators (5–50 vehicles, 500–1,000 orders/day).
It solves a heterogeneous capacitated VRP with time windows (HCVRPTW) using heuristics + Google OR-Tools + custom local search,
and supports dispatcher approval and dynamic re-optimization (new orders, breakdowns).

**Status:** Phase 0 (scaffold) done. Next: Phase 1 in [docs/PLAN.md](docs/PLAN.md).

## Quick start

Requires [uv](https://docs.astral.sh/uv/), Docker and make.

```bash
cp .env.example .env   # optional; defaults work without it
make setup             # Python 3.12 venv + optimizer deps
make lint test         # ruff + pytest
make up                # Postgres+PostGIS on :15432, Redis on :16379
make down
```

## Documents

| Doc | Purpose |
|---|---|
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Components, data flow, optimizer contract, and decisions that resolve gaps in the spec |
| [docs/PLAN.md](docs/PLAN.md) | Phased build plan with tasks and exit criteria (milestones M1–M6) |
| [docs/spec/](docs/spec/) | Original specification (`.docx`, source of truth for scope), component diagram and ER diagram |

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

## Git workflow

- `main`: milestone releases only; it is updated from `development` when a milestone passes.
- `development`: the integration branch. Every change lands here.
- Each small, significant change gets its own branch off `development`, named `feature/…`, `fix/…`, `chore/…` or `docs/…`.
  Lint and tests must pass at the branch tip. Merge it with `git merge --no-ff` so the branch stays visible in history.
- CI runs on pushes to `main` and `development`, and on pull requests.
