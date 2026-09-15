-- +goose Up
CREATE TABLE ios_certificates (
    id UUID PRIMARY KEY,
    sealed_certificate TEXT NOT NULL,
    sealed_certificate_password TEXT NOT NULL,
    common_name TEXT NOT NULL,
    serial_number TEXT NOT NULL,
    fingerprint_sha1 TEXT NOT NULL UNIQUE,
    certificate_type TEXT NOT NULL CHECK (certificate_type IN ('distribution', 'development')),
    team_id TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    source TEXT NOT NULL CHECK (source IN ('generated', 'uploaded')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE ios_identifier_certificates (
    app_identifier_id UUID PRIMARY KEY REFERENCES app_identifiers(id) ON DELETE CASCADE,
    certificate_id UUID NOT NULL REFERENCES ios_certificates(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE ios_provisioning_profiles (
    id UUID PRIMARY KEY,
    app_identifier_id UUID NOT NULL REFERENCES app_identifiers(id) ON DELETE CASCADE,
    bundle_identifier TEXT NOT NULL,
    profile_uuid TEXT NOT NULL,
    name TEXT NOT NULL,
    team_id TEXT NOT NULL,
    profile_type TEXT NOT NULL CHECK (profile_type IN ('app-store', 'ad-hoc', 'enterprise', 'development')),
    expires_at TIMESTAMPTZ NOT NULL,
    device_count INTEGER NOT NULL DEFAULT 0,
    certificate_fingerprints TEXT[] NOT NULL,
    sealed_profile TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (app_identifier_id, bundle_identifier)
);

CREATE TABLE app_store_connect_api_keys (
    id UUID PRIMARY KEY,
    app_id UUID NOT NULL UNIQUE REFERENCES apps(id) ON DELETE CASCADE,
    key_id TEXT NOT NULL,
    issuer_id TEXT NOT NULL,
    sealed_private_key TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE app_store_connect_api_keys;
DROP TABLE ios_provisioning_profiles;
DROP TABLE ios_identifier_certificates;
DROP TABLE ios_certificates;
