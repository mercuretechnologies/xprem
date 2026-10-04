-- The serving inventory is picked before pagination: an older runtime remains
-- served even when many newer publishes use another runtime. Active rollout
-- controls also remain served, including rollback-to-embedded controls.
-- name: GetLatestUpdateFeed :many
WITH heads AS (
    SELECT DISTINCT ON (u.branch_id, u.runtime_version_id, u.platform)
           u.branch_id, u.id,
           CASE WHEN u.rollout_percentage IS NOT NULL THEN u.control_update_id END AS control_id
    FROM updates u
    JOIN branches b ON b.id = u.branch_id
    JOIN runtime_versions rv ON rv.id = u.runtime_version_id
    WHERE b.app_id = @app_id
      AND (@branch::text = '' OR b.name = @branch)
      AND (@runtime_version::text = '' OR rv.version = @runtime_version)
      AND (@platform::text = '' OR u.platform = @platform)
      AND u.checked_at IS NOT NULL
    ORDER BY u.branch_id, u.runtime_version_id, u.platform, u.id DESC
), served AS (
    SELECT branch_id, id FROM heads
    UNION
    SELECT branch_id, control_id FROM heads WHERE control_id IS NOT NULL
)
SELECT u.id, u.update_uuid, u.update_type, u.created_at, u.commit_hash,
       u.platform, u.message, u.rollout_percentage, u.control_update_id,
       u.publish_group, u.branch_id, b.name AS branch_name,
       rv.version AS runtime_version
FROM updates u
JOIN branches b ON u.branch_id = b.id
JOIN runtime_versions rv ON u.runtime_version_id = rv.id
WHERE (u.branch_id, u.id) IN (SELECT branch_id, id FROM served)
  AND b.app_id = @app_id
  AND u.checked_at IS NOT NULL
  AND (@update_uuid::text = '' OR u.update_uuid::text ILIKE '%' || @update_uuid || '%')
  AND (@publish_group::text = '' OR u.publish_group::text ILIKE '%' || @publish_group || '%')
  AND (@commit_hash::text = '' OR u.commit_hash ILIKE '%' || @commit_hash || '%')
  AND (@created_from::timestamptz IS NULL OR u.created_at >= @created_from)
  AND (@created_to::timestamptz IS NULL OR u.created_at <= @created_to)
  AND (
    NOT @has_cursor::boolean
    OR (u.created_at, u.branch_id, u.id) < (@cursor_created_at::timestamptz, @cursor_branch_id::bigint, @cursor_update_id::bigint)
  )
ORDER BY u.created_at DESC, u.branch_id DESC, u.id DESC
LIMIT @row_limit::int;
