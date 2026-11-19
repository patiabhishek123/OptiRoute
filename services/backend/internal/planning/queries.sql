-- Snapshot reads span orders, fleet and depots. They are read-only and run in one REPEATABLE READ
-- transaction, so the problem sent to the optimizer is one consistent view of the day.

-- name: OrganizationTimezone :one
SELECT timezone FROM core.organizations WHERE id = @organization_id;

-- Routable resources: AVAILABLE vehicles paired with an ACTIVE driver for the day, at an active depot.
-- name: SnapshotVehicles :many
SELECT v.id, v.depot_id, v.registration_number, v.capacity_weight_kg, v.capacity_volume_m3,
       v.cost_per_km, v.fixed_cost, d.id AS driver_id, d.shift_start, d.shift_end,
       d.break_duration_minutes, d.max_overtime_minutes
FROM operations.driver_assignments a
JOIN operations.vehicles v ON v.id = a.vehicle_id
JOIN operations.drivers d ON d.id = a.driver_id
JOIN core.depots dp ON dp.id = v.depot_id
WHERE a.organization_id = @organization_id AND a.planning_date = @planning_date
  AND v.status = 'AVAILABLE' AND d.status = 'ACTIVE' AND dp.is_active
  AND (sqlc.narg(vehicle_ids)::uuid[] IS NULL OR v.id = ANY(sqlc.narg(vehicle_ids)::uuid[]))
  AND (sqlc.narg(depot_ids)::uuid[] IS NULL OR v.depot_id = ANY(sqlc.narg(depot_ids)::uuid[]))
ORDER BY v.registration_number;

-- name: SnapshotOrders :many
SELECT o.id, o.external_order_id, o.depot_id,
       ST_Y(o.delivery_location::geometry)::float8 AS lat, ST_X(o.delivery_location::geometry)::float8 AS lon,
       o.demand_weight_kg, o.demand_volume_m3, o.time_window_start, o.time_window_end,
       o.service_duration_minutes, o.priority
FROM operations.orders o
WHERE o.organization_id = @organization_id AND o.planning_date = @planning_date AND o.status = 'PENDING'
  AND (sqlc.narg(order_ids)::uuid[] IS NULL OR o.id = ANY(sqlc.narg(order_ids)::uuid[]))
  AND (sqlc.narg(batch_id)::text IS NULL OR o.batch_id = sqlc.narg(batch_id)::text)
  AND (sqlc.narg(depot_ids)::uuid[] IS NULL OR o.depot_id IS NULL
       OR o.depot_id = ANY(sqlc.narg(depot_ids)::uuid[]))
ORDER BY o.external_order_id;

-- name: SnapshotDepots :many
SELECT id, ST_Y(location::geometry)::float8 AS lat, ST_X(location::geometry)::float8 AS lon
FROM core.depots
WHERE organization_id = @organization_id AND id = ANY(@ids::uuid[])
ORDER BY id;

-- ---------------------------------------------------------------- jobs

-- name: FindJobByIdempotencyKey :one
SELECT id FROM optimization.jobs WHERE organization_id = @organization_id AND idempotency_key = @idempotency_key;

-- name: InsertJob :exec
INSERT INTO optimization.jobs (id, organization_id, kind, status, planning_date, idempotency_key,
    input_snapshot, solver_config)
VALUES (@id, @organization_id, 'SOLVE', 'QUEUED', @planning_date, sqlc.narg(idempotency_key),
    @input_snapshot, @solver_config);

-- name: InsertOutbox :exec
INSERT INTO core.outbox (topic, payload) VALUES (@topic, @payload);

-- name: GetJob :one
SELECT id, kind, status, planning_date, coalesce(solver_config->>'algorithm', '')::text AS algorithm,
       coalesce(jsonb_array_length(input_snapshot->'orders'), 0)::int4 AS order_count,
       coalesce(jsonb_array_length(input_snapshot->'vehicles'), 0)::int4 AS vehicle_count,
       objective_value, total_distance_km, execution_time_ms, error_message, cancel_requested,
       attempts, created_at, started_at, completed_at
FROM optimization.jobs
WHERE organization_id = @organization_id AND id = @id;

-- name: ListJobs :many
SELECT id, kind, status, planning_date, coalesce(solver_config->>'algorithm', '')::text AS algorithm,
       coalesce(jsonb_array_length(input_snapshot->'orders'), 0)::int4 AS order_count,
       coalesce(jsonb_array_length(input_snapshot->'vehicles'), 0)::int4 AS vehicle_count,
       objective_value, total_distance_km, execution_time_ms, error_message, cancel_requested,
       attempts, created_at, started_at, completed_at
FROM optimization.jobs
WHERE organization_id = @organization_id
  AND (sqlc.narg(planning_date)::date IS NULL OR planning_date = sqlc.narg(planning_date)::date)
ORDER BY created_at DESC
LIMIT @lim OFFSET @off;

-- name: GetJobResult :one
SELECT status, result FROM optimization.jobs WHERE organization_id = @organization_id AND id = @id;

-- A QUEUED job is cancelled at once (the worker skips non-active jobs); a running one is flagged
-- and the worker stops at its next stage boundary.
-- name: RequestCancel :one
UPDATE optimization.jobs SET
    cancel_requested = true,
    status       = CASE WHEN status = 'QUEUED' THEN 'CANCELLED' ELSE status END,
    completed_at = CASE WHEN status = 'QUEUED' THEN now() ELSE completed_at END
WHERE organization_id = @organization_id AND id = @id
  AND status IN ('QUEUED', 'PREPROCESSING', 'SOLVING', 'VALIDATING')
RETURNING status;

-- name: JobStatus :one
SELECT status FROM optimization.jobs WHERE organization_id = @organization_id AND id = @id;
