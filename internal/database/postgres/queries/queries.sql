-- name: GetAppByID :one
SELECT * FROM apps
WHERE id = $1 LIMIT 1;

-- name: GetApps :many
SELECT id, name 
FROM apps
ORDER BY created_at ASC;

-- name: InsertApp :one
INSERT INTO apps (id, name, keys_mode, sealed_public_key, sealed_private_key, path_public_key, path_private_key, aws_secret_id_public, aws_secret_id_private)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING id;

-- name: DeleteAppByID :execresult
DELETE FROM apps
WHERE id = $1;

-- name: UpdateAppNameByID :execresult
UPDATE apps 
SET name = $2, updated_at = CURRENT_TIMESTAMP
WHERE id = $1;

-- name: InsertChannel :one
INSERT INTO channels (app_id, branch_id, name)
VALUES ($1, $2, $3)
RETURNING id;

-- name: DeleteChannelByName :execresult
DELETE FROM channels
WHERE name = $1 AND app_id = $2;

-- name: GetChannelsByAppID :many
WITH latest_runtime AS (
    SELECT DISTINCT ON (u.branch_id)
        u.branch_id,
        u.runtime_version_id,
        rv.version
    FROM updates u
    JOIN branches b ON b.id = u.branch_id
    JOIN runtime_versions rv ON rv.id = u.runtime_version_id
    WHERE b.app_id = $1 AND u.checked_at IS NOT NULL
    ORDER BY u.branch_id, rv.created_at DESC, rv.id DESC
),
current_updates AS (
    SELECT DISTINCT ON (u.branch_id)
        u.branch_id,
        lr.version AS runtime_version,
        u.commit_hash,
        u.created_at,
        u.rollout_percentage
    FROM latest_runtime lr
    JOIN updates u
      ON u.branch_id = lr.branch_id
     AND u.runtime_version_id = lr.runtime_version_id
    WHERE u.checked_at IS NOT NULL
    ORDER BY
        u.branch_id,
        (u.rollout_percentage IS NOT NULL) DESC,
        u.created_at DESC,
        u.id DESC
)
SELECT channels.*, branches.name as branch_name,
    cr.id AS rollout_id,
    rb.name AS rollout_branch_name,
    cr.percentage AS rollout_percentage,
    cr.created_at AS rollout_created_at,
    cr.updated_at AS rollout_updated_at,
    bcu.runtime_version AS branch_current_runtime_version,
    bcu.commit_hash AS branch_current_commit_hash,
    bcu.created_at AS branch_current_update_created_at,
    bcu.rollout_percentage AS branch_current_rollout_percentage,
    rcu.runtime_version AS rollout_branch_current_runtime_version,
    rcu.commit_hash AS rollout_branch_current_commit_hash,
    rcu.created_at AS rollout_branch_current_update_created_at,
    rcu.rollout_percentage AS rollout_branch_current_rollout_percentage
FROM channels
LEFT JOIN branches ON channels.branch_id = branches.id AND branches.app_id = channels.app_id
LEFT JOIN channel_rollouts cr ON cr.channel_id = channels.id
LEFT JOIN branches rb ON cr.rollout_branch_id = rb.id
LEFT JOIN current_updates bcu ON bcu.branch_id = channels.branch_id
LEFT JOIN current_updates rcu ON rcu.branch_id = cr.rollout_branch_id
WHERE channels.app_id = $1
ORDER BY channels.created_at ASC;

-- name: GetChannelNamesByBranchName :many
SELECT c.name
FROM channels c
INNER JOIN branches b ON c.branch_id = b.id AND b.app_id = c.app_id
WHERE b.name = $1 AND b.app_id = $2
ORDER BY c.created_at ASC;

-- name: GetChannelBranchMapping :one
-- Hot path (manifest resolution). The LEFT JOINs fold the channel's active rollout
-- (if any) into the single mapping read so branch resolution stays ONE query.
SELECT c.id, b.name AS branch_name,
    cr.id AS rollout_id,
    rb.name AS rollout_branch_name,
    cr.percentage AS rollout_percentage
FROM channels c
JOIN branches b ON c.branch_id = b.id AND b.app_id = c.app_id
LEFT JOIN channel_rollouts cr ON cr.channel_id = c.id
LEFT JOIN branches rb ON cr.rollout_branch_id = rb.id
WHERE c.app_id = $1 AND c.name = $2;

-- name: InsertBranch :one
INSERT INTO branches (app_id, name)
VALUES ($1, $2)
RETURNING id;

-- name: GetBranchByName :one
SELECT id FROM branches
WHERE name = $1 AND app_id = $2
LIMIT 1;

-- name: DeleteBranchByName :execresult
-- NOT protected: a protected branch cannot be deleted by anyone, admins
-- included, and lifting the protection is the explicit step. The guard runs
-- inside the DELETE itself so a concurrent protect cannot race it; the store
-- disambiguates the 0-rows result into protected vs not-found.
DELETE FROM branches
WHERE name = $1 AND app_id = $2 AND NOT protected;

-- name: GetBranchesByAppID :many
WITH latest_runtime AS (
    SELECT DISTINCT ON (u.branch_id)
        u.branch_id,
        u.runtime_version_id,
        rv.version
    FROM updates u
    JOIN branches b ON b.id = u.branch_id
    JOIN runtime_versions rv ON rv.id = u.runtime_version_id
    WHERE b.app_id = $1 AND u.checked_at IS NOT NULL
    ORDER BY u.branch_id, rv.created_at DESC, rv.id DESC
),
current_updates AS (
    SELECT DISTINCT ON (u.branch_id)
        u.branch_id,
        lr.version AS runtime_version,
        u.commit_hash,
        u.created_at,
        u.rollout_percentage
    FROM latest_runtime lr
    JOIN updates u
      ON u.branch_id = lr.branch_id
     AND u.runtime_version_id = lr.runtime_version_id
    WHERE u.checked_at IS NOT NULL
    ORDER BY
        u.branch_id,
        (u.rollout_percentage IS NOT NULL) DESC,
        u.created_at DESC,
        u.id DESC
)
SELECT DISTINCT ON (branches.id) 
    branches.*, 
    channels.name AS channel_name,
    cu.runtime_version AS current_runtime_version,
    cu.commit_hash AS current_commit_hash,
    cu.created_at AS current_update_created_at,
    cu.rollout_percentage AS current_rollout_percentage
FROM branches
LEFT JOIN channels ON branches.id = channels.branch_id AND channels.app_id = branches.app_id
LEFT JOIN current_updates cu ON cu.branch_id = branches.id
WHERE branches.app_id = $1
ORDER BY branches.id, channels.created_at ASC NULLS LAST;

-- name: UpdateChannelBranchMapping :execresult
-- The EXISTS clause scopes the *target* branch to the caller's app. fk_channels_branch
-- only references branches(id), so without it any tenant's branch id satisfies the FK.
-- The NOT EXISTS clause refuses to remap a channel while it has an active rollout
-- (the mapping is locked until the rollout is promoted or reverted). Promotion repoints
-- the channel through RepointChannelToRolloutBranch instead, so it is not blocked here.
UPDATE channels
SET branch_id = $1
WHERE channels.app_id = $2
  AND channels.id = $3
  AND EXISTS (
      SELECT 1 FROM branches
      WHERE branches.id = $1 AND branches.app_id = $2
  )
  AND NOT EXISTS (
      SELECT 1 FROM channel_rollouts
      WHERE channel_rollouts.channel_id = channels.id
  );

-- name: GetRuntimeVersionsWithUpdateCount :many
SELECT 
    rv.id, 
    rv.version, 
    rv.created_at, 
    rv.updated_at,
    (
        SELECT COUNT(u.id)
        FROM updates u
        JOIN branches b ON u.branch_id = b.id
        WHERE u.runtime_version_id = rv.id 
          AND b.name = $2 AND u.checked_at IS NOT NULL
    ) AS update_count,
    (
        SELECT MAX(u.rollout_percentage)
        FROM updates u
        JOIN branches b ON u.branch_id = b.id
        WHERE u.runtime_version_id = rv.id
          AND b.name = $2
          AND u.checked_at IS NOT NULL
          AND u.rollout_percentage IS NOT NULL
    ) AS rollout_percentage
FROM runtime_versions rv
WHERE rv.app_id = $1
  -- Only allow rows where at least one matching update exists
  AND EXISTS (
      SELECT 1 
      FROM updates u
      JOIN branches b ON u.branch_id = b.id
      WHERE u.runtime_version_id = rv.id 
        AND b.name = $2
        AND u.checked_at IS NOT NULL
  )
ORDER BY rv.created_at DESC;

-- name: InsertRuntimeVersion :one
INSERT INTO runtime_versions (app_id, version)
VALUES ($1, $2)
RETURNING id;

-- name: GetUpdatesByByBranchNameAndRuntimeVersion :many
SELECT u.id, u.update_uuid, u.update_type, u.created_at, u.commit_hash, u.platform, u.message, u.checked_at, u.rollout_percentage, u.control_update_id, u.publish_group
FROM updates u
JOIN runtime_versions rv ON u.runtime_version_id = rv.id
JOIN branches b ON u.branch_id = b.id
JOIN apps a ON b.app_id = a.id
WHERE a.id = $1 
  AND rv.version = $2 
  AND b.name = $3
  AND u.checked_at IS NOT NULL
ORDER BY u.created_at DESC;

-- name: GetUpdatesPageByBranchNameAndRuntimeVersion :many
SELECT u.id, u.update_uuid, u.update_type, u.created_at, u.commit_hash, u.platform, u.message, u.checked_at, u.rollout_percentage, u.control_update_id, u.publish_group
FROM updates u
JOIN runtime_versions rv ON u.runtime_version_id = rv.id
JOIN branches b ON u.branch_id = b.id
JOIN apps a ON b.app_id = a.id
WHERE a.id = sqlc.arg('app_id')
  AND rv.version = sqlc.arg('runtime_version')
  AND b.name = sqlc.arg('branch_name')
  AND u.checked_at IS NOT NULL
  AND (sqlc.narg('before_id')::BIGINT IS NULL OR u.id < sqlc.narg('before_id'))
ORDER BY u.id DESC
LIMIT sqlc.arg('row_limit');

-- name: GetUpdateFeed :many
SELECT u.id, u.update_uuid, u.update_type, u.created_at, u.commit_hash,
       u.platform, u.message, u.rollout_percentage, u.control_update_id,
       u.publish_group, u.branch_id, b.name AS branch_name,
       rv.version AS runtime_version
FROM updates u
JOIN branches b ON u.branch_id = b.id
JOIN runtime_versions rv ON u.runtime_version_id = rv.id
WHERE b.app_id = @app_id
  AND u.checked_at IS NOT NULL
  AND (@branch::text = '' OR b.name = @branch)
  AND (@runtime_version::text = '' OR rv.version = @runtime_version)
  AND (@platform::text = '' OR u.platform = @platform)
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

-- name: GetUpdateType :one
SELECT u.update_type 
FROM updates u
JOIN branches b ON u.branch_id = b.id
WHERE b.app_id = $1
  AND b.name = $2
  AND u.id = $3;

-- name: GetUpdateCheckedAt :one
SELECT u.checked_at
FROM updates u
JOIN branches b ON u.branch_id = b.id
WHERE b.app_id = $1
  AND b.name = $2
  AND u.id = $3;

-- name: GetUpdatesByPublishGroup :many
-- The members of one publish group on a branch and runtime version, for the
-- group republish (republish every platform of one eoas publish). Only
-- checked rows: an unchecked row is an unfinished upload, not a served update.
SELECT u.id, u.platform, u.commit_hash
FROM updates u
JOIN branches b ON u.branch_id = b.id
JOIN runtime_versions rv ON u.runtime_version_id = rv.id
WHERE b.app_id = $1
  AND b.name = $2
  AND rv.version = $3
  AND u.publish_group = $4
  AND u.checked_at IS NOT NULL
ORDER BY u.id;

-- name: GetPublishGroupsPage :many
-- Page logical publishes rather than update rows. page_groups applies the
-- limit before joining members back in, so every returned group is complete.
WITH grouped AS (
  SELECT u.publish_group, u.branch_id, u.runtime_version_id, MAX(u.id)::BIGINT AS newest_id
  FROM updates u
  JOIN branches b ON u.branch_id = b.id
  JOIN runtime_versions rv ON u.runtime_version_id = rv.id
  WHERE b.app_id = sqlc.arg('app_id')
    AND b.name = sqlc.arg('branch_name')
    AND rv.version = sqlc.arg('runtime_version')
    AND u.publish_group IS NOT NULL
    AND u.checked_at IS NOT NULL
  GROUP BY u.publish_group, u.branch_id, u.runtime_version_id
), page_groups AS (
  SELECT publish_group, branch_id, runtime_version_id, newest_id
  FROM grouped
  WHERE (sqlc.narg('before_id')::BIGINT IS NULL OR newest_id < sqlc.narg('before_id'))
  ORDER BY newest_id DESC
  LIMIT sqlc.arg('row_limit')
)
SELECT pg.publish_group, pg.newest_id, u.id, u.created_at, u.platform,
       u.commit_hash, u.message
FROM page_groups pg
JOIN updates u
  ON u.publish_group = pg.publish_group
 AND u.branch_id = pg.branch_id
 AND u.runtime_version_id = pg.runtime_version_id
WHERE u.checked_at IS NOT NULL
ORDER BY pg.newest_id DESC, u.id ASC;

-- name: GetUpdateMetadata :one
SELECT updates.id, update_uuid, platform, commit_hash, message
FROM updates
JOIN branches ON updates.branch_id = branches.id
WHERE branches.app_id = $2
  AND branches.name = $3
  AND updates.id = $1;

-- name: StoreUpdateUUID :execresult
UPDATE updates
SET update_uuid = $2
WHERE updates.id = $1 AND branch_id = (
    SELECT branches.id 
    FROM branches 
    WHERE app_id = $3 
      AND name = $4
);

