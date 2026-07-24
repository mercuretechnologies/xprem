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
-- included — protection must be lifted first. The guard runs inside the
-- DELETE itself so a concurrent protect cannot race it; the store
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

-- name: GetUpdateFeed :many
SELECT u.id, u.update_uuid, u.update_type, u.created_at, u.commit_hash,
       u.platform, u.message, u.rollout_percentage, u.control_update_id,
       u.publish_group, u.branch_id, b.name AS branch_name,
       rv.version AS runtime_version,
       CASE WHEN
         -- The newest checked update is the current candidate. During a
         -- progressive rollout, its explicitly captured control remains
         -- current for the out-of-bucket cohort too.
         u.id = (
           SELECT current_update.id
           FROM updates current_update
           WHERE current_update.branch_id = u.branch_id
             AND current_update.runtime_version_id = u.runtime_version_id
             AND current_update.platform = u.platform
             AND current_update.checked_at IS NOT NULL
           ORDER BY current_update.id DESC
           LIMIT 1
         )
         OR EXISTS (
           SELECT 1
           FROM updates candidate
           WHERE candidate.branch_id = u.branch_id
             AND candidate.runtime_version_id = u.runtime_version_id
             AND candidate.platform = u.platform
             AND candidate.checked_at IS NOT NULL
             AND candidate.rollout_percentage IS NOT NULL
             AND candidate.control_update_id = u.id
         )
       THEN TRUE ELSE FALSE END AS health_relevant
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
UPDATE users
SET password_hash = $2, updated_at = CURRENT_TIMESTAMP
WHERE id = $1;

-- name: UpdateUserIsAdminByID :execresult
-- Same admin-row lock as DeleteUserByID: demoting the last remaining admin
-- matches no row. Promotions ($2 true) always pass the guard but still take
-- the lock, so they serialize with concurrent demotions.
WITH admins AS (
    SELECT id FROM users WHERE is_admin AND enabled ORDER BY id FOR UPDATE
)
UPDATE users
SET is_admin = $2, updated_at = CURRENT_TIMESTAMP
WHERE users.id = $1
  AND ($2::boolean
       OR users.id NOT IN (SELECT id FROM admins)
       OR (SELECT COUNT(*) FROM admins) > 1);

-- name: UpdateUserEnabledByID :execresult
-- Same admin-row lock as DeleteUserByID: disabling the last remaining enabled
-- admin matches no row, so approving/revoking accounts can never lock the
-- dashboard out. Enabling ($2 true) always passes the guard but still takes
-- the lock, so it serializes with concurrent disables.
WITH admins AS (
    SELECT id FROM users WHERE is_admin AND enabled ORDER BY id FOR UPDATE
)
UPDATE users
SET enabled = $2, updated_at = CURRENT_TIMESTAMP
WHERE users.id = $1
  AND ($2::boolean
       OR users.id NOT IN (SELECT id FROM admins)
       OR (SELECT COUNT(*) FROM admins) > 1);

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

-- name: GetApiKeyRestrictions :one
-- Enforcement read for one authenticated key on the CLI request hot path.
SELECT allowed_ips, can_access_protected_branches
FROM api_keys
WHERE id = $1;

-- name: GetApiKeyRestrictionsByAppID :many
SELECT id, allowed_ips, can_access_protected_branches
FROM api_keys
WHERE app_id = $1 AND revoked_at IS NULL;

-- name: UpdateApiKeyRestrictions :execrows
UPDATE api_keys
SET allowed_ips = $1, can_access_protected_branches = $2
WHERE id = $3 AND app_id = $4 AND revoked_at IS NULL;

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

-- name: CountGrantsByRole :one
SELECT COUNT(*) FROM user_app_grants
WHERE role_id = $1;

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
-- Idempotent device-row creation inside mutate's transaction.
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

-- name: IncrementIdentityValueStat :exec
INSERT INTO identity_value_stats (app_id, key, value, device_count)
VALUES ($1, $2, $3, 1)
ON CONFLICT (app_id, key, value) DO UPDATE SET
    device_count = identity_value_stats.device_count + 1,
    last_seen_at = CURRENT_TIMESTAMP;

-- name: DecrementIdentityValueStat :exec
UPDATE identity_value_stats
SET device_count = GREATEST(device_count - 1, 0)
WHERE app_id = $1 AND key = $2 AND value = $3;

-- name: DeleteZeroIdentityValueStats :exec
DELETE FROM identity_value_stats
WHERE app_id = $1 AND key = $2 AND value = $3 AND device_count <= 0;

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

-- Device inventory for the dashboard: newest-seen first, keyset-paginated on
-- (last_seen_at DESC, eas_client_id DESC) so deep pages stay cheap. The
-- optional jsonb filter (metadata @> $filter, served by the GIN index) powers
-- "devices for a userId / tenant". Fetch one extra row to detect the next page.
-- name: ListDevices :many
SELECT * FROM device_identity
WHERE app_id = $1
  AND (sqlc.narg('filter')::jsonb IS NULL OR metadata @> sqlc.narg('filter')::jsonb)
  AND (
    sqlc.narg('before_last_seen')::timestamptz IS NULL
    OR last_seen_at < sqlc.narg('before_last_seen')::timestamptz
    OR (last_seen_at = sqlc.narg('before_last_seen')::timestamptz
        AND eas_client_id < sqlc.narg('before_client_id')::uuid)
  )
