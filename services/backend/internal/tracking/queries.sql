-- ---------------------------------------------------------------- vehicle locations

-- name: VehicleExists :one
SELECT EXISTS (SELECT 1 FROM operations.vehicles WHERE organization_id = @organization_id AND id = @id)::boolean;

-- The route a vehicle is driving now: IN_PROGRESS on an ACTIVE plan (at most one, by index).
-- name: CurrentRouteForVehicle :one
SELECT r.id FROM routing.routes r
JOIN routing.route_plans p ON p.id = r.route_plan_id
WHERE r.organization_id = @organization_id AND r.vehicle_id = @vehicle_id
  AND r.status = 'IN_PROGRESS' AND p.status = 'ACTIVE';

-- name: InsertGPSEvent :exec
INSERT INTO operations.gps_events (organization_id, vehicle_id, route_id, location, speed_kmh, heading, recorded_at)
VALUES (@organization_id, @vehicle_id, sqlc.narg(route_id),
        ST_SetSRID(ST_MakePoint(@lon::float8, @lat::float8), 4326)::geography,
        sqlc.narg(speed_kmh), sqlc.narg(heading), @recorded_at);

-- name: LatestVehicleLocation :one
SELECT vehicle_id, route_id,
       ST_Y(location::geometry)::float8 AS lat, ST_X(location::geometry)::float8 AS lon,
       speed_kmh, heading, recorded_at
FROM operations.gps_events
WHERE organization_id = @organization_id AND vehicle_id = @vehicle_id
ORDER BY recorded_at DESC, id DESC
LIMIT 1;

-- name: LatestRouteLocation :one
SELECT vehicle_id, route_id,
       ST_Y(location::geometry)::float8 AS lat, ST_X(location::geometry)::float8 AS lon,
       speed_kmh, heading, recorded_at
FROM operations.gps_events
WHERE route_id = @route_id
ORDER BY recorded_at DESC, id DESC
LIMIT 1;

-- ---------------------------------------------------------------- route progress

-- name: GetRoute :one
SELECT r.id, r.route_plan_id, p.status AS plan_status, p.planning_date, r.vehicle_id, v.registration_number,
       r.status, r.started_at, r.completed_at
FROM routing.routes r
JOIN routing.route_plans p ON p.id = r.route_plan_id
JOIN operations.vehicles v ON v.id = r.vehicle_id
WHERE r.organization_id = @organization_id AND r.id = @id;

-- name: LockRoute :one
SELECT r.id, p.status AS plan_status, r.vehicle_id, r.status, r.started_at
FROM routing.routes r
JOIN routing.route_plans p ON p.id = r.route_plan_id
WHERE r.organization_id = @organization_id AND r.id = @id
FOR UPDATE OF r;

-- name: ListStops :many
SELECT s.id, s.order_id, o.external_order_id, o.customer_name, s.sequence_number,
       s.planned_arrival, s.actual_arrival, s.actual_departure, s.stop_status, s.failure_reason
FROM routing.route_stops s
JOIN operations.orders o ON o.id = s.order_id
WHERE s.route_id = @route_id
ORDER BY s.sequence_number;

-- name: StartRoute :exec
UPDATE routing.routes SET status = 'IN_PROGRESS', started_at = @at WHERE id = @id;

-- The vehicle has left the depot with these orders.
-- name: DispatchRouteOrders :execrows
UPDATE operations.orders SET status = 'DISPATCHED'
WHERE status = 'PLANNED' AND id IN (SELECT order_id FROM routing.route_stops WHERE route_id = @route_id);

-- name: LockStop :one
SELECT id, order_id, stop_status, actual_arrival FROM routing.route_stops
WHERE route_id = @route_id AND id = @id
FOR UPDATE;

-- name: ArriveStop :exec
UPDATE routing.route_stops SET stop_status = 'ARRIVED', actual_arrival = @at WHERE id = @id;

-- name: FinishStop :exec
UPDATE routing.route_stops
SET stop_status = @stop_status, actual_departure = @at, failure_reason = sqlc.narg(failure_reason)
WHERE id = @id;

-- name: SetOrderOutcome :exec
UPDATE operations.orders SET status = @status WHERE id = @id AND status = 'DISPATCHED';

-- name: CountOpenStops :one
SELECT count(*)::int4 FROM routing.route_stops
WHERE route_id = @route_id AND stop_status IN ('PENDING', 'ARRIVED');

-- name: CompleteRoute :exec
UPDATE routing.routes SET status = 'COMPLETED', completed_at = @at WHERE id = @id;
