-- +goose Up
ALTER TABLE ios_device_invitations ADD COLUMN claim_token UUID;

-- +goose Down
ALTER TABLE ios_device_invitations DROP COLUMN claim_token;
