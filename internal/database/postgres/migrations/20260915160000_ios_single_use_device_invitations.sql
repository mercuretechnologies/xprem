-- +goose Up
ALTER TABLE ios_device_invitations
    ADD COLUMN claimed_at TIMESTAMPTZ,
    ADD COLUMN consumed_at TIMESTAMPTZ,
    ADD COLUMN registration_id UUID REFERENCES ios_device_registrations(id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE ios_device_invitations
    DROP COLUMN registration_id,
    DROP COLUMN consumed_at,
    DROP COLUMN claimed_at;
