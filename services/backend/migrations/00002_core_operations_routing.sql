-- Core business schema (spec §8 + docs/ARCHITECTURE.md §6).
--
-- Tenant isolation is enforced by the database, not only by queries: every tenant table has
-- UNIQUE (organization_id, id), and child rows reference their parents through composite foreign
-- keys (organization_id, parent_id). A route can therefore never point at another organization's
-- vehicle, order or plan, even if application code is wrong.

-- +goose Up
CREATE EXTENSION IF NOT EXISTS postgis;
CREATE SCHEMA IF NOT EXISTS core;
CREATE SCHEMA IF NOT EXISTS operations;
CREATE SCHEMA IF NOT EXISTS routing;

-- ---------------------------------------------------------------- core

CREATE TABLE core.organizations (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        VARCHAR(200) NOT NULL,
    slug        VARCHAR(64)  NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9][a-z0-9-]*$'),
    timezone    VARCHAR(64)  NOT NULL DEFAULT 'Asia/Kolkata',
    currency    CHAR(3)      NOT NULL DEFAULT 'INR',
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE TABLE core.users (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id  UUID NOT NULL REFERENCES core.organizations (id) ON DELETE CASCADE,
    email            VARCHAR(320) NOT NULL CHECK (email = lower(email)),
    password_hash    TEXT NOT NULL,
    role             VARCHAR(16) NOT NULL CHECK (role IN ('ADMIN', 'DISPATCHER', 'VIEWER')),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (organization_id, email),
    UNIQUE (organization_id, id)
);

-- Rotating refresh tokens: only a hash is stored; reuse of a rotated token revokes the family.
CREATE TABLE core.refresh_tokens (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID NOT NULL REFERENCES core.users (id) ON DELETE CASCADE,
    family_id    UUID NOT NULL,
    token_hash   BYTEA NOT NULL UNIQUE,
    expires_at   TIMESTAMPTZ NOT NULL,
    revoked_at   TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX refresh_tokens_family_idx ON core.refresh_tokens (family_id);

CREATE TABLE core.depots (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id  UUID NOT NULL REFERENCES core.organizations (id) ON DELETE CASCADE,
    name             VARCHAR(200) NOT NULL,
    location         GEOGRAPHY(POINT, 4326) NOT NULL,
    is_active        BOOLEAN NOT NULL DEFAULT TRUE,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id)
);

-- Transactional outbox: written in the same transaction as the business change, relayed to Redis.
CREATE TABLE core.outbox (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    topic         VARCHAR(128) NOT NULL,
    payload       JSONB NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at  TIMESTAMPTZ
);
CREATE INDEX outbox_unpublished_idx ON core.outbox (id) WHERE published_at IS NULL;

-- ---------------------------------------------------------------- operations

CREATE TABLE operations.vehicles (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id      UUID NOT NULL REFERENCES core.organizations (id) ON DELETE CASCADE,
    depot_id             UUID NOT NULL,
    registration_number  VARCHAR(32) NOT NULL,
    vehicle_type         VARCHAR(32) NOT NULL,
    capacity_weight_kg   NUMERIC(10, 2) NOT NULL CHECK (capacity_weight_kg >= 0),
    capacity_volume_m3   NUMERIC(10, 3) NOT NULL CHECK (capacity_volume_m3 >= 0),
    cost_per_km          NUMERIC(10, 2) NOT NULL CHECK (cost_per_km >= 0),
    fixed_cost           NUMERIC(10, 2) NOT NULL DEFAULT 0 CHECK (fixed_cost >= 0),
    status               VARCHAR(16) NOT NULL DEFAULT 'AVAILABLE'
                         CHECK (status IN ('AVAILABLE', 'UNAVAILABLE', 'MAINTENANCE')),
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (organization_id, registration_number),
    UNIQUE (organization_id, id),
    FOREIGN KEY (organization_id, depot_id) REFERENCES core.depots (organization_id, id)
);

CREATE TABLE operations.drivers (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id         UUID NOT NULL REFERENCES core.organizations (id) ON DELETE CASCADE,
    name                    VARCHAR(200) NOT NULL,
    phone                   VARCHAR(32),
    shift_start             TIME NOT NULL,
    shift_end               TIME NOT NULL CHECK (shift_end > shift_start),
    break_duration_minutes  INTEGER NOT NULL DEFAULT 0 CHECK (break_duration_minutes >= 0),
    max_overtime_minutes    INTEGER NOT NULL DEFAULT 0 CHECK (max_overtime_minutes >= 0),
    status                  VARCHAR(16) NOT NULL DEFAULT 'ACTIVE'
                            CHECK (status IN ('ACTIVE', 'INACTIVE')),
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id)
);

