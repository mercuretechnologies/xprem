-- +goose Up
-- One setting per identifier: an App Store setting wins over an Ad Hoc one.
DELETE FROM ios_signing_settings ad_hoc
USING ios_signing_settings app_store
WHERE ad_hoc.app_identifier_id = app_store.app_identifier_id
    AND ad_hoc.distribution = 'ad-hoc'
    AND app_store.distribution = 'app-store';
ALTER TABLE ios_signing_settings DROP CONSTRAINT ios_signing_settings_pkey;
ALTER TABLE ios_signing_settings DROP COLUMN distribution;
ALTER TABLE ios_signing_settings ADD CONSTRAINT ios_signing_settings_pkey PRIMARY KEY (app_identifier_id);

-- +goose Down
-- Each setting comes back as the App Store setting of its identifier.
ALTER TABLE ios_signing_settings DROP CONSTRAINT ios_signing_settings_pkey;
ALTER TABLE ios_signing_settings ADD COLUMN distribution TEXT NOT NULL DEFAULT 'app-store'
    CHECK (distribution IN ('app-store', 'ad-hoc'));
ALTER TABLE ios_signing_settings ALTER COLUMN distribution DROP DEFAULT;
ALTER TABLE ios_signing_settings ADD CONSTRAINT ios_signing_settings_pkey PRIMARY KEY (app_identifier_id, distribution);
