OPTIMIZER := services/optimizer

.PHONY: help setup lint format test schemas fetch-solomon bench db-migrate worker up down logs

help:  ## List targets
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-14s %s\n", $$1, $$2}'

setup:  ## Install optimizer dependencies
	cd $(OPTIMIZER) && uv sync

lint:  ## Lint and format-check
	cd $(OPTIMIZER) && uv run ruff check . && uv run ruff format --check .

format:  ## Auto-format
	cd $(OPTIMIZER) && uv run ruff check --fix . && uv run ruff format .

test:  ## Run all tests
	cd $(OPTIMIZER) && uv run pytest

schemas:  ## Regenerate optimizer contract JSON Schemas
	cd $(OPTIMIZER) && uv run optiroute schema

fetch-solomon:  ## Download + verify the 56 Solomon instances into services/optimizer/data
	cd $(OPTIMIZER) && uv run optiroute fetch-solomon

ALGOS ?= S0
TIME_LIMIT ?= 15
bench:  ## Run Solomon benchmarks (ALGOS="S0 S1 S2" TIME_LIMIT=10), CSV into experiments/reports
	cd $(OPTIMIZER) && uv run optiroute bench $(foreach a,$(ALGOS),-a $(a)) --time-limit $(TIME_LIMIT) \
		--csv ../../experiments/reports/solomon-$(shell date +%Y%m%d-%H%M%S).csv

db-migrate:  ## Apply backend migrations (goose Up sections) to the dev database
	for f in services/backend/migrations/*.sql; do \
		sed -n '/+goose Up/,/+goose Down/p' $$f | docker compose exec -T db \
			psql -q -v ON_ERROR_STOP=1 -U optiroute -d optiroute || exit 1; done

worker:  ## Run the optimizer worker against local Postgres/Redis (and OSRM if up)
	cd $(OPTIMIZER) && set -a && . ../../.env.example && set +a && \
		OSRM_DATASET_VERSION_FILE=../routing/osrm/data/DATASET_VERSION uv run optiroute worker

up:  ## Start Postgres+PostGIS and Redis
	docker compose up -d --wait

down:  ## Stop local infrastructure
	docker compose down

logs:  ## Tail infrastructure logs
	docker compose logs -f