ORDER BY last_seen_at DESC, eas_client_id DESC
LIMIT sqlc.arg('lim')::int;

-- The observe flattener denormalizes the branch onto every ClickHouse row.
-- Resolved from the update uuid (permanent: an update never changes branch),
-- NEVER from the channel (a channel can be re-pointed over time). Cached
-- in-process by the caller, so this runs once per distinct update.
-- name: GetBranchNameByUpdateUUID :one
SELECT b.name
FROM updates u
INNER JOIN branches b ON u.branch_id = b.id
WHERE b.app_id = $1 AND u.update_uuid = $2
LIMIT 1;

-- Passive-contact bump (manifest poll, telemetry batch): refresh last_seen and
-- opportunistically enrich geo, never touching metadata. 1 row = known device;
-- 0 = brand new, the caller registers it.
-- name: TouchDeviceIdentity :execrows
UPDATE device_identity SET
    country_code = COALESCE(sqlc.narg('country_code'), country_code),
    city = COALESCE(sqlc.narg('city'), city),
    lat = COALESCE(sqlc.narg('lat'), lat),
    lng = COALESCE(sqlc.narg('lng'), lng),
    current_update_id = COALESCE(sqlc.narg('current_update_id'), current_update_id),
    last_seen_at = CURRENT_TIMESTAMP
WHERE app_id = $1 AND eas_client_id = $2;

-- Registration upsert for the passive path: the registry is uncapped (the
-- whole fleet is the update-health source of truth). ON CONFLICT absorbs the
-- race with a concurrent registration of the same device.
-- name: RegisterDevice :execrows
INSERT INTO device_identity (app_id, eas_client_id, country_code, city, lat, lng, current_update_id)
VALUES ($1, $2, sqlc.narg('country_code'), sqlc.narg('city'), sqlc.narg('lat'), sqlc.narg('lng'), sqlc.narg('current_update_id'))
ON CONFLICT (app_id, eas_client_id) DO UPDATE SET
    last_seen_at = CURRENT_TIMESTAMP,
    current_update_id = COALESCE(EXCLUDED.current_update_id, device_identity.current_update_id);


-- Records a manifest/native failure at server receipt time. fatal_error and
-- failure_type stay capture-once.
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
ON CONFLICT (app_id, eas_client_id, update_id) DO UPDATE SET
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
ON CONFLICT (app_id, eas_client_id, update_id) DO UPDATE SET
    last_seen_at = GREATEST(
        device_update_failures.last_seen_at,
        EXCLUDED.last_seen_at
    ),
    resolved_at = CASE
        WHEN device_update_failures.failure_type = 'runtime_issue'
         AND device_update_failures.resolved_at IS NOT NULL
         AND EXCLUDED.last_seen_at > device_update_failures.resolved_at
        THEN NULL
        ELSE device_update_failures.resolved_at
    END,
    fatal_error = CASE
        WHEN device_update_failures.fatal_error = '' THEN EXCLUDED.fatal_error
        ELSE device_update_failures.fatal_error
    END;

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
-- name: CountUpdateFailures :one
SELECT COUNT(*) FROM device_update_failures
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
-- name: UpdateFailureBreakdownByIDs :many
SELECT f.update_id AS update_uuid,
       COUNT(*) AS failure_count,
       COUNT(*) FILTER (WHERE f.failure_type = 'runtime_issue') AS runtime_count,
       COUNT(d.eas_client_id) AS still_on_update
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
-- name: ListDeviceHealthOutbox :many
SELECT id, event_type, app_id, eas_client_id, update_id, previous_update_id,
       failure_type, fatal_error, occurred_at
FROM device_health_outbox
ORDER BY id
LIMIT $1
FOR UPDATE SKIP LOCKED;

-- name: DeleteDeviceHealthOutbox :exec
DELETE FROM device_health_outbox
WHERE id = ANY(sqlc.arg('ids')::bigint[]);

-- A deployment with no ClickHouse has no historical-health consumer. Its
-- uniform no-ClickHouse replicas periodically discard this otherwise
-- unbounded queue; PostgreSQL instant-T health does not depend on it.
-- name: DiscardDeviceHealthOutbox :exec
DELETE FROM device_health_outbox;

-- Absolute current health for every update whose score is meaningful now:
-- the newest checked update of each branch/runtime/platform, plus the control
-- still serving an active rollout's out-of-bucket cohort. The ClickHouse
-- worker samples these rows into one-minute buckets.
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
relevant AS (
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
LEFT JOIN LATERAL (
    SELECT COUNT(*) AS devices_on_update
    FROM device_identity d
    WHERE d.app_id = r.app_id
      AND d.current_update_id = r.update_uuid
) adoption ON TRUE
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
