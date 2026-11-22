OPTIMIZER := services/optimizer
BACKEND := services/backend
DASHBOARD := apps/dashboard
DEV_ENV := set -a && . $(CURDIR)/.env.example && set +a

.PHONY: help setup lint format test schemas fetch-solomon bench osrm-build osrm-up db-migrate worker sqlc api dashboard dashboard-e2e e2e simulate up down logs

help:  ## List targets
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-14s %s\n", $$1, $$2}'

setup:  ## Install optimizer and dashboard dependencies
	cd $(OPTIMIZER) && uv sync
	cd $(DASHBOARD) && npm ci

lint:  ## Lint and type-check (Python, Go, TypeScript)
	cd $(OPTIMIZER) && uv run ruff check . && uv run ruff format --check .
	cd $(BACKEND) && test -z "$$(gofmt -l .)" && go vet ./...
	cd $(DASHBOARD) && npm run typecheck

format:  ## Auto-format
	cd $(OPTIMIZER) && uv run ruff check --fix . && uv run ruff format .

test:  ## Run all tests (Python, Go, dashboard; integration tests need `make up`)
	cd $(OPTIMIZER) && uv run pytest
	cd $(BACKEND) && go test -race ./...
	cd $(DASHBOARD) && npm test

sqlc:  ## Regenerate Go data-access code from SQL (sqlc)
	cd $(BACKEND) && go tool sqlc generate

api:  ## Run the Go API on :8080 (migrates on start)
	cd $(BACKEND) && $(DEV_ENV) && MIGRATE_ON_START=true go run ./cmd/api serve

schemas:  ## Regenerate optimizer contract JSON Schemas
	cd $(OPTIMIZER) && uv run optiroute schema

fetch-solomon:  ## Download + verify the 56 Solomon instances into services/optimizer/data
	cd $(OPTIMIZER) && uv run optiroute fetch-solomon

ALGOS ?= S0
TIME_LIMIT ?= 15
bench:  ## Run Solomon benchmarks (ALGOS="S0 S1 S2" TIME_LIMIT=10), CSV into experiments/reports
	cd $(OPTIMIZER) && uv run optiroute bench $(foreach a,$(ALGOS),-a $(a)) --time-limit $(TIME_LIMIT) \
		--csv ../../experiments/reports/solomon-$(shell date +%Y%m%d-%H%M%S).csv

osrm-build:  ## Download + clip Odisha and preprocess OSRM (memory/CPU capped)
	services/routing/osrm/build.sh

osrm-up:  ## Start the OSRM router (needs osrm-build)
	docker compose --profile routing up -d --wait osrm

db-migrate:  ## Apply database migrations (goose, embedded in the Go API)
	cd $(BACKEND) && $(DEV_ENV) && go run ./cmd/api migrate

worker:  ## Run the optimizer worker against local Postgres/Redis (and OSRM if up)
	cd $(OPTIMIZER) && set -a && . ../../.env.example && set +a && \
		OSRM_DATASET_VERSION_FILE=../routing/osrm/data/DATASET_VERSION uv run optiroute worker

dashboard:  ## Dashboard dev server on :5173 (proxies /api to :8080)
	cd $(DASHBOARD) && npm run dev

dashboard-e2e:  ## Browser tests (Playwright) against the real API (needs `make api`)
	cd $(DASHBOARD) && npx playwright test

e2e:  ## End-to-end check through the public API (needs `make api` and `make worker` running)
	python3 scripts/e2e_api.py

SPEED_UP ?= 60
simulate:  ## Drive the day's ACTIVE plan via the tracking API (DATE= ORG= EMAIL=, OPTIROUTE_PASSWORD; UNTIL=12:00 SPEED_UP=0)
	cd $(OPTIMIZER) && uv run optiroute simulate --date $(DATE) --org $(ORG) --email $(EMAIL) \
		--speed-up $(SPEED_UP) $(if $(UNTIL),--until $(UNTIL)) $(SIM_ARGS)

up:  ## Start Postgres+PostGIS and Redis
	docker compose up -d --wait

down:  ## Stop local infrastructure (including OSRM)
	docker compose --profile routing down

logs:  ## Tail infrastructure logs
	docker compose logs -f
