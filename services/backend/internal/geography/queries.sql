-- Depot locations are stored as GEOGRAPHY(POINT, 4326) and exposed as lat/lon.

-- name: CreateDepot :one
INSERT INTO core.depots (organization_id, name, location)
VALUES (@organization_id, @name,
        ST_SetSRID(ST_MakePoint(@lon::float8, @lat::float8), 4326)::geography)
RETURNING id, organization_id, name,
          ST_Y(location::geometry)::float8 AS lat, ST_X(location::geometry)::float8 AS lon,
          is_active, created_at;

-- name: ListDepots :many
SELECT id, organization_id, name,
       ST_Y(location::geometry)::float8 AS lat, ST_X(location::geometry)::float8 AS lon,
       is_active, created_at
FROM core.depots
WHERE organization_id = @organization_id
  AND (sqlc.narg(active)::boolean IS NULL OR is_active = sqlc.narg(active)::boolean)
ORDER BY name, id
LIMIT @lim OFFSET @off;

-- name: GetDepot :one
SELECT id, organization_id, name,
       ST_Y(location::geometry)::float8 AS lat, ST_X(location::geometry)::float8 AS lon,
       is_active, created_at
FROM core.depots
WHERE organization_id = @organization_id AND id = @id;

-- name: UpdateDepot :one
UPDATE core.depots SET
    name      = coalesce(sqlc.narg(name), name),
    is_active = coalesce(sqlc.narg(is_active), is_active),
    location  = CASE WHEN sqlc.narg(lat)::float8 IS NULL THEN location
                     ELSE ST_SetSRID(ST_MakePoint(sqlc.narg(lon)::float8, sqlc.narg(lat)::float8), 4326)::geography
                END
WHERE organization_id = @organization_id AND id = @id
RETURNING id, organization_id, name,
          ST_Y(location::geometry)::float8 AS lat, ST_X(location::geometry)::float8 AS lon,
          is_active, created_at;
