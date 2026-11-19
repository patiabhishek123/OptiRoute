-- name: CreateOrganization :one
INSERT INTO core.organizations (name, slug, timezone, currency)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetOrganization :one
SELECT * FROM core.organizations WHERE id = $1;

-- name: CreateUser :one
INSERT INTO core.users (organization_id, email, password_hash, role)
VALUES ($1, $2, $3, $4)
RETURNING id, organization_id, email, role, created_at;

-- name: GetUserForLogin :one
SELECT u.id, u.organization_id, u.password_hash, u.role
FROM core.users u
JOIN core.organizations o ON o.id = u.organization_id
WHERE o.slug = $1 AND u.email = $2;

-- name: GetUser :one
SELECT id, organization_id, email, role, created_at FROM core.users WHERE id = $1;

-- name: ListUsers :many
SELECT id, organization_id, email, role, created_at
FROM core.users
WHERE organization_id = $1
ORDER BY created_at, email;

-- name: InsertRefreshToken :exec
INSERT INTO core.refresh_tokens (user_id, family_id, token_hash, expires_at)
VALUES ($1, $2, $3, $4);

-- name: GetRefreshTokenForUpdate :one
SELECT id, user_id, family_id, expires_at, revoked_at
FROM core.refresh_tokens
WHERE token_hash = $1
FOR UPDATE;

-- name: RevokeRefreshToken :exec
UPDATE core.refresh_tokens SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL;

-- name: RevokeRefreshFamily :exec
UPDATE core.refresh_tokens SET revoked_at = now() WHERE family_id = $1 AND revoked_at IS NULL;
