-- name: LockBuildCacheOwner :one
SELECT id FROM app_identifiers WHERE app_id = $1 AND id = $2 FOR UPDATE;

-- name: BuildCacheUsage :one
SELECT COALESCE(sum(size), 0)::bigint AS bytes, count(*) AS objects
FROM (
    SELECT active.size FROM build_cache_objects AS active WHERE active.app_id = $1 AND active.app_identifier_id = $2
    UNION ALL
    SELECT retired.size FROM build_cache_cleanup AS retired WHERE retired.app_id = $1 AND retired.app_identifier_id = $2
) AS retained;

-- name: InsertBuildCacheObject :one
INSERT INTO build_cache_objects (id, app_id, app_identifier_id, namespace, cache_key, size, sha256)
VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING *;

-- name: GetBuildCacheUpload :one
SELECT * FROM build_cache_objects WHERE app_id = $1 AND app_identifier_id = $2 AND id = $3 AND expires_at > now();

-- name: FindBuildCacheObject :one
SELECT * FROM build_cache_objects WHERE app_id = $1 AND app_identifier_id = $2 AND namespace = $3 AND cache_key = $4 AND published_at IS NOT NULL AND expires_at > now();

-- name: RemovePreviousBuildCacheObject :exec
DELETE FROM build_cache_objects WHERE app_id = $1 AND app_identifier_id = $2 AND namespace = $3 AND cache_key = $4 AND published_at IS NOT NULL AND id <> $5;

-- name: PublishBuildCacheObject :one
UPDATE build_cache_objects SET published_at = now(), expires_at = now() + interval '30 days'
WHERE app_id = $1 AND app_identifier_id = $2 AND id = $3 AND expires_at > now() RETURNING *;

-- name: DeleteBuildCacheObject :exec
DELETE FROM build_cache_objects WHERE app_id = $1 AND app_identifier_id = $2 AND id = $3;

-- name: ExpireBuildCacheObjects :execrows
DELETE FROM build_cache_objects WHERE id IN (SELECT id FROM build_cache_objects WHERE expires_at <= now() LIMIT 100 FOR UPDATE SKIP LOCKED);

-- name: DueBuildCacheCleanup :many
SELECT * FROM build_cache_cleanup WHERE due_at <= now() ORDER BY due_at LIMIT 4 FOR UPDATE SKIP LOCKED;

-- name: DeleteBuildCacheCleanup :exec
DELETE FROM build_cache_cleanup WHERE id = $1;

-- name: RetryBuildCacheCleanup :exec
UPDATE build_cache_cleanup SET due_at = now() + interval '15 minutes' WHERE id = $1;
