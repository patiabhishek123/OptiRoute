-- Orders. Locations are GEOGRAPHY(POINT, 4326), exposed as lat/lon.

-- name: OrganizationTimezone :one
SELECT timezone FROM core.organizations WHERE id = @organization_id;

-- name: CreateOrder :one
INSERT INTO operations.orders (organization_id, depot_id, external_order_id, planning_date, batch_id,
    customer_name, customer_phone, delivery_location, demand_weight_kg, demand_volume_m3,
    time_window_start, time_window_end, service_duration_minutes, priority)
VALUES (@organization_id, sqlc.narg(depot_id), @external_order_id, @planning_date, sqlc.narg(batch_id),
    @customer_name, sqlc.narg(customer_phone),
    ST_SetSRID(ST_MakePoint(@lon::float8, @lat::float8), 4326)::geography,
    @demand_weight_kg, @demand_volume_m3, @time_window_start, @time_window_end,
    @service_duration_minutes, @priority)
RETURNING id;

-- CSV import: sqlc generates a pgx batch (all rows pipelined in one round trip, one transaction).
-- name: InsertOrderBatch :batchexec
INSERT INTO operations.orders (organization_id, depot_id, external_order_id, planning_date, batch_id,
    customer_name, customer_phone, delivery_location, demand_weight_kg, demand_volume_m3,
    time_window_start, time_window_end, service_duration_minutes, priority)
VALUES (@organization_id, sqlc.narg(depot_id), @external_order_id, @planning_date, sqlc.narg(batch_id),
    @customer_name, sqlc.narg(customer_phone),
    ST_SetSRID(ST_MakePoint(@lon::float8, @lat::float8), 4326)::geography,
    @demand_weight_kg, @demand_volume_m3, @time_window_start, @time_window_end,
    @service_duration_minutes, @priority);

-- name: ExistingExternalIDs :many
SELECT external_order_id FROM operations.orders
WHERE organization_id = @organization_id AND external_order_id = ANY(@external_order_ids::text[]);

-- name: GetOrder :one
SELECT id, depot_id, external_order_id, planning_date, batch_id, customer_name, customer_phone,
       ST_Y(delivery_location::geometry)::float8 AS lat, ST_X(delivery_location::geometry)::float8 AS lon,
       demand_weight_kg, demand_volume_m3, time_window_start, time_window_end,
       service_duration_minutes, priority, status, created_at
FROM operations.orders
WHERE organization_id = @organization_id AND id = @id;

-- name: ListOrders :many
SELECT id, depot_id, external_order_id, planning_date, batch_id, customer_name, customer_phone,
       ST_Y(delivery_location::geometry)::float8 AS lat, ST_X(delivery_location::geometry)::float8 AS lon,
       demand_weight_kg, demand_volume_m3, time_window_start, time_window_end,
       service_duration_minutes, priority, status, created_at
FROM operations.orders
WHERE organization_id = @organization_id
  AND (sqlc.narg(planning_date)::date IS NULL OR planning_date = sqlc.narg(planning_date)::date)
  AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status)::text)
  AND (sqlc.narg(depot_id)::uuid IS NULL OR depot_id = sqlc.narg(depot_id)::uuid)
  AND (sqlc.narg(batch_id)::text IS NULL OR batch_id = sqlc.narg(batch_id)::text)
ORDER BY planning_date, time_window_start, external_order_id
LIMIT @lim OFFSET @off;

-- Only PENDING orders are editable: a planned order's plan would silently go stale.
-- name: UpdatePendingOrder :one
UPDATE operations.orders SET
    depot_id                 = coalesce(sqlc.narg(depot_id), depot_id),
    customer_name            = coalesce(sqlc.narg(customer_name), customer_name),
    customer_phone           = coalesce(sqlc.narg(customer_phone), customer_phone),
    delivery_location        = CASE WHEN sqlc.narg(lat)::float8 IS NULL THEN delivery_location
        ELSE ST_SetSRID(ST_MakePoint(sqlc.narg(lon)::float8, sqlc.narg(lat)::float8), 4326)::geography END,
    demand_weight_kg         = coalesce(sqlc.narg(demand_weight_kg), demand_weight_kg),
    demand_volume_m3         = coalesce(sqlc.narg(demand_volume_m3), demand_volume_m3),
    time_window_start        = coalesce(sqlc.narg(time_window_start), time_window_start),
    time_window_end          = coalesce(sqlc.narg(time_window_end), time_window_end),
    service_duration_minutes = coalesce(sqlc.narg(service_duration_minutes), service_duration_minutes),
    priority                 = coalesce(sqlc.narg(priority), priority)
WHERE organization_id = @organization_id AND id = @id AND status = 'PENDING'
RETURNING id;

-- name: CancelOrder :one
UPDATE operations.orders SET status = 'CANCELLED'
WHERE organization_id = @organization_id AND id = @id AND status IN ('PENDING', 'PLANNED')
RETURNING id;

-- name: OrderStatus :one
SELECT status FROM operations.orders WHERE organization_id = @organization_id AND id = @id;
