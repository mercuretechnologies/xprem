-- name: InsertIosDeviceInvitation :one
INSERT INTO ios_device_invitations (
    id, app_id, token_hash, challenge, label, expires_at,
    created_by_actor_type, created_by_actor_id, created_by_actor_display
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING created_at;

-- name: ListIosDeviceInvitations :many
SELECT i.id, i.label, i.expires_at, i.revoked_at, i.consumed_at, i.created_at, i.created_by_actor_display,
       COALESCE(r.device_name, '')::text AS device_name, COALESCE(r.product, '')::text AS device_product
FROM ios_device_invitations i
LEFT JOIN ios_device_registrations r ON r.id = i.registration_id
WHERE i.app_id = $1
ORDER BY i.created_at DESC, i.id;

-- name: RevokeIosDeviceInvitation :one
UPDATE ios_device_invitations SET revoked_at = COALESCE(revoked_at, now())
WHERE app_id = $1 AND id = $2
RETURNING label;

-- name: ResolveIosDeviceInvitation :one
SELECT i.id, i.app_id, i.challenge, i.label, i.expires_at, (i.consumed_at IS NOT NULL)::bool AS consumed, a.name AS app_name
FROM ios_device_invitations i
JOIN apps a ON a.id = i.app_id
WHERE i.token_hash = $1 AND i.revoked_at IS NULL AND i.expires_at > now();

-- name: ClaimIosDeviceInvitation :execrows
-- A claim older than five minutes may be reclaimed; its old owner can no longer finish or release it.
UPDATE ios_device_invitations SET claimed_at = now(), claim_token = $2
WHERE id = $1 AND consumed_at IS NULL AND revoked_at IS NULL AND expires_at > now()
    AND (claimed_at IS NULL OR claimed_at < now() - interval '5 minutes');

-- name: ConsumeIosDeviceInvitation :execrows
UPDATE ios_device_invitations SET consumed_at = now(), registration_id = $2, claimed_at = NULL, claim_token = NULL
WHERE id = $1 AND claim_token = $3 AND consumed_at IS NULL AND revoked_at IS NULL AND expires_at > now();

-- name: ReleaseIosDeviceInvitation :execrows
UPDATE ios_device_invitations SET claimed_at = NULL, claim_token = NULL
WHERE id = $1 AND claim_token = $2;

-- name: InsertIosDeviceRegistration :exec
INSERT INTO ios_device_registrations (
    id, invitation_id, udid, device_name, product, os_version, status, apple_device_id, error
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: GetIosDeviceRegistration :one
SELECT r.status, r.device_name, r.product, r.error
FROM ios_device_registrations r
JOIN ios_device_invitations i ON i.id = r.invitation_id
WHERE i.token_hash = $1 AND r.id = $2;

-- name: ListRegisteredIosDevices :many
SELECT DISTINCT ON (upper(r.udid)) upper(r.udid)::text AS udid, r.product, r.os_version, r.created_at,
       i.id AS invitation_id, i.label
FROM ios_device_registrations r
JOIN ios_device_invitations i ON i.id = r.invitation_id
WHERE i.app_id = $1 AND r.status = 'registered'
ORDER BY upper(r.udid), r.created_at DESC;
