-- +goose Up
CREATE TABLE ios_signing_settings (
    app_identifier_id UUID NOT NULL REFERENCES app_identifiers(id) ON DELETE CASCADE,
    distribution TEXT NOT NULL CHECK (distribution IN ('app-store', 'ad-hoc')),
    mode TEXT NOT NULL CHECK (mode IN ('automatic', 'certificate')),
    -- NULL in mode 'certificate' means the selected certificate was deleted.
    certificate_id UUID REFERENCES ios_certificates(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (app_identifier_id, distribution),
    CHECK (mode = 'certificate' OR certificate_id IS NULL)
);

INSERT INTO ios_signing_settings (app_identifier_id, distribution, mode, certificate_id, created_at, updated_at)
SELECT app_identifier_id, 'app-store', 'certificate', certificate_id, created_at, updated_at
FROM ios_identifier_certificates;

DROP TABLE ios_identifier_certificates;
DROP TABLE ios_provisioning_profiles;

-- +goose Down
-- Certificate links come back from the App Store settings; provisioning profiles are not restored.
CREATE TABLE ios_identifier_certificates (
    app_identifier_id UUID PRIMARY KEY REFERENCES app_identifiers(id) ON DELETE CASCADE,
    certificate_id UUID NOT NULL REFERENCES ios_certificates(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO ios_identifier_certificates (app_identifier_id, certificate_id, created_at, updated_at)
SELECT app_identifier_id, certificate_id, created_at, updated_at
FROM ios_signing_settings
WHERE distribution = 'app-store' AND mode = 'certificate' AND certificate_id IS NOT NULL;

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
    CONSTRAINT ios_provisioning_profiles_identifier_bundle_type_key UNIQUE (app_identifier_id, bundle_identifier, profile_type)
);

DROP TABLE ios_signing_settings;
