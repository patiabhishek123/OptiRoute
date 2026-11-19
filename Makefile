OPTIMIZER := services/optimizer

.PHONY: help setup lint format test up down logs

help:  ## List targets
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-8s %s\n", $$1, $$2}'

setup:  ## Install optimizer dependencies
	cd $(OPTIMIZER) && uv sync

lint:  ## Lint and format-check
	cd $(OPTIMIZER) && uv run ruff check . && uv run ruff format --check .

format:  ## Auto-format
	cd $(OPTIMIZER) && uv run ruff check --fix . && uv run ruff format .

test:  ## Run all tests
	cd $(OPTIMIZER) && uv run pytest

up:  ## Start Postgres+PostGIS and Redis
	docker compose up -d --wait

down:  ## Stop local infrastructure
	docker compose down

logs:  ## Tail infrastructure logs
	docker compose logs -f
