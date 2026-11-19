-- ---------------------------------------------------------------- materialization

-- Serializes plan creation per organization and day, so version numbers never collide.
-- name: LockPlanningDay :exec
SELECT pg_advisory_xact_lock(hashtextextended(@lock_key::text, 0));

-- name: GetJobForPlan :one
SELECT organization_id, status, planning_date, coalesce(input_snapshot->>'timezone', '')::text AS timezone, result
FROM optimization.jobs WHERE id = @id;

-- name: NextPlanVersion :one
SELECT (coalesce(max(version), 0) + 1)::int4 AS version
FROM routing.route_plans WHERE organization_id = @organization_id AND planning_date = @planning_date;

-- name: InsertPlan :one
INSERT INTO routing.route_plans (organization_id, optimization_job_id, planning_date, version, status,
    total_distance_km, total_cost, algorithm, objective, unassigned_order_ids)
VALUES (@organization_id, @optimization_job_id, @planning_date, @version, 'DRAFT',
    @total_distance_km, @total_cost, @algorithm, @objective, @unassigned_order_ids)
ON CONFLICT (optimization_job_id) DO NOTHING
RETURNING id;

-- name: InsertRoute :one
INSERT INTO routing.routes (organization_id, route_plan_id, vehicle_id, driver_id,
    total_distance_km, estimated_duration_minutes)
VALUES (@organization_id, @route_plan_id, @vehicle_id, sqlc.narg(driver_id), @total_distance_km,
    @estimated_duration_minutes)
RETURNING id;

-- name: InsertStops :copyfrom
INSERT INTO routing.route_stops (organization_id, route_id, order_id, sequence_number,
    planned_arrival, planned_departure)
VALUES (@organization_id, @route_id, @order_id, @sequence_number, @planned_arrival, @planned_departure);

-- ---------------------------------------------------------------- reads

-- name: ListPlans :many
SELECT p.id, p.optimization_job_id, p.planning_date, p.version, p.status, p.algorithm,
       p.total_distance_km, p.total_cost, p.objective,
       cardinality(p.unassigned_order_ids)::int4 AS unassigned_count,
       (SELECT count(*) FROM routing.routes r WHERE r.route_plan_id = p.id)::int4 AS route_count,
       p.created_at, p.approved_at, p.activated_at
FROM routing.route_plans p
WHERE p.organization_id = @organization_id
  AND (sqlc.narg(planning_date)::date IS NULL OR p.planning_date = sqlc.narg(planning_date)::date)
  AND (sqlc.narg(status)::text IS NULL OR p.status = sqlc.narg(status)::text)
ORDER BY p.planning_date DESC, p.version DESC
LIMIT @lim OFFSET @off;

-- name: GetPlan :one
SELECT p.id, p.optimization_job_id, p.planning_date, p.version, p.status, p.algorithm,
       p.total_distance_km, p.total_cost, p.objective, p.unassigned_order_ids,
       p.created_at, p.approved_by, p.approved_at, p.activated_at
FROM routing.route_plans p
WHERE p.organization_id = @organization_id AND p.id = @id;

-- name: ListRoutesForPlan :many
SELECT r.id, r.vehicle_id, v.registration_number, r.driver_id, d.name AS driver_name, r.status,
       r.total_distance_km, r.estimated_duration_minutes
FROM routing.routes r
JOIN operations.vehicles v ON v.id = r.vehicle_id
LEFT JOIN operations.drivers d ON d.id = r.driver_id
WHERE r.organization_id = @organization_id AND r.route_plan_id = @route_plan_id
ORDER BY v.registration_number;

-- name: ListStopsForPlan :many
SELECT s.id, s.route_id, s.order_id, o.external_order_id, o.customer_name,
       ST_Y(o.delivery_location::geometry)::float8 AS lat, ST_X(o.delivery_location::geometry)::float8 AS lon,
       s.sequence_number, s.planned_arrival, s.planned_departure, s.actual_arrival, s.actual_departure,
       s.stop_status, o.time_window_start, o.time_window_end
FROM routing.route_stops s
JOIN routing.routes r ON r.id = s.route_id
JOIN operations.orders o ON o.id = s.order_id
WHERE s.organization_id = @organization_id AND r.route_plan_id = @route_plan_id
ORDER BY s.route_id, s.sequence_number;

-- name: GetRoute :one
SELECT r.id, r.route_plan_id, r.vehicle_id, v.registration_number, r.driver_id, d.name AS driver_name,
       r.status, r.total_distance_km, r.estimated_duration_minutes
FROM routing.routes r
JOIN operations.vehicles v ON v.id = r.vehicle_id
LEFT JOIN operations.drivers d ON d.id = r.driver_id
WHERE r.organization_id = @organization_id AND r.id = @id;

-- name: ListStopsForRoute :many
SELECT s.id, s.route_id, s.order_id, o.external_order_id, o.customer_name,
       ST_Y(o.delivery_location::geometry)::float8 AS lat, ST_X(o.delivery_location::geometry)::float8 AS lon,
       s.sequence_number, s.planned_arrival, s.planned_departure, s.actual_arrival, s.actual_departure,
       s.stop_status, o.time_window_start, o.time_window_end
FROM routing.route_stops s
JOIN operations.orders o ON o.id = s.order_id
WHERE s.organization_id = @organization_id AND s.route_id = @route_id
ORDER BY s.sequence_number;
