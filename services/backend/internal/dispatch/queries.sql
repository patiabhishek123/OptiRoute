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

-- ---------------------------------------------------------------- lifecycle

-- name: LockPlan :one
SELECT id, status, planning_date FROM routing.route_plans
WHERE organization_id = @organization_id AND id = @id
FOR UPDATE;

-- name: ApprovePlan :exec
UPDATE routing.route_plans SET status = 'APPROVED', approved_by = @approved_by, approved_at = now()
WHERE id = @id;

-- name: RejectPlan :exec
UPDATE routing.route_plans SET status = 'REJECTED' WHERE id = @id;

-- name: ActivePlanForDay :one
SELECT id FROM routing.route_plans
WHERE organization_id = @organization_id AND planning_date = @planning_date AND status = 'ACTIVE'
FOR UPDATE;

-- True once any route or stop of the plan has moved past its planned state (tracking, Phase 5).
-- name: PlanHasStarted :one
SELECT EXISTS (
    SELECT 1 FROM routing.routes r
    LEFT JOIN routing.route_stops s ON s.route_id = r.id
    WHERE r.route_plan_id = @route_plan_id AND (r.status <> 'PLANNED' OR s.stop_status <> 'PENDING')
)::boolean AS started;

-- Orders in the plan that can no longer be planned: anything not PENDING, except orders that are
-- PLANNED on the plan being replaced.
-- name: StaleOrders :many
SELECT o.id, o.external_order_id, o.status
FROM routing.route_stops s
JOIN routing.routes r ON r.id = s.route_id
JOIN operations.orders o ON o.id = s.order_id
WHERE r.route_plan_id = @route_plan_id
  AND NOT (o.status = 'PENDING'
       OR (o.status = 'PLANNED' AND EXISTS (
            SELECT 1 FROM routing.order_assignments a
            WHERE a.order_id = o.id AND a.route_plan_id = sqlc.narg(replaced_plan_id)::uuid)))
ORDER BY o.external_order_id;

-- name: SupersedePlan :exec
UPDATE routing.route_plans SET status = 'SUPERSEDED' WHERE id = @id;

-- name: DeleteAssignmentsForPlan :many
DELETE FROM routing.order_assignments WHERE route_plan_id = @route_plan_id RETURNING order_id;

-- name: InsertAssignmentsForPlan :execrows
INSERT INTO routing.order_assignments (order_id, organization_id, route_id, route_plan_id)
SELECT s.order_id, s.organization_id, s.route_id, r.route_plan_id
FROM routing.route_stops s JOIN routing.routes r ON r.id = s.route_id
WHERE r.route_plan_id = @route_plan_id;

-- name: MarkPlanOrdersPlanned :execrows
UPDATE operations.orders SET status = 'PLANNED'
WHERE id IN (SELECT order_id FROM routing.order_assignments WHERE route_plan_id = @route_plan_id);

-- Orders of a superseded plan that the new plan does not cover go back to PENDING.
-- name: RevertUnassignedOrders :many
UPDATE operations.orders o SET status = 'PENDING'
WHERE o.id = ANY(@order_ids::uuid[]) AND o.status = 'PLANNED'
  AND NOT EXISTS (SELECT 1 FROM routing.order_assignments a WHERE a.order_id = o.id)
RETURNING o.id;

-- name: ActivatePlan :exec
UPDATE routing.route_plans SET status = 'ACTIVE', activated_at = now() WHERE id = @id;

-- name: InsertPlanEvent :exec
INSERT INTO core.outbox (topic, payload) VALUES ('opti:plan-events', @payload);
