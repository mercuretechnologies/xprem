-- +goose Up
-- +goose StatementBegin

-- Android signing credentials of an app, one set per app. The keystore and
-- its passwords are sealed with AES-GCM under the deployment master key,
-- bound to the app id; only android_package and key_alias are readable as-is.
CREATE TABLE android_credentials (
    id UUID PRIMARY KEY,
    app_id UUID NOT NULL UNIQUE,
    android_package VARCHAR(255) NOT NULL,
    key_alias VARCHAR(255) NOT NULL,
    sealed_keystore TEXT NOT NULL,
    sealed_keystore_password TEXT NOT NULL,
    sealed_key_password TEXT NOT NULL,
    sealed_google_service_account_key TEXT,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT fk_android_credentials_app FOREIGN KEY (app_id) REFERENCES apps(id) ON DELETE CASCADE
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS android_credentials;
-- +goose StatementEnd
