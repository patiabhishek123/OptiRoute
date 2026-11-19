-- ---------------------------------------------------------------- vehicles

-- name: CreateVehicle :one
INSERT INTO operations.vehicles (organization_id, depot_id, registration_number, vehicle_type,
    capacity_weight_kg, capacity_volume_m3, cost_per_km, fixed_cost)
VALUES (@organization_id, @depot_id, @registration_number, @vehicle_type,
    @capacity_weight_kg, @capacity_volume_m3, @cost_per_km, @fixed_cost)
RETURNING *;

-- name: ListVehicles :many
SELECT * FROM operations.vehicles
WHERE organization_id = @organization_id
  AND (sqlc.narg(depot_id)::uuid IS NULL OR depot_id = sqlc.narg(depot_id)::uuid)
  AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status)::text)
ORDER BY registration_number, id
LIMIT @lim OFFSET @off;

-- name: GetVehicle :one
SELECT * FROM operations.vehicles WHERE organization_id = @organization_id AND id = @id;

-- name: UpdateVehicle :one
UPDATE operations.vehicles SET
    depot_id            = coalesce(sqlc.narg(depot_id), depot_id),
    registration_number = coalesce(sqlc.narg(registration_number), registration_number),
    vehicle_type        = coalesce(sqlc.narg(vehicle_type), vehicle_type),
    capacity_weight_kg  = coalesce(sqlc.narg(capacity_weight_kg), capacity_weight_kg),
    capacity_volume_m3  = coalesce(sqlc.narg(capacity_volume_m3), capacity_volume_m3),
    cost_per_km         = coalesce(sqlc.narg(cost_per_km), cost_per_km),
    fixed_cost          = coalesce(sqlc.narg(fixed_cost), fixed_cost),
    status              = coalesce(sqlc.narg(status), status)
WHERE organization_id = @organization_id AND id = @id
RETURNING *;

-- ---------------------------------------------------------------- drivers

-- name: CreateDriver :one
INSERT INTO operations.drivers (organization_id, name, phone, shift_start, shift_end,
    break_duration_minutes, max_overtime_minutes)
VALUES (@organization_id, @name, @phone, @shift_start, @shift_end,
    @break_duration_minutes, @max_overtime_minutes)
RETURNING *;

-- name: ListDrivers :many
SELECT * FROM operations.drivers
WHERE organization_id = @organization_id
  AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status)::text)
ORDER BY name, id
LIMIT @lim OFFSET @off;

-- name: GetDriver :one
SELECT * FROM operations.drivers WHERE organization_id = @organization_id AND id = @id;

-- name: UpdateDriver :one
UPDATE operations.drivers SET
    name                   = coalesce(sqlc.narg(name), name),
    phone                  = coalesce(sqlc.narg(phone), phone),
    shift_start            = coalesce(sqlc.narg(shift_start), shift_start),
    shift_end              = coalesce(sqlc.narg(shift_end), shift_end),
    break_duration_minutes = coalesce(sqlc.narg(break_duration_minutes), break_duration_minutes),
    max_overtime_minutes   = coalesce(sqlc.narg(max_overtime_minutes), max_overtime_minutes),
    status                 = coalesce(sqlc.narg(status), status)
WHERE organization_id = @organization_id AND id = @id
RETURNING *;

-- ---------------------------------------------------------------- daily pairing

-- name: DeleteAssignmentsForDate :exec
DELETE FROM operations.driver_assignments
WHERE organization_id = @organization_id AND planning_date = @planning_date;

-- name: InsertAssignment :exec
INSERT INTO operations.driver_assignments (organization_id, planning_date, vehicle_id, driver_id)
VALUES (@organization_id, @planning_date, @vehicle_id, @driver_id);

-- name: ListAssignments :many
SELECT a.vehicle_id, v.registration_number, a.driver_id, d.name AS driver_name
FROM operations.driver_assignments a
JOIN operations.vehicles v ON v.id = a.vehicle_id
JOIN operations.drivers d ON d.id = a.driver_id
WHERE a.organization_id = @organization_id AND a.planning_date = @planning_date
ORDER BY v.registration_number;