-- Daily vehicle <-> driver pairing (docs/ARCHITECTURE.md §5: the solver sees one resource per pair).
CREATE TABLE operations.driver_assignments (
    organization_id  UUID NOT NULL,
    planning_date    DATE NOT NULL,
    vehicle_id       UUID NOT NULL,
    driver_id        UUID NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (vehicle_id, planning_date),
    UNIQUE (driver_id, planning_date),
    FOREIGN KEY (organization_id, vehicle_id) REFERENCES operations.vehicles (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, driver_id) REFERENCES operations.drivers (organization_id, id) ON DELETE CASCADE
);

CREATE TABLE operations.orders (
    id                        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id           UUID NOT NULL REFERENCES core.organizations (id) ON DELETE CASCADE,
    depot_id                  UUID,  -- NULL: any depot's vehicles may serve it
    external_order_id         VARCHAR(64) NOT NULL,
    planning_date             DATE NOT NULL,
    batch_id                  VARCHAR(64),
    customer_name             VARCHAR(200) NOT NULL,
    customer_phone            VARCHAR(32),
    delivery_location         GEOGRAPHY(POINT, 4326) NOT NULL,
    demand_weight_kg          NUMERIC(10, 2) NOT NULL DEFAULT 0 CHECK (demand_weight_kg >= 0),
    demand_volume_m3          NUMERIC(10, 3) NOT NULL DEFAULT 0 CHECK (demand_volume_m3 >= 0),
    time_window_start         TIMESTAMPTZ NOT NULL,
    time_window_end           TIMESTAMPTZ NOT NULL,
    service_duration_minutes  INTEGER NOT NULL DEFAULT 5 CHECK (service_duration_minutes >= 0),
    priority                  INTEGER NOT NULL DEFAULT 1 CHECK (priority >= 1),
    status                    VARCHAR(16) NOT NULL DEFAULT 'PENDING'
                              CHECK (status IN ('PENDING', 'PLANNED', 'DISPATCHED', 'DELIVERED',
                                                'FAILED', 'CANCELLED')),
    created_at                TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (time_window_end >= time_window_start),
    UNIQUE (organization_id, external_order_id),
    UNIQUE (organization_id, id),
    FOREIGN KEY (organization_id, depot_id) REFERENCES core.depots (organization_id, id)
);
CREATE INDEX orders_planning_idx ON operations.orders (organization_id, planning_date, status);

-- ---------------------------------------------------------------- optimization (tenant FK)

-- NOT VALID: rows written before organizations existed (dev databases) are left alone;
-- every new or updated job must reference a real organization.
ALTER TABLE optimization.jobs
    ADD CONSTRAINT jobs_organization_fk FOREIGN KEY (organization_id)
        REFERENCES core.organizations (id) ON DELETE CASCADE NOT VALID;
ALTER TABLE optimization.jobs ADD CONSTRAINT jobs_org_id_key UNIQUE (organization_id, id);

-- ---------------------------------------------------------------- routing

CREATE TABLE routing.route_plans (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id      UUID NOT NULL REFERENCES core.organizations (id) ON DELETE CASCADE,
    optimization_job_id  UUID NOT NULL,
    planning_date        DATE NOT NULL,
    version              INTEGER NOT NULL CHECK (version >= 1),
    status               VARCHAR(16) NOT NULL DEFAULT 'DRAFT'
                         CHECK (status IN ('DRAFT', 'APPROVED', 'ACTIVE', 'REJECTED', 'SUPERSEDED')),
    total_distance_km    NUMERIC(12, 3) NOT NULL,
    total_cost           NUMERIC(14, 2) NOT NULL,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    approved_by          UUID,
    approved_at          TIMESTAMPTZ,
    activated_at         TIMESTAMPTZ,
    UNIQUE (organization_id, planning_date, version),
    UNIQUE (optimization_job_id),  -- one plan per job: re-delivered job events are idempotent
    UNIQUE (organization_id, id),
    FOREIGN KEY (organization_id, optimization_job_id) REFERENCES optimization.jobs (organization_id, id),
    FOREIGN KEY (organization_id, approved_by) REFERENCES core.users (organization_id, id)
);
-- At most one ACTIVE plan per organization and day.
CREATE UNIQUE INDEX route_plans_one_active_idx
    ON routing.route_plans (organization_id, planning_date) WHERE status = 'ACTIVE';

