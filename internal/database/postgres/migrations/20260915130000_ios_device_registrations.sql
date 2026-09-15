-- +goose Up
CREATE TABLE ios_device_invitations (
    id UUID PRIMARY KEY,
    app_id UUID NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    challenge TEXT NOT NULL,
    label TEXT NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    created_by_actor_type TEXT NOT NULL,
    created_by_actor_id TEXT NOT NULL,
    created_by_actor_display TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX ios_device_invitations_app ON ios_device_invitations(app_id, created_at DESC);

CREATE TABLE ios_device_registrations (
    id UUID PRIMARY KEY,
    invitation_id UUID NOT NULL REFERENCES ios_device_invitations(id) ON DELETE CASCADE,
    udid TEXT NOT NULL,
    device_name TEXT NOT NULL,
    product TEXT NOT NULL,
    os_version TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('registered', 'failed')),
    apple_device_id TEXT,
    error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX ios_device_registrations_invitation ON ios_device_registrations(invitation_id);

-- +goose Down
DROP TABLE ios_device_registrations;
DROP TABLE ios_device_invitations;