-- name: GetLatestUpdate :one
SELECT 
    u.id,
    u.update_uuid,
    u.branch_id,
    u.runtime_version_id,
    u.update_type,
    u.commit_hash,
    u.message,
    u.platform,
    u.created_at
FROM updates u
JOIN branches b ON u.branch_id = b.id
JOIN runtime_versions rv ON u.runtime_version_id = rv.id
WHERE b.app_id = $1
  AND b.name = $2
  AND rv.version = $3
  AND u.platform = $4
  AND u.checked_at IS NOT NULL
ORDER BY u.id DESC
LIMIT 1;

-- name: GetUpdateByBranchNameAndRuntime :one
-- app_id is load-bearing, not redundant: pk_updates is (branch_id, id), so an
-- update id is only unique per branch, and branch names are only unique per app.
-- Without the app filter the same (id, branch, runtime) triple matches another
-- tenant's row.
SELECT u.id, u.update_uuid, b.app_id, b.name AS branch_name, r.version AS runtime_version, u.update_type, u.commit_hash, u.message, u.platform, u.created_at, u.rollout_percentage, u.control_update_id
FROM updates u
INNER JOIN branches b ON u.branch_id = b.id
INNER JOIN runtime_versions r ON u.runtime_version_id = r.id
WHERE b.app_id = $1
  AND u.id = $2
  AND b.name = $3
  AND r.version = $4
LIMIT 1;

-- name: GetUpdatesMetadataByBranchName :many
SELECT u.id, rv.version AS runtime_version
FROM updates u
INNER JOIN branches b ON u.branch_id = b.id
INNER JOIN runtime_versions rv ON u.runtime_version_id = rv.id
WHERE b.name = $1 AND b.app_id = $2;

-- name: MarkUpdateAsChecked :execrows
-- Stamps the "complete and pickable" sentinel. The stamp is refused (0 rows) when it
-- would break a rollout invariant: a plain update cannot become visible while a
-- rollout is active on its (branch, rtv, platform), and a rollout cannot activate if
-- ANY other update of that target was checked in while it was uploading.
--
-- The second arm compares checked_at against the target's created_at rather than
-- comparing ids. control_update_id is captured by InsertUpdateWithRollout at
-- requestUploadUrl time, so the rollout's control is only still accurate if the branch
-- did not move during the upload. An id comparison misses the update that was ALREADY
-- uploading when the rollout started: it carries a lower id, yet it reaches checked
-- state later, and the rollout would then activate pointing its out-of-bucket cohort at
-- the update before it, leaving the one in between served to nobody.
--
-- Known and accepted limitation: both arms read checked_at, the very column a
-- concurrent stamp is writing, so neither sees an uncommitted sibling. Two stamps whose
-- statements genuinely overlap (a plain update and a rollout on the same branch, rtv AND
-- platform, landing within the same few milliseconds) can therefore both pass, leaving
-- the rollout active but invisible to serving while HasActiveRolloutUpdate still refuses
-- further publishes. Closing it needs real serialization, an advisory lock on
-- (branch, rtv, platform) around the stamp, which puts a contention point on the publish
-- path for a window this narrow. The checked_at comparison above already reduced the
-- exposure from the whole upload duration to that statement overlap.
WITH target AS (
    SELECT u.id, u.branch_id, u.runtime_version_id, u.platform, u.rollout_percentage, u.created_at
    FROM updates u
    JOIN branches b ON u.branch_id = b.id
    WHERE u.id = $1 AND b.app_id = $2 AND b.name = $3
),
updated_rows AS (
    UPDATE updates
    SET checked_at = CURRENT_TIMESTAMP
    FROM target
    WHERE updates.id = target.id
      AND updates.branch_id = target.branch_id
      AND (
        (target.rollout_percentage IS NULL AND NOT EXISTS (
            SELECT 1 FROM updates a
            WHERE a.branch_id = target.branch_id
              AND a.runtime_version_id = target.runtime_version_id
              AND a.platform = target.platform
              AND a.rollout_percentage IS NOT NULL
              AND a.checked_at IS NOT NULL
        ))
        OR
        (target.rollout_percentage IS NOT NULL AND NOT EXISTS (
            SELECT 1 FROM updates n
            WHERE n.branch_id = target.branch_id
              AND n.runtime_version_id = target.runtime_version_id
              AND n.platform = target.platform
              AND n.checked_at IS NOT NULL
              AND n.id <> target.id
              AND n.checked_at > target.created_at
        ))
      )
    RETURNING updates.runtime_version_id
)
UPDATE runtime_versions
SET updated_at = CURRENT_TIMESTAMP
WHERE id IN (SELECT runtime_version_id FROM updated_rows);

-- name: InsertUpdate :one
WITH resolved_names AS (
    SELECT 
        b.id AS resolved_branch_id,
        rv.id AS resolved_runtime_version_id,
        b.app_id,
        b.name AS branch_name,
        rv.version AS runtime_version
    FROM branches b
    INNER JOIN runtime_versions rv ON rv.app_id = b.app_id
    WHERE b.name = $2
      AND rv.version = $4
      AND b.app_id = $3
)
INSERT INTO updates (
    id,
    branch_id,
    runtime_version_id,
    update_type,
    platform,
    commit_hash,
    message,
    publish_group
) VALUES (
    $1,
    (SELECT resolved_branch_id FROM resolved_names),
    (SELECT resolved_runtime_version_id FROM resolved_names),
    $5,
    $6,
    $7,
    $8,
    $9
)
RETURNING
    id,
    platform,
    commit_hash,
    message,
    created_at,
    (SELECT app_id FROM resolved_names) AS app_id,
    (SELECT branch_name FROM resolved_names) AS branch_name,
    (SELECT runtime_version FROM resolved_names) AS runtime_version;

-- name: InsertApiKey :one
-- Returns the new key's id: the audit trail needs a stable target id that
-- matches the one revocation events carry.
INSERT INTO api_keys (app_id, name, hint, hashed_key)
VALUES ($1, $2, $3, $4)
RETURNING id;

-- name: GetApiKeysMetadataByAppID :many
SELECT id, name, hint, created_at, last_used_at
FROM api_keys
WHERE app_id = $1 AND revoked_at IS NULL
ORDER BY created_at ASC;

-- name: RevokeApiKeyByID :one
-- Returns the revoked key's name so the audit entry can carry it without a
-- separate read. Only a live key matches: re-revoking (double submit, retry)
-- must not re-stamp the historical revoked_at nor emit a second audit entry,
-- so it falls into the same no-rows not-found path as an unknown id.
UPDATE api_keys
SET revoked_at = CURRENT_TIMESTAMP
WHERE id = $1 AND app_id = $2 AND revoked_at IS NULL
RETURNING name;

-- name: GetApiKeyNameByID :one
-- The audit actor display of CLI requests: one indexed read, never a list scan.
SELECT name FROM api_keys
WHERE id = $1 AND app_id = $2;

-- name: ValidateAndTouchAuth :one
-- Returns the matched key id so the caller can enforce per-key restrictions
-- (enterprise) on top of the authentication itself.
UPDATE api_keys
SET last_used_at = CURRENT_TIMESTAMP
WHERE app_id = $1
  AND hashed_key = $2
  AND revoked_at IS NULL
RETURNING id;

-- name: InsertUser :one
INSERT INTO users (id, email, password_hash, is_admin, enabled)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, email, is_admin, enabled, created_at;

-- name: GetUserByEmail :one
SELECT * FROM users
WHERE email = $1 LIMIT 1;

-- name: GetUserByID :one
SELECT * FROM users
WHERE id = $1 LIMIT 1;

-- name: GetUsers :many
SELECT id, email, is_admin, enabled, created_at, last_connected_at FROM users
ORDER BY created_at ASC;

-- name: TouchUserLastConnectedAt :exec
UPDATE users
SET last_connected_at = CURRENT_TIMESTAMP
WHERE id = $1;

-- name: DeleteUserByID :execresult
-- Locks the admin rows first so concurrent deletes/demotions/disables
-- serialize: deleting the last remaining admin matches no row instead of
-- leaving the dashboard without any admin. Disabled admins are excluded, since
-- an account that cannot sign in is no safety net.
WITH admins AS (
    SELECT id FROM users WHERE is_admin AND enabled ORDER BY id FOR UPDATE
)
DELETE FROM users
WHERE users.id = $1
  AND (users.id NOT IN (SELECT id FROM admins) OR (SELECT COUNT(*) FROM admins) > 1);

-- name: UpdateUserPasswordByID :execresult
-- The session version moves with the password, in the same statement. A second
-- call could fail after this one committed, which would leave the sessions
-- minted under the old password alive behind the new one. Nothing else retires
-- them: the account stays enabled and keeps its admin flag, so the version is
-- the only thing that ends those sessions.
UPDATE users
SET password_hash = $2,
    session_version = session_version + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1;

-- name: UpdateUserIsAdminByID :execresult
-- Same admin-row lock as DeleteUserByID: demoting the last remaining admin
-- matches no row. Promotions ($2 true) always pass the guard but still take
-- the lock, so they serialize with concurrent demotions.
--
-- Losing the admin flag also ends the account's sessions, in this statement so
-- that the revocation cannot be skipped by a stale read or lost to a failure
-- between two calls. The CASE reads users.is_admin, which in a SET expression
-- is the value BEFORE the update: the version therefore moves only on a real
-- demotion, never on a promotion and never on an idempotent PATCH.
WITH admins AS (
    SELECT id FROM users WHERE is_admin AND enabled ORDER BY id FOR UPDATE
)
UPDATE users
SET is_admin = $2,
    session_version = session_version
        + CASE WHEN users.is_admin AND NOT $2::boolean THEN 1 ELSE 0 END,
    updated_at = CURRENT_TIMESTAMP
WHERE users.id = $1
  AND ($2::boolean
       OR users.id NOT IN (SELECT id FROM admins)
       OR (SELECT COUNT(*) FROM admins) > 1);

-- name: UpdateUserEnabledByID :execresult
-- Same admin-row lock as DeleteUserByID: disabling the last remaining enabled
-- admin matches no row, so approving/revoking accounts can never lock the
-- dashboard out. Enabling ($2 true) always passes the guard but still takes
-- the lock, so it serializes with concurrent disables.
--
-- Losing access also ends the account's sessions, same reasoning and same
-- shape as UpdateUserIsAdminByID: the version moves only when an enabled
-- account is being disabled, so approving one never signs anybody out.
WITH admins AS (
    SELECT id FROM users WHERE is_admin AND enabled ORDER BY id FOR UPDATE
)
UPDATE users
SET enabled = $2,
    session_version = session_version
        + CASE WHEN users.enabled AND NOT $2::boolean THEN 1 ELSE 0 END,
    updated_at = CURRENT_TIMESTAMP
WHERE users.id = $1
  AND ($2::boolean
       OR users.id NOT IN (SELECT id FROM admins)
       OR (SELECT COUNT(*) FROM admins) > 1);

-- name: BumpUserSessionVersion :execresult
-- Invalidates every token the account holds at once: both the per-request
-- check and the refresh path compare the JWT's sv claim to this column.
UPDATE users
SET session_version = session_version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $1;

-- name: InsertRefreshToken :exec
INSERT INTO refresh_tokens (id, user_id, family_id, expires_at)
VALUES ($1, $2, $3, $4);

-- name: ConsumeRefreshToken :one
-- Single-use claim, atomic on purpose: two requests presenting the same token
-- concurrently must not both succeed, or rotation would hand out two live
-- successors and replay detection would never fire. The loser gets no row and
-- goes look at why (see GetRefreshToken).
UPDATE refresh_tokens
SET used_at = CURRENT_TIMESTAMP, replaced_by = sqlc.arg(replaced_by)
WHERE id = sqlc.arg(id)
  AND used_at IS NULL
  AND expires_at > CURRENT_TIMESTAMP
RETURNING *;

-- name: GetRefreshToken :one
-- used_recently answers "was this token rotated within the replay grace" using
-- the DATABASE clock on both sides. Comparing used_at (stamped by
-- CURRENT_TIMESTAMP) against the application's clock would straddle two
-- machines: a database running ahead would make the window always true and
-- silently disable replay detection, one running behind would read every
-- legitimate concurrent refresh as a replay.
SELECT *,
       (used_at IS NOT NULL AND used_at > CURRENT_TIMESTAMP - sqlc.arg(replay_grace)::interval) AS used_recently
FROM refresh_tokens
WHERE id = sqlc.arg(id);

-- name: DeleteRefreshTokenFamily :exec
DELETE FROM refresh_tokens
WHERE family_id = $1;

-- name: DeleteExpiredRefreshTokensForUser :exec
-- Runs whenever the account is issued a token, which bounds the table to the
-- live tokens of accounts that still sign in, without a background job.
DELETE FROM refresh_tokens
WHERE user_id = $1
  AND expires_at <= CURRENT_TIMESTAMP;

-- name: MigrateLegacyApp :exec
INSERT INTO apps (
    id, 
    name, 
    keys_mode, 
    sealed_public_key, 
    sealed_private_key, 
    path_public_key, 
    path_private_key, 
    aws_secret_id_public, 
    aws_secret_id_private
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9
)
ON CONFLICT (id) DO UPDATE SET
    name = EXCLUDED.name,
    keys_mode = EXCLUDED.keys_mode,
    sealed_public_key = EXCLUDED.sealed_public_key,
    sealed_private_key = EXCLUDED.sealed_private_key,
    path_public_key = EXCLUDED.path_public_key,
    path_private_key = EXCLUDED.path_private_key,
    aws_secret_id_public = EXCLUDED.aws_secret_id_public,
    aws_secret_id_private = EXCLUDED.aws_secret_id_private;

