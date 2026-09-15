-- +goose Up
ALTER TABLE ios_provisioning_profiles DROP CONSTRAINT ios_provisioning_profiles_app_identifier_id_bundle_identifi_key;
ALTER TABLE ios_provisioning_profiles ADD CONSTRAINT ios_provisioning_profiles_identifier_bundle_type_key
    UNIQUE (app_identifier_id, bundle_identifier, profile_type);

-- +goose Down
-- Refuse rollback while a bundle id has profiles of several types.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM ios_provisioning_profiles
        GROUP BY app_identifier_id, bundle_identifier
        HAVING COUNT(*) > 1
    ) THEN
        RAISE EXCEPTION 'Cannot restore one provisioning profile per bundle identifier without deleting profiles';
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE ios_provisioning_profiles DROP CONSTRAINT ios_provisioning_profiles_identifier_bundle_type_key;
ALTER TABLE ios_provisioning_profiles ADD CONSTRAINT ios_provisioning_profiles_app_identifier_id_bundle_identifi_key
    UNIQUE (app_identifier_id, bundle_identifier);
