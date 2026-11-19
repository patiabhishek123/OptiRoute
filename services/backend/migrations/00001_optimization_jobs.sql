-- Optimization job table shared by the Go API (writer of new jobs) and the Python worker
-- (writer of solver fields only). See docs/ARCHITECTURE.md §2 and §6.
-- Foreign keys to core.organizations / routing.route_plans are added with those tables in Phase 3.

-- +goose Up
CREATE SCHEMA IF NOT EXISTS optimization;

CREATE TABLE optimization.jobs (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id       UUID NOT NULL,
    kind                  VARCHAR(16) NOT NULL DEFAULT 'SOLVE'
                          CHECK (kind IN ('SOLVE', 'REOPTIMIZE')),
    status                VARCHAR(16) NOT NULL DEFAULT 'QUEUED'
                          CHECK (status IN ('QUEUED', 'PREPROCESSING', 'SOLVING', 'VALIDATING',
                                            'COMPLETED', 'FAILED', 'CANCELLED', 'INFEASIBLE',
                                            'TIME_LIMIT')),
    planning_date         DATE,
    problem_version       INTEGER NOT NULL DEFAULT 1,
    idempotency_key       VARCHAR(128),
    input_snapshot        JSONB NOT NULL,
    solver_config         JSONB,
    solver_name           VARCHAR(64),
    solver_version        VARCHAR(32),
    objective_value       NUMERIC,
    total_distance_km     NUMERIC,
    execution_time_ms     INTEGER,
    result                JSONB,
    error_message         TEXT,
    cancel_requested      BOOLEAN NOT NULL DEFAULT FALSE,
    attempts              INTEGER NOT NULL DEFAULT 0,
    parent_route_plan_id  UUID,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at            TIMESTAMPTZ,
    completed_at          TIMESTAMPTZ,
    UNIQUE (organization_id, idempotency_key)
);

CREATE INDEX jobs_org_created_idx ON optimization.jobs (organization_id, created_at DESC);
CREATE INDEX jobs_active_idx ON optimization.jobs (status)
    WHERE status IN ('QUEUED', 'PREPROCESSING', 'SOLVING', 'VALIDATING');

-- +goose Down
DROP TABLE optimization.jobs;
DROP SCHEMA optimization;