-- name: MigrateLegacyChannel :exec
INSERT INTO channels (
    app_id, 
    branch_id, 
    name
) VALUES (
    $1, 
    $2, 
    $3
)
ON CONFLICT (app_id, name) DO UPDATE SET
    branch_id = EXCLUDED.branch_id;

-- name: MigrateLegacyBranch :one
INSERT INTO branches (
    app_id, 
    name
) VALUES (
    $1, 
    $2
)
ON CONFLICT (app_id, name) DO UPDATE SET
    name = EXCLUDED.name
RETURNING id;

-- name: MigrateLegacyRuntimeVersion :exec
INSERT INTO runtime_versions (
    app_id, 
    version, 
    created_at, 
    updated_at
) VALUES (
    $1, 
    $2, 
    $3, 
    $4
)
ON CONFLICT (app_id, version) DO UPDATE SET
    updated_at = EXCLUDED.updated_at;

-- name: MigrateLegacyUpdate :exec
INSERT INTO updates (
    id, 
    branch_id, 
    runtime_version_id, 
    update_type, 
    platform, 
    commit_hash, 
    message,
    checked_at,
    update_uuid,
    created_at
) VALUES (
    $1,
    (SELECT id FROM branches b WHERE b.app_id = $2 AND b.name = $3),
    (SELECT id FROM runtime_versions rv WHERE rv.app_id = $2 AND rv.version = $4),
    $5,
    $6,
    $7,
    $8,
    $9,
    $10,
    $11
)
ON CONFLICT (branch_id, id) DO UPDATE SET
    runtime_version_id = EXCLUDED.runtime_version_id,
    update_type = EXCLUDED.update_type,
    platform = EXCLUDED.platform,
    commit_hash = EXCLUDED.commit_hash,
    message = EXCLUDED.message,
    checked_at = EXCLUDED.checked_at,
    update_uuid = EXCLUDED.update_uuid,
    created_at = EXCLUDED.created_at;
-- name: GetEnterpriseLicense :one
SELECT * FROM enterprise_license
WHERE singleton;

-- name: UpsertEnterpriseLicense :one
INSERT INTO enterprise_license (singleton, license_key)
VALUES (TRUE, $1)
ON CONFLICT (singleton) DO UPDATE
SET license_key = EXCLUDED.license_key, updated_at = CURRENT_TIMESTAMP
RETURNING *;

-- name: DeleteEnterpriseLicense :exec
DELETE FROM enterprise_license;

-- name: GetSSOConfig :one
SELECT * FROM sso_config
WHERE singleton;