ALTER TABLE optimization.jobs
    ADD CONSTRAINT jobs_parent_plan_fk FOREIGN KEY (organization_id, parent_route_plan_id)
        REFERENCES routing.route_plans (organization_id, id) NOT VALID;

CREATE TABLE routing.routes (
    id                          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id             UUID NOT NULL,
    route_plan_id               UUID NOT NULL,
    vehicle_id                  UUID NOT NULL,
    driver_id                   UUID,
    status                      VARCHAR(16) NOT NULL DEFAULT 'PLANNED'
                                CHECK (status IN ('PLANNED', 'IN_PROGRESS', 'COMPLETED', 'CANCELLED')),
    total_distance_km           NUMERIC(12, 3) NOT NULL,
    estimated_duration_minutes  INTEGER NOT NULL,
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (route_plan_id, vehicle_id),
    UNIQUE (organization_id, id),
    FOREIGN KEY (organization_id, route_plan_id) REFERENCES routing.route_plans (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, vehicle_id) REFERENCES operations.vehicles (organization_id, id),
    FOREIGN KEY (organization_id, driver_id) REFERENCES operations.drivers (organization_id, id)
);

CREATE TABLE routing.route_stops (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id    UUID NOT NULL,
    route_id           UUID NOT NULL,
    order_id           UUID NOT NULL,
    sequence_number    INTEGER NOT NULL CHECK (sequence_number >= 1),
    planned_arrival    TIMESTAMPTZ NOT NULL,
    planned_departure  TIMESTAMPTZ NOT NULL,
    actual_arrival     TIMESTAMPTZ,
    actual_departure   TIMESTAMPTZ,
    stop_status        VARCHAR(16) NOT NULL DEFAULT 'PENDING'
                       CHECK (stop_status IN ('PENDING', 'ARRIVED', 'COMPLETED', 'FAILED', 'SKIPPED')),
    UNIQUE (route_id, sequence_number),
    UNIQUE (route_id, order_id),
    FOREIGN KEY (organization_id, route_id) REFERENCES routing.routes (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, order_id) REFERENCES operations.orders (organization_id, id)
);

-- Which route currently owns each order: the PRIMARY KEY enforces "an order cannot be assigned to
-- two active routes". Activation swaps a plan's rows in one transaction (ARCHITECTURE §6).
CREATE TABLE routing.order_assignments (
    order_id         UUID PRIMARY KEY,
    organization_id  UUID NOT NULL,
    route_id         UUID NOT NULL,
    route_plan_id    UUID NOT NULL,
    assigned_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (organization_id, order_id) REFERENCES operations.orders (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, route_id) REFERENCES routing.routes (organization_id, id),
    FOREIGN KEY (organization_id, route_plan_id) REFERENCES routing.route_plans (organization_id, id)
);

CREATE TABLE operations.gps_events (
    id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    organization_id  UUID NOT NULL,
    vehicle_id       UUID NOT NULL,
    route_id         UUID,
    location         GEOGRAPHY(POINT, 4326) NOT NULL,
    speed_kmh        NUMERIC(6, 2),
    heading          NUMERIC(5, 2),
    recorded_at      TIMESTAMPTZ NOT NULL,
    FOREIGN KEY (organization_id, vehicle_id) REFERENCES operations.vehicles (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, route_id) REFERENCES routing.routes (organization_id, id)
);
CREATE INDEX gps_events_vehicle_time_idx ON operations.gps_events (vehicle_id, recorded_at DESC);

-- +goose Down
DROP TABLE operations.gps_events;
DROP TABLE routing.order_assignments;
DROP TABLE routing.route_stops;
DROP TABLE routing.routes;
ALTER TABLE optimization.jobs DROP CONSTRAINT jobs_parent_plan_fk;
DROP TABLE routing.route_plans;
ALTER TABLE optimization.jobs DROP CONSTRAINT jobs_org_id_key;
ALTER TABLE optimization.jobs DROP CONSTRAINT jobs_organization_fk;
DROP TABLE operations.orders;
DROP TABLE operations.driver_assignments;
DROP TABLE operations.drivers;
DROP TABLE operations.vehicles;
DROP TABLE core.outbox;
DROP TABLE core.depots;
DROP TABLE core.refresh_tokens;
DROP TABLE core.users;
DROP TABLE core.organizations;
DROP SCHEMA routing;
DROP SCHEMA operations;
DROP SCHEMA core;
