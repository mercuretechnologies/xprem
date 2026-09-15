-- name: InsertIosCertificate :one
-- DO UPDATE instead of DO NOTHING so RETURNING yields the id of an existing row.
INSERT INTO ios_certificates (
    id, sealed_certificate, sealed_certificate_password, common_name, serial_number,
    fingerprint_sha1, certificate_type, team_id, expires_at, source
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (fingerprint_sha1) DO UPDATE SET fingerprint_sha1 = EXCLUDED.fingerprint_sha1
RETURNING id;

-- name: UpdateIosCertificateFile :exec
UPDATE ios_certificates
SET sealed_certificate = $2, sealed_certificate_password = $3, updated_at = now()
WHERE id = $1;

-- name: GetIosCertificate :one
SELECT id, common_name, serial_number, fingerprint_sha1, certificate_type, team_id,
       expires_at, source, created_at
FROM ios_certificates
WHERE id = $1;

-- name: ListIosCertificates :many
SELECT id, common_name, serial_number, fingerprint_sha1, certificate_type, team_id,
       expires_at, source, created_at
FROM ios_certificates
ORDER BY created_at DESC, id;

-- name: GetIosSigningSetting :one
SELECT mode, certificate_id
FROM ios_signing_settings
WHERE app_identifier_id = $1;

-- name: UpsertIosSigningSetting :exec
INSERT INTO ios_signing_settings (app_identifier_id, mode, certificate_id)
VALUES ($1, $2, $3)
ON CONFLICT (app_identifier_id) DO UPDATE SET
    mode = EXCLUDED.mode,
    certificate_id = EXCLUDED.certificate_id,
    updated_at = now();

-- name: UpsertAppStoreConnectApiKey :exec
INSERT INTO app_store_connect_api_keys (id, app_id, key_id, issuer_id, sealed_private_key)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (app_id) DO UPDATE SET
    key_id = EXCLUDED.key_id,
    issuer_id = EXCLUDED.issuer_id,
    sealed_private_key = EXCLUDED.sealed_private_key,
    updated_at = now();

-- name: GetAppStoreConnectApiKey :one
SELECT key_id, issuer_id, sealed_private_key, created_at, updated_at
FROM app_store_connect_api_keys
WHERE app_id = $1;

-- name: DeleteAppStoreConnectApiKey :execresult
DELETE FROM app_store_connect_api_keys WHERE app_id = $1;