-- name: UpsertSSOConfig :one
INSERT INTO sso_config (singleton, issuer, client_id, sealed_client_secret, provider_name, scopes, enabled, allowed_email_domains, allowed_groups, groups_claim, trust_unverified_email, manual_user_validation)
VALUES (TRUE, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (singleton) DO UPDATE
SET issuer = EXCLUDED.issuer,
    client_id = EXCLUDED.client_id,
    sealed_client_secret = EXCLUDED.sealed_client_secret,
    provider_name = EXCLUDED.provider_name,
    scopes = EXCLUDED.scopes,
    enabled = EXCLUDED.enabled,
    allowed_email_domains = EXCLUDED.allowed_email_domains,
    allowed_groups = EXCLUDED.allowed_groups,
    groups_claim = EXCLUDED.groups_claim,
    trust_unverified_email = EXCLUDED.trust_unverified_email,
    manual_user_validation = EXCLUDED.manual_user_validation,
    updated_at = CURRENT_TIMESTAMP
RETURNING *;

-- name: DeleteSSOConfig :exec
DELETE FROM sso_config;

-- name: GetUserBySSOSubject :one
SELECT u.* FROM users u
JOIN sso_identities si ON si.user_id = u.id
WHERE si.issuer = $1 AND si.subject = $2;

-- name: InsertSSOIdentity :exec
INSERT INTO sso_identities (issuer, subject, user_id, email)
VALUES ($1, $2, $3, $4);

-- name: TouchSSOIdentityLastLogin :exec
UPDATE sso_identities
SET last_login_at = CURRENT_TIMESTAMP
WHERE issuer = $1 AND subject = $2;

-- The queries below back the Enterprise Edition per-key access restrictions
-- (ee/apikeyrestrictions). sqlc generates a single package for the whole
-- schema, so the EE feature's SQL lives here like the enterprise license
-- queries above.

-- name: GetApiKeyAccess :many
-- Enforcement read for one authenticated key on the CLI request hot path: the
-- IP allow-list, the branch-creation flag and the branch rules in one round
-- trip. A key with no rule yields a single row with a NULL pattern, which is
-- the unrestricted default.
--
-- revoked_at IS NULL is redundant with authentication, which already refuses a
-- revoked key, and it is here anyway: this is the last read before a publish is
-- authorised, so it costs nothing to make "zero rows" mean exactly what the
-- caller treats it as, a key that may no longer act.
SELECT k.allowed_ips, r.pattern, r.actions
FROM api_keys k
LEFT JOIN api_key_branch_rules r ON r.api_key_id = k.id
WHERE k.id = $1 AND k.revoked_at IS NULL;

-- name: GetApiKeyAccessByAppID :many
-- Same shape for the dashboard, over every live key of one app. Ordered so
-- the caller can fold consecutive rows into one key without a map.
SELECT k.id, k.allowed_ips, r.pattern, r.actions
FROM api_keys k
LEFT JOIN api_key_branch_rules r ON r.api_key_id = k.id
WHERE k.app_id = $1 AND k.revoked_at IS NULL
ORDER BY k.id, r.id;

-- name: UpdateApiKeyAccess :execrows
UPDATE api_keys
SET allowed_ips = $1
WHERE id = $2 AND app_id = $3 AND revoked_at IS NULL;

-- name: DeleteApiKeyBranchRules :exec
-- The rules of one key are replaced wholesale, inside the same transaction as
-- UpdateApiKeyAccess: a partial write would leave a key granting something
-- nobody asked for.
DELETE FROM api_key_branch_rules WHERE api_key_id = $1;

-- name: InsertApiKeyBranchRule :exec
INSERT INTO api_key_branch_rules (api_key_id, pattern, actions)
VALUES ($1, $2, $3);

-- name: SetBranchProtected :execrows
UPDATE branches
SET protected = $1
WHERE app_id = $2 AND name = $3;

-- name: IsBranchProtected :one
SELECT protected FROM branches
WHERE app_id = $1 AND name = $2;

-- The queries below back progressive rollouts (MIT core, control-plane mode only).

-- name: GetUpdateByUUID :one
-- App-scoped, checked-only lookup by the persistent update UUID. Backs the /assets
-- rollout fix: expo-updates sends Expo-Requested-Update-ID on every asset request, so
-- the exact update it is running can be served regardless of the rollout decision.
SELECT u.id, u.update_uuid, b.app_id, b.name AS branch_name, r.version AS runtime_version, u.update_type, u.commit_hash, u.message, u.platform, u.created_at, u.rollout_percentage, u.control_update_id
FROM updates u
INNER JOIN branches b ON u.branch_id = b.id
INNER JOIN runtime_versions r ON u.runtime_version_id = r.id
WHERE b.app_id = $1
  AND u.update_uuid = $2
  AND u.checked_at IS NOT NULL
LIMIT 1;

-- name: GetLatestUpdateWithRollout :one
-- Latest checked update for (branch, rtv, platform) plus its control, resolved through
-- the explicit control_update_id pointer (a LEFT JOIN on the composite PK, NOT a LIMIT-2
-- heuristic). Control fields are NULL when the update carries no rollout.
SELECT
    u.id,
    u.update_uuid,
    u.branch_id,
    u.runtime_version_id,
    u.update_type,
    u.commit_hash,
    u.message,
    u.platform,
    u.created_at,
    u.rollout_percentage,
    u.control_update_id,
    c.id AS control_id,
    c.created_at AS control_created_at,
    c.update_type AS control_update_type
FROM updates u
JOIN branches b ON u.branch_id = b.id
JOIN runtime_versions rv ON u.runtime_version_id = rv.id
LEFT JOIN updates c ON c.branch_id = u.branch_id AND c.id = u.control_update_id
WHERE b.app_id = $1
  AND b.name = $2
  AND rv.version = $3
  AND u.platform = $4
  AND u.checked_at IS NOT NULL
ORDER BY u.id DESC
LIMIT 1;

-- name: HasActiveRolloutUpdate :one
-- Fail-fast publish guard: reports whether (branch, rtv) already has an active
-- per-update rollout on any platform.
SELECT EXISTS (
    SELECT 1
    FROM updates u
    JOIN branches b ON u.branch_id = b.id
    JOIN runtime_versions rv ON u.runtime_version_id = rv.id
    WHERE b.app_id = $1
      AND b.name = $2
      AND rv.version = $3
      AND u.rollout_percentage IS NOT NULL
      AND u.checked_at IS NOT NULL
);

-- name: GetActiveRolloutUpdates :many
-- The active per-update rollout rows for (branch, rtv), one per platform.
SELECT u.id, u.platform, u.rollout_percentage, u.control_update_id, u.created_at
FROM updates u
JOIN branches b ON u.branch_id = b.id
JOIN runtime_versions rv ON u.runtime_version_id = rv.id
WHERE b.app_id = $1
  AND b.name = $2
  AND rv.version = $3
  AND u.rollout_percentage IS NOT NULL
  AND u.checked_at IS NOT NULL
ORDER BY u.platform ASC;

-- name: SetUpdateRolloutPercentage :execrows
-- Dashboard progression: sets the new percentage on every active rollout row for
-- (branch, rtv). The rollout_percentage < $4 guard enforces monotonic increase inside
-- the UPDATE itself so concurrent progressions cannot lower the percentage; the service
-- pre-reads only to produce a friendly 400. 0 rows means the rollout ended or was
-- progressed past $4 in a concurrent edit.
UPDATE updates
SET rollout_percentage = $4
WHERE branch_id = (SELECT branches.id FROM branches WHERE branches.app_id = $1 AND branches.name = $2)
  AND runtime_version_id = (SELECT runtime_versions.id FROM runtime_versions WHERE runtime_versions.app_id = $1 AND runtime_versions.version = $3)
  AND rollout_percentage IS NOT NULL
  AND rollout_percentage < $4
  AND checked_at IS NOT NULL;

-- name: ClearUpdateRollout :execrows
-- Ends the per-update rollout for (branch, rtv) by clearing the percentage on every
-- active row. Used by both "finish" (progress to 100) and "revert". control_update_id
-- is deliberately retained: it is the historical marker the dashboard uses to render
-- the finished-rollout state, and serving only ever reads it together with a non-NULL
-- rollout_percentage.
UPDATE updates
SET rollout_percentage = NULL
WHERE branch_id = (SELECT branches.id FROM branches WHERE branches.app_id = $1 AND branches.name = $2)
  AND runtime_version_id = (SELECT runtime_versions.id FROM runtime_versions WHERE runtime_versions.app_id = $1 AND runtime_versions.version = $3)
  AND rollout_percentage IS NOT NULL
  AND checked_at IS NOT NULL;

-- name: InsertUpdateWithRollout :one
-- Publishes an update carrying a rollout percentage. The resolved_control CTE resolves
-- the control (latest checked update of the same branch/rtv/platform) in the same
-- statement; control_id may be NULL for the first update of a branch.
WITH resolved_names AS (
    SELECT
        b.id AS resolved_branch_id,
        rv.id AS resolved_runtime_version_id,
        b.app_id,
        b.name AS branch_name,
        rv.version AS runtime_version
    FROM branches b
    INNER JOIN runtime_versions rv ON rv.app_id = b.app_id
    WHERE b.name = $2
      AND rv.version = $4
      AND b.app_id = $3
),
resolved_control AS (
    SELECT u.id AS control_id
    FROM updates u
    WHERE u.branch_id = (SELECT resolved_branch_id FROM resolved_names)
      AND u.runtime_version_id = (SELECT resolved_runtime_version_id FROM resolved_names)
      AND u.platform = $6
      AND u.checked_at IS NOT NULL
    ORDER BY u.id DESC
    LIMIT 1
)
INSERT INTO updates (
    id,
    branch_id,
    runtime_version_id,
    update_type,
    platform,
    commit_hash,
    message,
    rollout_percentage,
    control_update_id,
    publish_group
) VALUES (
    $1,
    (SELECT resolved_branch_id FROM resolved_names),
    (SELECT resolved_runtime_version_id FROM resolved_names),
    $5,
    $6,
    $7,
    $8,
    $9,
    (SELECT control_id FROM resolved_control),
    $10
)
RETURNING
    id,
    platform,
    commit_hash,
    message,
    created_at,
    rollout_percentage,
    control_update_id,
    (SELECT app_id FROM resolved_names) AS app_id,
    (SELECT branch_name FROM resolved_names) AS branch_name,
    (SELECT runtime_version FROM resolved_names) AS runtime_version;

-- name: InsertChannelRollout :execrows
-- App-scoped INSERT...SELECT that refuses an unmapped channel (branch_id IS NULL) and a
-- rollout branch equal to the channel's current default. 0 rows inserted => the service
-- disambiguates (404 unknown channel / 400 unmapped or same branch). 23505 on channel_id
-- => 409 already active.
INSERT INTO channel_rollouts (id, channel_id, rollout_branch_id, percentage)
SELECT $1, c.id, rb.id, $2
FROM channels c
JOIN branches rb ON rb.app_id = c.app_id AND rb.name = $5
WHERE c.app_id = $3
  AND c.name = $4
  AND c.branch_id IS NOT NULL
  AND rb.id <> c.branch_id;

-- name: GetChannelRollout :one
SELECT cr.id, cr.channel_id, ch.name AS channel_name,
    db.name AS default_branch_name,
    rb.name AS rollout_branch_name,
    cr.percentage, cr.created_at, cr.updated_at
FROM channel_rollouts cr
JOIN channels ch ON cr.channel_id = ch.id
JOIN branches db ON ch.branch_id = db.id
JOIN branches rb ON cr.rollout_branch_id = rb.id
WHERE ch.app_id = $1 AND ch.name = $2;

-- name: UpdateChannelRolloutPercentage :execrows
UPDATE channel_rollouts
SET percentage = $1, updated_at = CURRENT_TIMESTAMP
WHERE channel_id = (SELECT id FROM channels WHERE app_id = $2 AND name = $3);

-- name: DeleteChannelRollout :execrows
DELETE FROM channel_rollouts
WHERE channel_id = (SELECT id FROM channels WHERE app_id = $1 AND name = $2);

-- name: RepointChannelToRolloutBranch :execrows
-- Promote step: repoints the channel to its rollout branch. Runs with DeleteChannelRollout
-- inside a single transaction (Engine.WithTx). Not blocked by UpdateChannelBranchMapping's
-- rollout guard because it is a distinct statement.
UPDATE channels
SET branch_id = (
    SELECT rollout_branch_id FROM channel_rollouts WHERE channel_id = channels.id
)
WHERE app_id = $1 AND name = $2
  AND EXISTS (SELECT 1 FROM channel_rollouts WHERE channel_id = channels.id);

-- name: GetChannelRolloutsByBranch :many
-- Branch-delete guard: the channels whose active rollout serves this branch. FK RESTRICT
-- already blocks the delete; this yields the friendly channel list for the error message.
SELECT ch.name AS channel_name
FROM channel_rollouts cr
JOIN channels ch ON cr.channel_id = ch.id
WHERE cr.rollout_branch_id = (SELECT branches.id FROM branches WHERE branches.app_id = $1 AND branches.name = $2);

-- Enterprise user roles & per-app grants (ee/rbac)

-- name: InsertRole :one
INSERT INTO roles (id, name, permissions)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetRoleByID :one
SELECT * FROM roles
WHERE id = $1 LIMIT 1;

-- name: ListRoles :many
SELECT * FROM roles
ORDER BY name ASC;

-- name: UpdateRole :execresult
UPDATE roles
SET name = $2, permissions = $3, updated_at = CURRENT_TIMESTAMP
WHERE id = $1;

-- name: DeleteRole :execresult
-- The FK from user_app_grants is ON DELETE RESTRICT: deleting a role that is
-- still assigned fails with a foreign-key violation the store maps to a
-- friendly "role in use" error.
DELETE FROM roles
WHERE id = $1;

-- name: ListUserAppGrants :many
-- The member's grants with their role resolved, one row per granted app.
SELECT g.user_id, g.app_id, g.role_id, g.extra_permissions,
       r.name AS role_name, r.permissions AS role_permissions
FROM user_app_grants g
LEFT JOIN roles r ON r.id = g.role_id
WHERE g.user_id = $1
ORDER BY g.app_id ASC;

-- name: GetUserAppGrant :one
-- The enforcement read behind every member mutation: the grant row for one
-- (user, app) pair with the role's permissions resolved.
SELECT g.user_id, g.app_id, g.role_id, g.extra_permissions,
       r.permissions AS role_permissions
FROM user_app_grants g
LEFT JOIN roles r ON r.id = g.role_id
WHERE g.user_id = $1 AND g.app_id = $2
LIMIT 1;

-- name: ListAccessibleAppIDs :many
SELECT app_id FROM user_app_grants
WHERE user_id = $1;

-- name: DeleteUserAppGrantsByUser :exec
-- Grants are replaced wholesale (delete + insert in one transaction).
DELETE FROM user_app_grants
WHERE user_id = $1;

-- name: InsertUserAppGrant :exec
INSERT INTO user_app_grants (user_id, app_id, role_id, extra_permissions)
VALUES ($1, $2, $3, $4);

-- name: CountGrantsPerUser :many
-- Backs the Users page warning: members with zero grants see an empty
-- dashboard, admins should notice at a glance.
SELECT user_id, COUNT(*) AS grant_count
FROM user_app_grants
GROUP BY user_id;

-- Enterprise audit log (ee/audit)

-- name: InsertAuditLogEvent :one
-- occurred_at is always the database's clock (column default), never a
-- caller-supplied time: one clock orders the whole log.
INSERT INTO audit_log_events (actor_type, actor_id, actor_display, action,
                              target_type, target_id, target_display, app_id,
                              outcome, ip, user_agent, metadata)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING *;

-- name: ListAuditLogEvents :many
-- The viewer read: newest first, keyset-paginated on id (insert order, so no
-- tie-breaking column is needed), every filter optional. before_id is the
-- cursor: NULL on the first page, then the last id of the previous page.
SELECT * FROM audit_log_events
WHERE (sqlc.narg('actor_id')::TEXT IS NULL OR actor_id = sqlc.narg('actor_id'))
  AND (sqlc.narg('action')::TEXT IS NULL OR action = sqlc.narg('action'))
  AND (sqlc.narg('app_id')::TEXT IS NULL OR app_id = sqlc.narg('app_id'))
  AND (sqlc.narg('outcome')::TEXT IS NULL OR outcome = sqlc.narg('outcome'))
  AND (sqlc.narg('occurred_from')::TIMESTAMPTZ IS NULL OR occurred_at >= sqlc.narg('occurred_from'))
  AND (sqlc.narg('occurred_to')::TIMESTAMPTZ IS NULL OR occurred_at <= sqlc.narg('occurred_to'))
  AND (sqlc.narg('before_id')::BIGINT IS NULL OR id < sqlc.narg('before_id'))
ORDER BY id DESC
LIMIT sqlc.arg('row_limit');

-- name: ListAuditLogEventsAfter :many
-- The archive exporter's batch read: strictly after the cursor, oldest first.
-- The 30-second visibility lag closes a loss window: BIGSERIAL ids are drawn
-- in execution order but rows become visible in commit order, so without the
-- lag the exporter could read id N+1, advance the cursor past a still
-- uncommitted id N, and never see N again. Inserts live at most 5 seconds
-- (recordTimeout in ee/audit), so a row stamped 30 seconds ago (occurred_at
-- and now() share the database clock) is committed or gone for good.
SELECT * FROM audit_log_events
WHERE id > $1
  AND occurred_at < now() - INTERVAL '30 seconds'
ORDER BY id ASC
LIMIT $2;

-- name: GetAuditExportCursor :one
SELECT last_exported_id FROM audit_export_state WHERE id;

-- name: AdvanceAuditExportCursor :execresult
-- Optimistic compare-and-swap: 0 rows means another replica advanced first
-- and this batch must be abandoned (its file was an idempotent overwrite).
UPDATE audit_export_state
SET last_exported_id = $2
WHERE id AND last_exported_id = $1;

-- name: PurgeAuditLogEventsBefore :execresult
-- The retention purge, the audit table's single mutation besides inserts.
DELETE FROM audit_log_events
WHERE occurred_at < $1;

-- name: PurgeExportedAuditLogEventsBefore :execresult
-- The retention purge while archiving is enabled: an expired row that the
-- exporter has not archived yet is kept until it is, so "purged rows live on
-- in the archive" holds even when the purge races a large export backlog.
DELETE FROM audit_log_events
WHERE occurred_at < $1
  AND id <= (SELECT last_exported_id FROM audit_export_state);

-- name: CountAuditLogEvents :one
-- Same filters as ListAuditLogEvents minus the cursor: the total the viewer
-- shows next to the paginated list.
SELECT COUNT(*) FROM audit_log_events
WHERE (sqlc.narg('actor_id')::TEXT IS NULL OR actor_id = sqlc.narg('actor_id'))
  AND (sqlc.narg('action')::TEXT IS NULL OR action = sqlc.narg('action'))
  AND (sqlc.narg('app_id')::TEXT IS NULL OR app_id = sqlc.narg('app_id'))
  AND (sqlc.narg('outcome')::TEXT IS NULL OR outcome = sqlc.narg('outcome'))
  AND (sqlc.narg('occurred_from')::TIMESTAMPTZ IS NULL OR occurred_at >= sqlc.narg('occurred_from'))
  AND (sqlc.narg('occurred_to')::TIMESTAMPTZ IS NULL OR occurred_at <= sqlc.narg('occurred_to'));

-- ============================================================
-- Identity (ee/identity)
-- ============================================================

-- name: ListIdentitySchemaKeys :many
SELECT * FROM identity_schema
WHERE app_id = $1
ORDER BY key ASC;

-- name: UpsertIdentitySchemaKey :one
INSERT INTO identity_schema (app_id, key, value_type, max_length)
VALUES ($1, $2, $3, $4)
ON CONFLICT (app_id, key) DO UPDATE SET
    value_type = EXCLUDED.value_type,
    max_length = EXCLUDED.max_length
RETURNING *;

-- name: DeleteIdentitySchemaKey :execresult
DELETE FROM identity_schema
WHERE app_id = $1 AND key = $2;

-- Wipes the autocomplete entries of a key when it leaves the allowlist, so
-- searchMetadata never suggests values of a key the operator removed. The
-- values already merged into device_identity.metadata are left in place.
-- name: DeleteIdentityValueStatsForKey :exec
DELETE FROM identity_value_stats
WHERE app_id = $1 AND key = $2;

-- Creates the row if this install was never seen. Split from the update on
-- purpose: FOR UPDATE cannot lock a row that does not exist yet, so two
-- concurrent first identifies of the same install would both merge against
-- an empty map and one would silently win. Insert-then-lock serializes them.
-- name: EnsureDeviceIdentity :exec
INSERT INTO device_identity (app_id, eas_client_id)
VALUES ($1, $2)
ON CONFLICT (app_id, eas_client_id) DO NOTHING;

-- name: GetDeviceIdentityForUpdate :one
SELECT * FROM device_identity
WHERE app_id = $1 AND eas_client_id = $2
FOR UPDATE;

-- name: GetDeviceIdentity :one
SELECT * FROM device_identity
WHERE app_id = $1 AND eas_client_id = $2;

-- The merge write of an identity op. Same COALESCE rule as TouchDeviceIdentity:
-- a NULL geo argument means "this operation did not resolve one", and must
-- leave whatever an earlier batch established rather than blank it.
-- name: UpdateDeviceIdentity :one
UPDATE device_identity SET
    metadata = $3,
    country_code = COALESCE(sqlc.narg('country_code'), country_code),
    city = COALESCE(sqlc.narg('city'), city),
    lat = COALESCE(sqlc.narg('lat'), lat),
    lng = COALESCE(sqlc.narg('lng'), lng),
    last_seen_at = CURRENT_TIMESTAMP
WHERE app_id = $1 AND eas_client_id = $2
RETURNING *;

-- Per-value device counts, kept in step with the merges that produce them and
-- read back by autocomplete. The decrement floors at zero instead of deleting
-- the row, because the delete would have to run inside the same lock ordering
-- as the merge to be safe, and a row at zero is harmless: the third statement
-- sweeps it afterwards, outside the hot path. A count that drifted below zero
-- would be unrecoverable, one that lingers at zero is not.
-- ONE statement for the whole mutation, increments and decrements together,
-- and that is not a detail: the ordering these rows are locked in is what
-- keeps two devices sharing a stat row (same tenant, same plan) from
-- deadlocking, and it has to hold across ALL of a transaction's ops, not
-- within each direction. Splitting into "every increment, then every
-- decrement" would let A lock acme then globex while B locks globex then
-- acme, which is the deadlock the caller's sort exists to prevent.
--
-- ORDER BY inside the SELECT is therefore load-bearing: rows are processed,
-- and their conflicts locked, in that order. The caller sorts too, so the two
-- agree.
--
-- The WHERE keeps a decrement of a row that does not exist a no-op, which is
-- what the plain UPDATE did before: the EXISTS reads without locking, so it
-- adds nothing to the ordering. And the conflict arm floors at zero, because a
-- count that drifted below zero would be unrecoverable while one lingering at
-- zero is swept by the statement below.
-- name: ApplyIdentityValueStats :exec
INSERT INTO identity_value_stats (app_id, key, value, device_count)
SELECT $1, t.key, t.value, t.delta
FROM (
    SELECT unnest(sqlc.arg(keys)::TEXT[])   AS key,
           unnest(sqlc.arg(values)::TEXT[]) AS value,
           unnest(sqlc.arg(deltas)::INT[])  AS delta
) AS t
WHERE t.delta > 0
   OR EXISTS (
       SELECT 1 FROM identity_value_stats s
       WHERE s.app_id = $1 AND s.key = t.key AND s.value = t.value
   )
ORDER BY t.key, t.value
ON CONFLICT (app_id, key, value) DO UPDATE SET
    device_count = GREATEST(identity_value_stats.device_count + EXCLUDED.device_count, 0),
    last_seen_at = CURRENT_TIMESTAMP;

-- Sweeps the rows the statement above left at zero, in one pass over the pairs
-- it touched. Same rows, already locked by it, so this introduces no new
-- ordering.
-- name: DeleteZeroIdentityValueStats :exec
DELETE FROM identity_value_stats
WHERE app_id = $1
  AND device_count <= 0
  AND (key, value) IN (
      SELECT unnest(sqlc.arg(keys)::TEXT[]), unnest(sqlc.arg(values)::TEXT[])
  );

-- Autocomplete, empty-search arm: top values of a key by device count.
-- Deliberately a separate query from SearchIdentityValues: an OR'd
-- "search = '' OR value ILIKE ..." arm makes the statement un-indexable under
-- a generic plan (pgx prepares statements), forcing a seq scan of every
-- distinct value. Split, each arm gets its own stable plan: this one is an
-- index-only scan on (app_id, key, device_count DESC, value ASC).
-- name: TopIdentityValues :many
SELECT value, device_count FROM identity_value_stats
WHERE app_id = $1 AND key = $2
ORDER BY device_count DESC, value ASC
LIMIT sqlc.arg(max_results)::INT;

-- Autocomplete, substring arm: case-insensitive containment served by the
-- trigram index. % and _ in the search act as SQL wildcards; harmless for
-- autocomplete, so they are not escaped.
-- name: SearchIdentityValues :many
SELECT value, device_count FROM identity_value_stats
WHERE app_id = $1 AND key = $2
  AND value ILIKE '%' || sqlc.arg(search)::TEXT || '%'
ORDER BY device_count DESC, value ASC
LIMIT sqlc.arg(max_results)::INT;

-- The current Identity cohort is projected into ClickHouse queries as an
-- external table. The JSONB containment predicate is served by the metadata
-- GIN index and keeps mutable identity data out of the analytics store.
--
-- One containment document per requested value, so "plan is pro or
-- enterprise" is a single index scan: GIN honours @> ANY(array).
--
-- Bounded, because the whole result is materialized in memory and then copied
-- onto the wire as an external table, on every Observe request. A broad
-- filter over a large fleet is otherwise millions of UUIDs per request. The
-- caller fetches one extra row to tell "exactly at the cap" from "truncated"
-- and says so in the response rather than silently answering for a subset.
-- name: ListObserveCohortDeviceIDs :many
SELECT eas_client_id FROM device_identity
WHERE app_id = $1
  AND last_seen_at >= sqlc.arg(active_since)::timestamptz
  AND metadata @> ANY(sqlc.arg(filters)::jsonb[])
ORDER BY last_seen_at DESC, eas_client_id DESC
LIMIT sqlc.arg('lim')::int;

-- City centroids are intentionally grouped before crossing the API boundary:
-- GeoLite2 coordinates identify a city, not an exact device position.
--
-- Two callers, one shape. With the period as active_since this is the map's
-- static layer ("installs per city"); with the last few seconds it is the
-- map's live feed ("check-ins per city since the last poll"). Aggregating is
-- what keeps the live feed affordable: the row count is bounded by geography,
-- so a 5M device fleet costs the same payload as a 5k one, and the LIMIT
-- degrades by dropping the quietest cities rather than by growing.
--
-- The same FILTER predicates as ListDevices, deliberately: the map is a view of
-- the inventory, so narrowing the page has to narrow the map too. A globe that
-- keeps showing the whole fleet while the tables next to it show a branch is a
-- globe that lies. Only the window differs: here last_seen_at bounds the
-- period, there the keyset bounds the page.
--
-- This used to be two queries. The release dimensions lived on the update, so
-- filtering on one meant three joins, and the planner cannot skip a join it
-- might need: carrying them unconditionally cost 4x (330ms -> 1.4s) on the
-- query that runs on every page load, filtered or not. The workaround was a
-- join-free twin picked at runtime. Now that the dimensions sit on the device
-- there is one query again, and it is the cheap one in every case.
-- name: ListObserveLocations :many
SELECT d.country_code, d.city, d.lat, d.lng, COUNT(*) AS device_count
FROM device_identity d
WHERE d.app_id = $1
  AND d.last_seen_at >= sqlc.arg(active_since)::timestamptz
  AND d.lat IS NOT NULL
  AND d.lng IS NOT NULL
  AND (coalesce(cardinality(sqlc.arg('filters')::jsonb[]), 0) = 0 OR d.metadata @> ANY(sqlc.arg('filters')::jsonb[]))
  AND (coalesce(cardinality(sqlc.arg('eas_client_id')::uuid[]), 0) = 0 OR d.eas_client_id = ANY(sqlc.arg('eas_client_id')::uuid[]))
  AND (coalesce(cardinality(sqlc.arg('current_update_id')::uuid[]), 0) = 0 OR d.current_update_id = ANY(sqlc.arg('current_update_id')::uuid[]))
  AND (coalesce(cardinality(sqlc.arg('publish_group')::uuid[]), 0) = 0 OR d.publish_group = ANY(sqlc.arg('publish_group')::uuid[]))
  AND (coalesce(cardinality(sqlc.arg('device_model')::text[]), 0) = 0 OR d.device_model = ANY(sqlc.arg('device_model')::text[]))
  AND (coalesce(cardinality(sqlc.arg('os_name')::text[]), 0) = 0 OR d.os_name = ANY(sqlc.arg('os_name')::text[]))
  AND (coalesce(cardinality(sqlc.arg('os_version')::text[]), 0) = 0 OR d.os_version = ANY(sqlc.arg('os_version')::text[]))
  AND (coalesce(cardinality(sqlc.arg('country_code')::text[]), 0) = 0 OR d.country_code = ANY(sqlc.arg('country_code')::text[]))
  AND (coalesce(cardinality(sqlc.arg('branch')::text[]), 0) = 0 OR d.branch_name = ANY(sqlc.arg('branch')::text[]))
  AND (coalesce(cardinality(sqlc.arg('runtime_version')::text[]), 0) = 0 OR d.runtime_version = ANY(sqlc.arg('runtime_version')::text[]))
  AND (coalesce(cardinality(sqlc.arg('platform')::text[]), 0) = 0 OR d.platform = ANY(sqlc.arg('platform')::text[]))
GROUP BY d.country_code, d.city, d.lat, d.lng
ORDER BY device_count DESC, d.country_code ASC NULLS LAST, d.city ASC NULLS LAST
LIMIT 500;

-- Real-time active installs for Observe. Unlike telemetry aggregates this
-- stays available when ClickHouse is disabled.
--
-- Same filter predicates as ListObserveLocations, for the same reason: this
-- number sits next to the map and the tables on one filtered page. Counting the
-- whole fleet while the page is narrowed to a branch is the same lie the map
-- would tell.
-- name: CountObserveActiveDevices :one
SELECT COUNT(*)
FROM device_identity d
WHERE d.app_id = $1
  AND d.last_seen_at >= sqlc.arg(active_since)::timestamptz
  AND (coalesce(cardinality(sqlc.arg('filters')::jsonb[]), 0) = 0 OR d.metadata @> ANY(sqlc.arg('filters')::jsonb[]))
  AND (coalesce(cardinality(sqlc.arg('eas_client_id')::uuid[]), 0) = 0 OR d.eas_client_id = ANY(sqlc.arg('eas_client_id')::uuid[]))
  AND (coalesce(cardinality(sqlc.arg('current_update_id')::uuid[]), 0) = 0 OR d.current_update_id = ANY(sqlc.arg('current_update_id')::uuid[]))
  AND (coalesce(cardinality(sqlc.arg('publish_group')::uuid[]), 0) = 0 OR d.publish_group = ANY(sqlc.arg('publish_group')::uuid[]))
  AND (coalesce(cardinality(sqlc.arg('device_model')::text[]), 0) = 0 OR d.device_model = ANY(sqlc.arg('device_model')::text[]))
  AND (coalesce(cardinality(sqlc.arg('os_name')::text[]), 0) = 0 OR d.os_name = ANY(sqlc.arg('os_name')::text[]))
  AND (coalesce(cardinality(sqlc.arg('os_version')::text[]), 0) = 0 OR d.os_version = ANY(sqlc.arg('os_version')::text[]))
  AND (coalesce(cardinality(sqlc.arg('country_code')::text[]), 0) = 0 OR d.country_code = ANY(sqlc.arg('country_code')::text[]))
  AND (coalesce(cardinality(sqlc.arg('branch')::text[]), 0) = 0 OR d.branch_name = ANY(sqlc.arg('branch')::text[]))
  AND (coalesce(cardinality(sqlc.arg('runtime_version')::text[]), 0) = 0 OR d.runtime_version = ANY(sqlc.arg('runtime_version')::text[]))
  AND (coalesce(cardinality(sqlc.arg('platform')::text[]), 0) = 0 OR d.platform = ANY(sqlc.arg('platform')::text[]));

-- Resolve an EAS publish group to the concrete update UUIDs stored on
-- telemetry rows. A publish group can contain one update per platform.
-- name: ListObserveUpdateUUIDsByPublishGroup :many
SELECT u.update_uuid
FROM updates u
JOIN branches b ON b.id = u.branch_id
WHERE b.app_id = $1
  AND u.publish_group = $2
  AND u.update_uuid IS NOT NULL
  AND u.checked_at IS NOT NULL
ORDER BY u.id;

-- Device inventory for the dashboard, with the release dimensions of whatever
-- update each device currently runs: newest-seen first, keyset-paginated on
-- (last_seen_at DESC, eas_client_id DESC) so deep pages stay cheap. The
-- optional jsonb filters (metadata @> ANY, served by the GIN index) power
-- "devices for a userId / tenant". Fetch one extra row to detect the next page.
--
-- branch, runtime version, platform and publish group are properties of the
-- update and never change, so they are stored on the device at check-in rather
-- than joined back here (20260726120000_device_release_columns.sql). The
-- app-scoping guard that used to live in this query, as an EXISTS on the
-- updates join, moved to the write with them: a device of app A claiming an
-- update of app B resolves to nothing there and reads NULL here.
--
-- NULL, deliberately: a device on the embedded bundle, or on an update this
-- server does not know, still appears in the unfiltered inventory. Filtering on
-- a release dimension does exclude it, which is the honest answer to "show me
-- devices on branch X".
-- name: ListDevices :many
SELECT d.*
FROM device_identity d
WHERE d.app_id = $1
  AND (coalesce(cardinality(sqlc.arg('filters')::jsonb[]), 0) = 0 OR d.metadata @> ANY(sqlc.arg('filters')::jsonb[]))
  AND (coalesce(cardinality(sqlc.arg('eas_client_id')::uuid[]), 0) = 0 OR d.eas_client_id = ANY(sqlc.arg('eas_client_id')::uuid[]))
  AND (coalesce(cardinality(sqlc.arg('current_update_id')::uuid[]), 0) = 0 OR d.current_update_id = ANY(sqlc.arg('current_update_id')::uuid[]))
  AND (coalesce(cardinality(sqlc.arg('publish_group')::uuid[]), 0) = 0 OR d.publish_group = ANY(sqlc.arg('publish_group')::uuid[]))
  AND (coalesce(cardinality(sqlc.arg('device_model')::text[]), 0) = 0 OR d.device_model = ANY(sqlc.arg('device_model')::text[]))
  AND (coalesce(cardinality(sqlc.arg('os_name')::text[]), 0) = 0 OR d.os_name = ANY(sqlc.arg('os_name')::text[]))
  AND (coalesce(cardinality(sqlc.arg('os_version')::text[]), 0) = 0 OR d.os_version = ANY(sqlc.arg('os_version')::text[]))
  AND (coalesce(cardinality(sqlc.arg('country_code')::text[]), 0) = 0 OR d.country_code = ANY(sqlc.arg('country_code')::text[]))
  AND (coalesce(cardinality(sqlc.arg('branch')::text[]), 0) = 0 OR d.branch_name = ANY(sqlc.arg('branch')::text[]))
  AND (coalesce(cardinality(sqlc.arg('runtime_version')::text[]), 0) = 0 OR d.runtime_version = ANY(sqlc.arg('runtime_version')::text[]))
  AND (coalesce(cardinality(sqlc.arg('platform')::text[]), 0) = 0 OR d.platform = ANY(sqlc.arg('platform')::text[]))
  AND (
    sqlc.narg('before_last_seen')::timestamptz IS NULL
    OR d.last_seen_at < sqlc.narg('before_last_seen')::timestamptz
    OR (d.last_seen_at = sqlc.narg('before_last_seen')::timestamptz
        AND d.eas_client_id < sqlc.narg('before_client_id')::uuid)
  )
ORDER BY d.last_seen_at DESC, d.eas_client_id DESC
LIMIT sqlc.arg('lim')::int;

-- Devices that pinged in the last N minutes, whatever the ping: a manifest
-- poll, a metrics batch or a log batch all bump last_seen_at, so this is
-- "currently live", not "currently sending telemetry". Served by
-- idx_device_identity_last_seen. The check-in debounce means last_seen_at is
-- accurate to within a minute, which is invisible at a 20 minute window.
--
-- Filtered on the same dimensions as ListDevices, so the count sits next to a
-- filtered inventory without contradicting it, and now by the same means: every
-- dimension is a plain predicate on device_identity.
--
-- This query used to reach the release dimensions through a subquery over
-- updates, hashed once, because the three-way join ListDevices carried was
-- unaffordable on a whole-fleet count. Measured then on 2.4M online devices,
-- 20 minute window: 586 ms unfiltered and 1 025 ms with a branch filter using
-- the joins, against 45 ms and 112 ms using the subquery. With the dimensions
-- stored on the device the subquery is gone too, along with the two traps it
-- carried (the leading cardinality check that kept "no release filter" from
-- meaning "only updates I know", and the ban on correlating it to the outer
-- row, which cost 9 seconds when someone tried).
-- name: CountOnlineDevices :one
SELECT count(*)
FROM device_identity d
WHERE d.app_id = $1
  AND d.last_seen_at > sqlc.arg('since')::timestamptz
  AND (coalesce(cardinality(sqlc.arg('filters')::jsonb[]), 0) = 0 OR d.metadata @> ANY(sqlc.arg('filters')::jsonb[]))
  AND (coalesce(cardinality(sqlc.arg('eas_client_id')::uuid[]), 0) = 0 OR d.eas_client_id = ANY(sqlc.arg('eas_client_id')::uuid[]))
  AND (coalesce(cardinality(sqlc.arg('current_update_id')::uuid[]), 0) = 0 OR d.current_update_id = ANY(sqlc.arg('current_update_id')::uuid[]))
  AND (coalesce(cardinality(sqlc.arg('publish_group')::uuid[]), 0) = 0 OR d.publish_group = ANY(sqlc.arg('publish_group')::uuid[]))
  AND (coalesce(cardinality(sqlc.arg('device_model')::text[]), 0) = 0 OR d.device_model = ANY(sqlc.arg('device_model')::text[]))
  AND (coalesce(cardinality(sqlc.arg('os_name')::text[]), 0) = 0 OR d.os_name = ANY(sqlc.arg('os_name')::text[]))
  AND (coalesce(cardinality(sqlc.arg('os_version')::text[]), 0) = 0 OR d.os_version = ANY(sqlc.arg('os_version')::text[]))
  AND (coalesce(cardinality(sqlc.arg('country_code')::text[]), 0) = 0 OR d.country_code = ANY(sqlc.arg('country_code')::text[]))
  AND (coalesce(cardinality(sqlc.arg('branch')::text[]), 0) = 0 OR d.branch_name = ANY(sqlc.arg('branch')::text[]))
  AND (coalesce(cardinality(sqlc.arg('runtime_version')::text[]), 0) = 0 OR d.runtime_version = ANY(sqlc.arg('runtime_version')::text[]))
  AND (coalesce(cardinality(sqlc.arg('platform')::text[]), 0) = 0 OR d.platform = ANY(sqlc.arg('platform')::text[]));

-- Same lookup, one row: the flattener needs both the branch and the publish
-- group of an update, and one round trip beats two. publish_group is NULL for
-- updates from older CLIs and for rollback markers, which is why the caller
-- treats it as optional rather than as a missing row.
-- name: GetUpdateOriginByUUID :one
SELECT b.name AS branch_name, u.publish_group
FROM updates u
INNER JOIN branches b ON u.branch_id = b.id
WHERE b.app_id = $1 AND u.update_uuid = $2
LIMIT 1;

-- Passive-contact bump (manifest poll, telemetry batch): refresh last_seen and
-- opportunistically enrich geo, never touching metadata. 1 row = known device;
-- 0 = brand new, the caller registers it.
--
-- The release dimensions are resolved here rather than joined at read time
-- (see 20260726120000_device_release_columns.sql). The CTE is the app-scoping
-- guard the reads used to carry as an EXISTS: it joins branches on the caller's
-- app id, so an update belonging to another app resolves to no row and the
-- columns land NULL, exactly as the old LEFT JOIN produced.
-- name: TouchDeviceIdentity :execrows
WITH origin AS (
    SELECT u.update_uuid,
           b.name AS branch_name,
           rv.version AS runtime_version,
           u.platform,
           u.publish_group
    FROM updates u
    INNER JOIN branches b ON b.id = u.branch_id AND b.app_id = $1
    LEFT JOIN runtime_versions rv ON rv.id = u.runtime_version_id
    WHERE u.update_uuid = sqlc.narg('current_update_id')::uuid
    LIMIT 1
)
UPDATE device_identity SET
    country_code = COALESCE(sqlc.narg('country_code'), device_identity.country_code),
    city = COALESCE(sqlc.narg('city'), device_identity.city),
    lat = COALESCE(sqlc.narg('lat'), device_identity.lat),
    lng = COALESCE(sqlc.narg('lng'), device_identity.lng),
    -- The RESOLVED update, never the id as it arrived. The header is
    -- unauthenticated, so a device can name an update that does not exist or
    -- belongs to another app, and writing it raw put a value the server never
    -- published into an analytical column. Worse, the outbox trigger fires on
    -- every change of this column, so alternating invented ids minted one
    -- permanent adoption event per request. Unresolved now lands NULL, which
    -- the trigger ignores (it only enqueues a non-NULL update).
    --
    -- The cost is that a device back on its embedded bundle reports the
    -- bundle's own id, which matches no row, so it reads as "on no known
    -- update" rather than naming the bundle. Publishing an update to a fresh
    -- app is what fills this in, and the docs say so.
    current_update_id = CASE WHEN sqlc.narg('current_update_id')::uuid IS NULL
        THEN device_identity.current_update_id ELSE (SELECT o.update_uuid FROM origin o) END,
    -- Rewritten on the same rule, so the release columns can never describe an
    -- update the id column does not hold: a manifest poll carrying no header
    -- keeps what the last one established.
    branch_name = CASE WHEN sqlc.narg('current_update_id')::uuid IS NULL
        THEN device_identity.branch_name ELSE (SELECT o.branch_name FROM origin o) END,
    runtime_version = CASE WHEN sqlc.narg('current_update_id')::uuid IS NULL
        THEN device_identity.runtime_version ELSE (SELECT o.runtime_version FROM origin o) END,
    platform = CASE WHEN sqlc.narg('current_update_id')::uuid IS NULL
        THEN device_identity.platform ELSE (SELECT o.platform FROM origin o) END,
    publish_group = CASE WHEN sqlc.narg('current_update_id')::uuid IS NULL
        THEN device_identity.publish_group ELSE (SELECT o.publish_group FROM origin o) END,
    -- Only telemetry knows the hardware; a manifest poll passes NULL here and
    -- must never blank what a previous batch established.
    device_model = COALESCE(sqlc.narg('device_model'), device_identity.device_model),
    os_name = COALESCE(sqlc.narg('os_name'), device_identity.os_name),
    os_version = COALESCE(sqlc.narg('os_version'), device_identity.os_version),
    app_version = COALESCE(sqlc.narg('app_version'), device_identity.app_version),
    current_update_observed_at = CASE WHEN sqlc.narg('current_update_id')::uuid IS NULL
        THEN device_identity.current_update_observed_at ELSE sqlc.arg('observed_at')::timestamptz END,
    -- Moved onto, not heard from. The watermark above advances on every poll,
    -- because that is what makes it able to order racing observations; this one
    -- stands still for as long as the device stays where it is, which is what
    -- lets a chart date an adoption.
    current_update_arrived_at = CASE
        WHEN sqlc.narg('current_update_id')::uuid IS NULL
            THEN device_identity.current_update_arrived_at
        WHEN device_identity.current_update_id IS DISTINCT FROM (SELECT o.update_uuid FROM origin o)
            THEN sqlc.arg('observed_at')::timestamptz
        -- Unchanged means unchanged, including when nothing is recorded yet.
        -- Filling a NULL here with the current instant would have dated the
        -- whole pre-existing fleet at the deploy that introduced the column,
        -- within one debounce window, and drawn a vertical cliff there on any
        -- window spanning it. It buys nothing either way: a NULL and a stamp
        -- read identically on every window that starts after the deploy, since
        -- both fold into the first bucket, and on the windows where they differ
        -- the NULL is the honest one.
        ELSE device_identity.current_update_arrived_at
    END,
    last_seen_at = CURRENT_TIMESTAMP
WHERE device_identity.app_id = $1 AND device_identity.eas_client_id = $2
  -- An observation older than the one on file changes nothing, not even
  -- last_seen_at: a device that took an update while offline then flushes the
  -- telemetry it recorded BEFORE the switch races the manifest poll announcing
  -- the new one, and whichever lands last used to win. The guard belongs in
  -- the WHERE and not in each CASE above: PostgreSQL re-evaluates it against
  -- the freshly written row when a concurrent UPDATE releases the row lock,
  -- while a CTE or a self-join would still be reading the snapshot both racers
  -- started from and would let them both through.
  --
  -- A check-in naming no update passes unconditionally: it says nothing about
  -- which update runs, so it has nothing to be stale about.
  AND (sqlc.narg('current_update_id')::uuid IS NULL
       OR device_identity.current_update_observed_at IS NULL
       OR sqlc.arg('observed_at')::timestamptz >= device_identity.current_update_observed_at);

-- Registration upsert for the passive path: the registry is uncapped (the
-- whole fleet is the update-health source of truth). ON CONFLICT absorbs the
-- race with a concurrent registration of the same device.
-- name: RegisterDevice :execrows
WITH origin AS (
    SELECT u.update_uuid,
           b.name AS branch_name,
           rv.version AS runtime_version,
           u.platform,
           u.publish_group
    FROM updates u
    INNER JOIN branches b ON b.id = u.branch_id AND b.app_id = $1
    LEFT JOIN runtime_versions rv ON rv.id = u.runtime_version_id
    WHERE u.update_uuid = sqlc.narg('current_update_id')::uuid
    LIMIT 1
)
INSERT INTO device_identity (
    app_id, eas_client_id, country_code, city, lat, lng, current_update_id,
    device_model, os_name, os_version, app_version, current_update_observed_at,
    current_update_arrived_at,
    branch_name, runtime_version, platform, publish_group
)
VALUES (
    $1, $2, sqlc.narg('country_code'), sqlc.narg('city'), sqlc.narg('lat'),
    sqlc.narg('lng'), (SELECT update_uuid FROM origin), sqlc.narg('device_model'),
    sqlc.narg('os_name'), sqlc.narg('os_version'), sqlc.narg('app_version'),
    CASE WHEN sqlc.narg('current_update_id')::uuid IS NULL
        THEN NULL ELSE sqlc.arg('observed_at')::timestamptz END,
    -- A first sighting IS an arrival.
    CASE WHEN sqlc.narg('current_update_id')::uuid IS NULL
        THEN NULL ELSE sqlc.arg('observed_at')::timestamptz END,
    (SELECT branch_name FROM origin), (SELECT runtime_version FROM origin),
    (SELECT platform FROM origin), (SELECT publish_group FROM origin)
)
-- Same rule as TouchDeviceIdentity on the conflict arm: the release columns
-- follow current_update_id, and only when this registration names one.
-- The arms below test the PARAMETER and not EXCLUDED: now that the insert
-- writes the RESOLVED update, EXCLUDED is NULL both when the check-in named no
-- update and when it named one the server does not know, and those two must
-- not behave the same. The first keeps what the row holds, the second blanks
-- it, exactly as TouchDeviceIdentity does.
ON CONFLICT (app_id, eas_client_id) DO UPDATE SET
    last_seen_at = CURRENT_TIMESTAMP,
    current_update_id = CASE WHEN sqlc.narg('current_update_id')::uuid IS NULL
        THEN device_identity.current_update_id ELSE EXCLUDED.current_update_id END,
    branch_name = CASE WHEN sqlc.narg('current_update_id')::uuid IS NULL
        THEN device_identity.branch_name ELSE EXCLUDED.branch_name END,
    runtime_version = CASE WHEN sqlc.narg('current_update_id')::uuid IS NULL
        THEN device_identity.runtime_version ELSE EXCLUDED.runtime_version END,
    platform = CASE WHEN sqlc.narg('current_update_id')::uuid IS NULL
        THEN device_identity.platform ELSE EXCLUDED.platform END,
    publish_group = CASE WHEN sqlc.narg('current_update_id')::uuid IS NULL
        THEN device_identity.publish_group ELSE EXCLUDED.publish_group END,
    device_model = COALESCE(EXCLUDED.device_model, device_identity.device_model),
    os_name = COALESCE(EXCLUDED.os_name, device_identity.os_name),
    os_version = COALESCE(EXCLUDED.os_version, device_identity.os_version),
    app_version = COALESCE(EXCLUDED.app_version, device_identity.app_version),
    current_update_observed_at = CASE WHEN sqlc.narg('current_update_id')::uuid IS NULL
        THEN device_identity.current_update_observed_at
        ELSE EXCLUDED.current_update_observed_at END,
    -- Same distinction as TouchDeviceIdentity: the arrival only moves when the
    -- update does.
    current_update_arrived_at = CASE
        WHEN sqlc.narg('current_update_id')::uuid IS NULL
            THEN device_identity.current_update_arrived_at
        WHEN device_identity.current_update_id IS DISTINCT FROM EXCLUDED.current_update_id
            THEN EXCLUDED.current_update_arrived_at
        -- Same as above: a device merely seen again keeps whatever it has,
        -- NULL included.
        ELSE device_identity.current_update_arrived_at
    END
-- Same staleness guard as TouchDeviceIdentity, on the arm that absorbs the
-- race between two concurrent registrations of the same device.
WHERE sqlc.narg('current_update_id')::uuid IS NULL
   OR device_identity.current_update_observed_at IS NULL
   OR EXCLUDED.current_update_observed_at >= device_identity.current_update_observed_at;


-- Records a manifest/native failure at server receipt time. fatal_error stays
-- capture-once.
--
-- The conflict target carries failure_type (20260726140000_failure_type_in_key.sql):
-- a runtime crash on the same pair is a different row, so a launch rollback can
-- no longer land on top of one and inherit its type.
-- name: UpsertDeviceUpdateFailure :exec
INSERT INTO device_update_failures (
    app_id, eas_client_id, update_id, failure_type, fatal_error,
    first_seen_at, last_seen_at
)
SELECT $1, $2, u.update_uuid, sqlc.arg(failure_type), sqlc.arg(fatal_error),
       sqlc.arg(occurred_at), sqlc.arg(occurred_at)
FROM updates u
JOIN branches b ON b.id = u.branch_id
WHERE b.app_id = $1
  AND u.update_uuid = $3
  AND u.checked_at IS NOT NULL
ON CONFLICT (app_id, eas_client_id, update_id, failure_type) DO UPDATE SET
    last_seen_at = GREATEST(
        device_update_failures.last_seen_at,
        EXCLUDED.last_seen_at
    ),
    fatal_error = CASE
        WHEN device_update_failures.fatal_error = '' THEN EXCLUDED.fatal_error
        ELSE device_update_failures.fatal_error
    END;

-- Runtime crash transition. The watermark upsert serializes concurrent
-- startup/crash delivery for this device+update; only the newest source event
-- can change current health.
-- name: RecordDeviceRuntimeFailure :exec
WITH runtime_state AS (
    INSERT INTO device_update_runtime_state (
        app_id, eas_client_id, update_id, last_crashed_at
    )
    SELECT $1, $2, u.update_uuid, sqlc.arg(occurred_at)
    FROM updates u
    JOIN branches b ON b.id = u.branch_id
    WHERE b.app_id = $1
      AND u.update_uuid = $3
      AND u.checked_at IS NOT NULL
    ON CONFLICT (app_id, eas_client_id, update_id) DO UPDATE SET
        last_crashed_at = GREATEST(
            device_update_runtime_state.last_crashed_at,
            EXCLUDED.last_crashed_at
        )
    RETURNING app_id, eas_client_id, update_id,
              last_started_at, last_crashed_at
)
INSERT INTO device_update_failures (
    app_id, eas_client_id, update_id, failure_type, fatal_error,
    first_seen_at, last_seen_at
)
SELECT app_id, eas_client_id, update_id, 'runtime_issue',
       sqlc.arg(fatal_error), sqlc.arg(occurred_at), sqlc.arg(occurred_at)
FROM runtime_state
WHERE last_started_at IS NULL OR last_crashed_at >= last_started_at
-- The conflict target pins failure_type to 'runtime_issue', the literal this
-- statement inserts, so the reopen below no longer needs to check the type: it
-- cannot reach a row typed by the manifest writer.
ON CONFLICT (app_id, eas_client_id, update_id, failure_type) DO UPDATE SET
    last_seen_at = GREATEST(
        device_update_failures.last_seen_at,
        EXCLUDED.last_seen_at
    ),
    resolved_at = CASE
        WHEN device_update_failures.resolved_at IS NOT NULL
         AND EXCLUDED.last_seen_at > device_update_failures.resolved_at
        THEN NULL
        ELSE device_update_failures.resolved_at
    END,
    fatal_error = CASE
        WHEN device_update_failures.fatal_error = '' THEN EXCLUDED.fatal_error
        ELSE device_update_failures.fatal_error
    END;

-- A rollback that stopped being one. Manifest failures (update_issue) had no
-- resolution path at all: a device reports the update it could not launch, and
-- that row stayed open forever, because the recovery signal the runtime path
-- uses is a successful JS startup and a native launch failure never produces
-- one. An update the whole fleet had long moved past therefore kept every
-- failure it ever collected, and since its live population was zero, its
-- health read 0% for good.
--
-- Two ways a device stops being stuck, and both are visible on the manifest
-- poll alone, with no telemetry involved.
--
-- It has moved PAST the update, onto a later one of the same branch, runtime
-- and platform. Ordered on updates.id rather than on a timestamp: two
-- platforms published in the same second would otherwise resolve each other's
-- failures. The direction is what carries the meaning. A rollback lands on an
-- OLDER update, so the failure stays open and the device stays counted as
-- stuck; an upgrade lands on a newer one, so it closes. This holds for BOTH
-- kinds of failure: a device that crashed in JS on an update it has since left
-- behind is no longer failing on it either.
--
-- Or it is running the very update it failed on, which means that update
-- launched after all. Restricted to update_issue, and the restriction is the
-- whole point: for a runtime failure, running the update IS the failing state,
-- since the device launches it and then crashes in JS. Resolving on that
-- signal would clear every JS crash on the next manifest poll. Those clear
-- through ResolveDeviceRuntimeFailure below, on a successful startup.
--
-- The consequence is worth stating: faulty devices now means "stuck on this
-- update now" rather than "failed on it once". A bad release stops being
-- visible here once the fleet has moved on, and that history lives in the
-- ClickHouse projection instead, which is the only place that can keep it.
--
-- The strict comparison on last_seen_at belongs to that second case ONLY, where
-- it settles a genuine ambiguity: the same poll saying "I run this update" and
-- "this update failed" must leave the failure open. There is no such ambiguity
-- when the device has moved past the update, and applying the guard there was
-- a bug that made the whole rule inert. expo-updates keeps listing a failed id
-- in Expo-Recent-Failed-Update-IDs for a while after the fact, so every poll
-- rewrote last_seen_at to the poll instant, which is stamped by the database
-- AFTER the request timestamp the resolution compares against. The comparison
-- was therefore false forever, for exactly the devices the rule exists to
-- clear.
-- The way back. ResolveDeviceUpdateFailures below closes a failure when the
-- device moves onto a later release; this re-opens one when the device is back
-- at or behind the update it had failed, and has reported failing it again
-- since it was closed. Without it, closing was a one-way door: a device that
-- went forward once and later hit the same broken update again stayed counted
-- as healthy for good.
--
-- Deliberately NOT done in UpsertDeviceUpdateFailure's ON CONFLICT arm, where
-- it would look natural. expo-updates keeps listing a failed id in
-- Expo-Recent-Failed-Update-IDs long after the fact, so every repeat would
-- re-open the row and the resolution running later in the same check-in would
-- close it again: two outbox events per device per debounce window, for a state
-- that never actually changed. Here the direction is known, so a device sitting
-- on a newer release repeating an old id changes nothing.
-- name: ReopenDeviceUpdateFailures :execrows
UPDATE device_update_failures failure
SET resolved_at = NULL
FROM updates running
JOIN branches running_branch ON running_branch.id = running.branch_id,
     updates failed
WHERE failure.app_id = $1
  AND failure.eas_client_id = $2
  AND failure.failure_type = 'update_issue'
  AND failure.resolved_at IS NOT NULL
  -- Reported again since it was closed, which is the only evidence that the
  -- device met the failure a second time rather than merely remembering it.
  AND failure.last_seen_at > failure.resolved_at
  AND running_branch.app_id = $1
  AND running.update_uuid = $3
  AND running.checked_at IS NOT NULL
  AND failed.update_uuid = failure.update_id
  AND failed.branch_id = running.branch_id
  AND failed.runtime_version_id = running.runtime_version_id
  AND failed.platform = running.platform
  -- Strictly BEHIND it: the device rolled back and is stuck again.
  --
  -- Not "on it", even though that reads as the symmetric case. Running the very
  -- update one failed is the evidence the resolution uses to CLOSE the row, and
  -- owning it here too made the two rules fight over the same pair of columns:
  -- a device sending both manifest polls and telemetry closed on one and
  -- re-opened on the other indefinitely, emitting an outbox event each way. The
  -- resolution settles that case with a timestamp comparison this query cannot
  -- make, so it keeps it.
  AND failed.id > running.id;

-- name: ResolveDeviceUpdateFailures :execrows
UPDATE device_update_failures failure
-- Never before the fault it closes. observed_at is not always the instant the
-- request arrived: on the telemetry path it is the newest record of the batch,
-- which a device flushing a backlog can date days ago. A resolution stamped
-- before its own first_seen_at emits its -1 in an earlier bucket than the +1 on
-- the fallback chart, and reaches ClickHouse as a 'recovered' older than the
-- 'failure' it answers.
SET resolved_at = GREATEST(sqlc.arg(observed_at), failure.first_seen_at)
-- The join to the failed update lives in the WHERE, not in an ON clause: the
-- row being updated is not part of the FROM list, so a JOIN condition cannot
-- reference it and the predicate would silently match nothing.
FROM updates running
JOIN branches running_branch ON running_branch.id = running.branch_id,
     updates failed
WHERE failure.app_id = $1
  AND failure.eas_client_id = $2
  AND failure.resolved_at IS NULL
  AND running_branch.app_id = $1
  AND running.update_uuid = $3
  AND running.checked_at IS NOT NULL
  AND failed.update_uuid = failure.update_id
  AND (
      (failed.branch_id = running.branch_id
       AND failed.runtime_version_id = running.runtime_version_id
       AND failed.platform = running.platform
       AND failed.id < running.id)
      -- Compared on the uuid, not on the id. updates.id is
      -- milliseconds*10 + a platform digit and the primary key is
      -- (branch_id, id), so ids are unique per BRANCH: two branches of one app
      -- published in the same millisecond for the same platform, which is what
      -- a CI job releasing main and staging together does, carry the same id.
      -- Matching on it would have closed a failure on one branch's update
      -- because the device runs the other's. The arm above is safe from this
      -- because it pins the whole lineage.
      OR (failed.update_uuid = running.update_uuid
          AND failure.failure_type = 'update_issue'
          AND failure.last_seen_at < sqlc.arg(observed_at))
     );

-- Successful JS startup. Recording the watermark even without an existing
-- failure prevents a delayed older crash from regressing the device. Strict
-- comparison makes a crash win timestamp ties.
-- name: ResolveDeviceRuntimeFailure :execrows
WITH runtime_state AS (
    INSERT INTO device_update_runtime_state (
        app_id, eas_client_id, update_id, last_started_at
    )
    SELECT $1, $2, u.update_uuid, sqlc.arg(occurred_at)
    FROM updates u
    JOIN branches b ON b.id = u.branch_id
    WHERE b.app_id = $1
      AND u.update_uuid = $3
      AND u.checked_at IS NOT NULL
    ON CONFLICT (app_id, eas_client_id, update_id) DO UPDATE SET
        last_started_at = GREATEST(
            device_update_runtime_state.last_started_at,
            EXCLUDED.last_started_at
        )
    RETURNING app_id, eas_client_id, update_id, last_started_at
)
UPDATE device_update_failures failure
SET resolved_at = runtime_state.last_started_at
FROM runtime_state
WHERE failure.app_id = runtime_state.app_id
  AND failure.eas_client_id = runtime_state.eas_client_id
  AND failure.update_id = runtime_state.update_id
  AND failure.failure_type = 'runtime_issue'
  AND failure.resolved_at IS NULL
  AND failure.last_seen_at < runtime_state.last_started_at;

-- Instant-T adoption: how many devices currently run this update.
-- name: CountDevicesOnUpdate :one
SELECT COUNT(*) FROM device_identity
WHERE app_id = $1 AND current_update_id = $2;

-- Instant-T health: how many devices this update crashed on at launch.
-- DISTINCT on the device: one device can hold both a launch rollback and a
-- runtime crash for the same update, which is two rows and one device.
-- name: CountUpdateFailures :one
SELECT COUNT(DISTINCT eas_client_id) FROM device_update_failures
WHERE app_id = $1 AND update_id = $2 AND resolved_at IS NULL;

-- The fleet's adoption breakdown, biggest cohorts first. NULL update = the
-- embedded bundle (or a device seen before this feature landed).
-- name: AdoptionBreakdown :many
SELECT current_update_id, COUNT(*) AS device_count
FROM device_identity
WHERE app_id = $1
GROUP BY current_update_id
ORDER BY device_count DESC, current_update_id ASC NULLS LAST;

-- Batch adoption counts for a set of updates: every device CURRENTLY running
-- each update (the dashboard's "Devices" column).
-- name: DevicesOnUpdateByIDs :many
SELECT current_update_id AS update_uuid, COUNT(*) AS device_count
FROM device_identity
WHERE app_id = $1
  AND current_update_id = ANY(sqlc.arg(update_ids)::uuid[])
GROUP BY current_update_id;

-- Batch failure breakdown for a set of updates. All-time per update: an
-- update's failures belong to its rollout window by construction (update ids
-- are never reused), and the health score is only shown for the active one.
-- still_on_update counts failed devices whose CURRENT update is still the
-- failed one (runtime_issue devices that did not move on): the overlap
-- between the failure set and the current-device cohort, which the health
-- math needs so those devices are neither double-counted as attempts nor
-- kept in the healthy numerator. A failed device that has since moved to
-- another update (or rolled back: every update_issue) leaves the overlap by
-- construction, so the join self-corrects when a device changes update.
-- Every count is DISTINCT on the device, and the two breakdowns are counted
-- independently rather than one being derived from the other. A device can hold
-- both a launch rollback and a runtime crash for the same update, which is two
-- rows and one device: counting rows would inflate the totals, and deriving
-- update_devices as failed_devices - runtime_devices would silently drop that
-- device's rollback. failed_devices is therefore the size of the failure SET
-- and may be smaller than update_devices + runtime_devices.
-- name: UpdateFailureBreakdownByIDs :many
SELECT f.update_id AS update_uuid,
       COUNT(DISTINCT f.eas_client_id) AS failed_devices,
       COUNT(DISTINCT f.eas_client_id) FILTER (WHERE f.failure_type = 'update_issue') AS update_devices,
       COUNT(DISTINCT f.eas_client_id) FILTER (WHERE f.failure_type = 'runtime_issue') AS runtime_devices,
       COUNT(DISTINCT d.eas_client_id) AS still_on_update
FROM device_update_failures f
LEFT JOIN device_identity d
    ON d.app_id = f.app_id
   AND d.eas_client_id = f.eas_client_id
   AND d.current_update_id = f.update_id
WHERE f.app_id = $1
  AND f.update_id = ANY(sqlc.arg(update_ids)::uuid[])
  AND f.resolved_at IS NULL
GROUP BY f.update_id;

-- Durable ClickHouse delivery queue. The worker reads through a transaction;
-- SKIP LOCKED lets several API replicas drain disjoint batches safely.
-- The outbox carries ids; the analytical store wants the dimensions that go
-- with them, so they are resolved here, once, on a bounded batch. The update
-- side is permanent (an update never changes branch), the device side is the
-- hardware as currently known, which is what a crash is read against.
-- Deliberately NOT "FOR UPDATE SKIP LOCKED" any more. The row lock used to be
-- what stopped two replicas delivering the same event, and it worked, but it
-- only holds for the life of its transaction: the delivery had to keep that
-- transaction open across the ClickHouse insert, so an unreachable ClickHouse
-- pinned a connection and a transaction id, and a transaction id that never
-- advances stops vacuum from cleaning a table that takes an event per device
-- state change.
--
-- Mutual exclusion moved to a session advisory lock taken by the caller, which
-- costs no transaction. What guards against a double delivery afterwards is
-- the destination: device_health_events is a ReplacingMergeTree keyed on
-- (app_id, outbox_id), so the same event delivered twice collapses.
-- name: ListDeviceHealthOutbox :many
SELECT o.id, o.event_type, o.app_id, o.eas_client_id, o.update_id, o.previous_update_id,
       o.failure_type, o.fatal_error, o.occurred_at,
       coalesce(b.name, '') AS branch,
       coalesce(rv.version, '') AS runtime_version,
       coalesce(u.platform, '') AS platform,
       coalesce(d.os_name, '') AS os_name,
       coalesce(d.os_version, '') AS os_version,
       coalesce(d.device_model, '') AS device_model,
       coalesce(d.country_code, '') AS country_code,
       coalesce(d.app_version, '') AS app_version
FROM device_health_outbox o
-- The EXISTS scopes the update to the event's app, not just its uuid. update_id
-- originates from an unauthenticated header, so a device of app A can name an
-- update of app B: without it, `branch` correctly resolves to '' (that join IS
-- app-scoped) while platform and runtime_version, which hang off this join,
-- would carry B's values into A's analytics rows. Same guard the device
-- inventory carried before its dimensions moved onto device_identity.
--
-- Read from the update rather than from `d`, which now stores the same
-- dimensions: `d` describes the update the device runs NOW, and an outbox event
-- is about the update it names, which for a rollback or a switch is a different
-- one.
LEFT JOIN updates u ON u.update_uuid = o.update_id
    AND EXISTS (
        SELECT 1 FROM branches ub
        WHERE ub.id = u.branch_id AND ub.app_id = o.app_id
    )
LEFT JOIN branches b ON b.id = u.branch_id AND b.app_id = o.app_id
LEFT JOIN runtime_versions rv ON rv.id = u.runtime_version_id
LEFT JOIN device_identity d ON d.app_id = o.app_id AND d.eas_client_id = o.eas_client_id
ORDER BY o.id
LIMIT $1;

-- name: DeleteDeviceHealthOutbox :exec
DELETE FROM device_health_outbox
WHERE id = ANY(sqlc.arg('ids')::bigint[]);

-- A deployment with no ClickHouse has no historical-health consumer. Its
-- uniform no-ClickHouse replicas periodically discard this otherwise
-- unbounded queue; PostgreSQL instant-T health does not depend on it.
-- name: DiscardDeviceHealthOutbox :exec
DELETE FROM device_health_outbox;

-- Absolute current health for every update the fleet is on. Three kinds of
-- row, and they answer different questions:
--
--   current/candidate  the newest checked update of each branch/runtime/
--                      platform, and control, the update an active rollout
--                      runs against. Their health score is what you watch
--                      during a release.
--   legacy             every other update devices are still running. Its
--                      health is settled, but "how many are stuck on the
--                      version from three months ago" is a question an OTA
--                      operator has to be able to answer, and it can only be
--                      answered if the series was recorded while it was true.
--
-- The legacy arm is bounded by where the fleet actually sits, not by how much
-- has ever been published: an app with a thousand updates has devices on a
-- handful of them. Adoption is counted once for every update in a single
-- grouped pass over (app_id, current_update_id), which is indexed, rather
-- than once per update.
--
-- The ClickHouse worker samples these rows into one-minute buckets.
-- What PostgreSQL alone can say about an update over time, for deployments
-- with no ClickHouse. It is NOT the same answer as the projected history, and
-- the difference is worth stating precisely because only one of the two series
-- below is honest about the past.
--
-- Failures are exact. device_update_failures keeps both ends of every fault
-- (first_seen_at, and resolved_at when a runtime issue recovers), so a count at
-- an instant is a real count at that instant, and the curve can fall as well as
-- rise.
--
-- Arrivals are not, and cannot be. device_identity holds one row per device,
-- overwritten: current_update_arrived_at records when a device MOVED ONTO the
-- update it runs now, never when it left the one before. So the only population reachable here
-- is "devices still on this update today", dated by when each of them arrived.
-- That curve is exact at its right edge and increasingly revisionist the
-- further back it is read, because every device that has since moved away was
-- erased from its own history. It can only ever rise: a rollback does not show
-- as a fall, it shows as a peak that was never there.
--
-- Deltas rather than a count per bucket, which would have meant a full scan of
-- device_identity per point on the chart. Anything older than the window is
-- folded into bucket zero so the caller's running total starts at what was
-- already there rather than at nothing. The caller holds the window start and
-- the step, so it turns an index back into an instant and carries the running
-- sum itself.
-- name: ListUpdateHealthStateDeltas :many
WITH failure_islands AS (
    -- A device is failing on an update from its first fault until its last one
    -- clears, and one device counts ONCE however many faults it holds: since
    -- 20260726140000_failure_type_in_key.sql a launch rollback and a JS crash
    -- on the same pair are two rows by design, and counting rows let the
    -- failing curve climb above the population it is drawn against.
    --
    -- Merged rather than collapsed to one span, though. Taking MIN(first_seen)
    -- and MAX(resolved) over every row of the device would bridge the gap
    -- between two faults that did not overlap: a device that failed at 09:00,
    -- recovered at 09:30 and crashed again at 14:00 would have read as failing
    -- for the five healthy hours in between, and its second fault would have
    -- been dated five hours early. So overlapping faults merge and disjoint
    -- ones stay apart, which is the standard islands walk below.
    SELECT update_id,
           eas_client_id,
           MIN(first_seen_at) AS opened_at,
           CASE WHEN bool_or(resolved_at IS NULL) THEN NULL ELSE MAX(resolved_at) END AS closed_at
    FROM (
        SELECT update_id, eas_client_id, first_seen_at, resolved_at,
               SUM(CASE WHEN prev_end IS NULL OR first_seen_at > prev_end THEN 1 ELSE 0 END)
                   OVER (PARTITION BY update_id, eas_client_id ORDER BY first_seen_at) AS island
        FROM (
            SELECT f.update_id, f.eas_client_id, f.first_seen_at, f.resolved_at,
                   -- An unresolved fault swallows everything that starts after
                   -- it, hence infinity rather than NULL.
                   MAX(COALESCE(f.resolved_at, 'infinity'::timestamptz)) OVER (
                       PARTITION BY f.update_id, f.eas_client_id
                       ORDER BY f.first_seen_at
                       ROWS BETWEEN UNBOUNDED PRECEDING AND 1 PRECEDING) AS prev_end
            FROM device_update_failures f
            WHERE f.app_id = @app_id
              AND f.update_id = ANY(@update_ids::uuid[])
        ) ordered
    ) grouped
    GROUP BY update_id, eas_client_id, island
)
SELECT d.update_uuid,
       d.idx AS bucket_index,
       SUM(d.adopted)::bigint AS adopted_delta,
       SUM(d.failing)::bigint AS failing_delta
FROM (
    SELECT di.current_update_id AS update_uuid,
           floor(EXTRACT(EPOCH FROM (
               GREATEST(
                   -- No recorded arrival means the device was already there
                   -- when the column appeared, so it belongs to the first
                   -- bucket rather than to nothing at all. Dropping those rows
                   -- put the curve permanently below the adoption count shown
                   -- beside it.
                   COALESCE(di.current_update_arrived_at, @from_ts::timestamptz),
                   @from_ts::timestamptz
               ) - @from_ts::timestamptz
           )) / @step_seconds::int)::int AS idx,
           1 AS adopted,
           0 AS failing
    FROM device_identity di
    WHERE di.app_id = @app_id
      AND di.current_update_id = ANY(@update_ids::uuid[])
      AND (di.current_update_arrived_at IS NULL
           OR di.current_update_arrived_at <= @to_ts::timestamptz)

    UNION ALL

    SELECT i.update_id,
           floor(EXTRACT(EPOCH FROM (
               GREATEST(i.opened_at, @from_ts::timestamptz) - @from_ts::timestamptz
           )) / @step_seconds::int)::int,
           0,
           1
    FROM failure_islands i
    WHERE i.opened_at <= @to_ts::timestamptz

    UNION ALL

    -- The other end of each merged fault.
    SELECT i.update_id,
           floor(EXTRACT(EPOCH FROM (
               GREATEST(i.closed_at, @from_ts::timestamptz) - @from_ts::timestamptz
           )) / @step_seconds::int)::int,
           0,
           -1
    FROM failure_islands i
    WHERE i.closed_at IS NOT NULL
      AND i.closed_at <= @to_ts::timestamptz
      -- A resolution stamped before the fault it closes would otherwise emit
      -- its -1 in an earlier bucket than the +1 and drive the curve negative.
      AND i.closed_at >= i.opened_at
) d
GROUP BY d.update_uuid, d.idx
ORDER BY d.update_uuid, d.idx;

-- name: ListCurrentUpdateHealthSnapshots :many
WITH latest AS (
    SELECT DISTINCT ON (u.branch_id, u.runtime_version_id, u.platform)
           b.app_id,
           u.branch_id,
           u.runtime_version_id,
           u.platform,
           u.update_uuid,
           u.rollout_percentage,
           u.control_update_id
    FROM updates u
    JOIN branches b ON b.id = u.branch_id
    WHERE u.checked_at IS NOT NULL
    ORDER BY u.branch_id, u.runtime_version_id, u.platform, u.id DESC
),
adoption AS (
    SELECT app_id, current_update_id AS update_uuid, COUNT(*) AS devices_on_update
    FROM device_identity
    WHERE current_update_id IS NOT NULL
    GROUP BY app_id, current_update_id
),
tracked AS (
    SELECT app_id,
           update_uuid,
           CASE WHEN rollout_percentage IS NULL THEN 'current' ELSE 'candidate' END::text AS role
    FROM latest
    WHERE update_uuid IS NOT NULL

    UNION ALL

    SELECT l.app_id, control.update_uuid, 'control'::text AS role
    FROM latest l
    JOIN updates control
      ON control.branch_id = l.branch_id
     AND control.id = l.control_update_id
    WHERE l.rollout_percentage IS NOT NULL
      AND control.update_uuid IS NOT NULL
),
relevant AS (
    SELECT * FROM tracked

    UNION ALL

    -- Restricted to updates this server published: a device reporting an id
    -- nobody knows must not mint a series for an update that does not exist.
    SELECT a.app_id, a.update_uuid, 'legacy'::text AS role
    FROM adoption a
    WHERE NOT EXISTS (
        SELECT 1 FROM tracked t
        WHERE t.app_id = a.app_id AND t.update_uuid = a.update_uuid
    )
      AND EXISTS (
        SELECT 1
        FROM updates u
        JOIN branches b ON b.id = u.branch_id
        WHERE b.app_id = a.app_id AND u.update_uuid = a.update_uuid
    )
)
SELECT r.app_id,
       r.update_uuid,
       r.role,
       COALESCE(adoption.devices_on_update, 0)::bigint AS devices_on_update,
       (COALESCE(adoption.devices_on_update, 0) - COALESCE(failures.still_on_update, 0))::bigint AS successful_devices,
       COALESCE(failures.faulty_devices, 0)::bigint AS faulty_devices,
       COALESCE(failures.update_issues, 0)::bigint AS update_issues,
       COALESCE(failures.runtime_issues, 0)::bigint AS runtime_issues
FROM relevant r
LEFT JOIN adoption ON adoption.app_id = r.app_id AND adoption.update_uuid = r.update_uuid
LEFT JOIN LATERAL (
    SELECT COUNT(*) AS faulty_devices,
           COUNT(*) FILTER (WHERE f.failure_type = 'update_issue') AS update_issues,
           COUNT(*) FILTER (WHERE f.failure_type = 'runtime_issue') AS runtime_issues,
           COUNT(*) FILTER (
               WHERE EXISTS (
                   SELECT 1
                   FROM device_identity d
                   WHERE d.app_id = f.app_id
                     AND d.eas_client_id = f.eas_client_id
                     AND d.current_update_id = f.update_id
               )
           ) AS still_on_update
    FROM device_update_failures f
    WHERE f.app_id = r.app_id
      AND f.update_id = r.update_uuid
      AND f.resolved_at IS NULL
) failures ON TRUE;

-- name: InsertOAuthClient :exec
INSERT INTO oauth_clients (id, name, redirect_uris)
VALUES ($1, $2, $3);

-- name: GetOAuthClient :one
SELECT * FROM oauth_clients WHERE id = $1;

-- name: InsertOAuthAuthorizationCode :exec
INSERT INTO oauth_authorization_codes (id, client_id, user_id, redirect_uri, code_challenge, scope, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: DeleteExpiredOAuthAuthorizationCodes :exec
DELETE FROM oauth_authorization_codes WHERE expires_at < CURRENT_TIMESTAMP;

-- name: ConsumeOAuthAuthorizationCode :one
-- Single-use claim, atomic on purpose: two exchanges presenting the same code
-- concurrently must not both succeed. The loser gets no row.
UPDATE oauth_authorization_codes
SET used_at = CURRENT_TIMESTAMP
WHERE id = $1 AND used_at IS NULL AND expires_at > CURRENT_TIMESTAMP
RETURNING *;

-- name: GetChannelBranchSurfing :one
SELECT branch_surfing_enabled, branch_surfing_pattern
FROM channels
WHERE app_id = $1 AND name = $2;

-- name: UpdateChannelBranchSurfing :execresult
UPDATE channels
SET branch_surfing_enabled = $3, branch_surfing_pattern = $4
WHERE app_id = $1 AND name = $2;

-- name: GetSurfableBranches :many
-- Branches a device on this app, runtime version and platform could actually be
-- served. A branch with no published update matching all three is unreachable,
-- so listing it would offer a switch that silently does nothing: the manifest
-- route filters on platform too, and would quietly fall back to the channel's
-- branch. rv is scoped to the app as well: version strings are unique per app,
-- not globally.
SELECT b.name AS branch_name, MAX(u.created_at)::timestamptz AS last_update_at
FROM branches b
JOIN updates u ON u.branch_id = b.id AND u.checked_at IS NOT NULL
JOIN runtime_versions rv ON rv.id = u.runtime_version_id AND rv.app_id = $1
WHERE b.app_id = $1 AND rv.version = $2 AND u.platform = @platform::text
GROUP BY b.id, b.name
ORDER BY MAX(u.created_at) DESC, b.name ASC;
