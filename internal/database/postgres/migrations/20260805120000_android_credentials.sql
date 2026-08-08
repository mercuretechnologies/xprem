-- +goose Up
-- +goose StatementBegin

CREATE TABLE app_identifiers (
    id UUID PRIMARY KEY,
    app_id UUID NOT NULL,
    platform VARCHAR(10) NOT NULL,
    identifier VARCHAR(255) NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT fk_app_identifiers_app FOREIGN KEY (app_id) REFERENCES apps(id) ON DELETE CASCADE,
    CONSTRAINT uq_app_identifier UNIQUE (app_id, platform, identifier)
);

DROP TABLE IF EXISTS android_credentials;
CREATE TABLE android_credentials (
    id UUID PRIMARY KEY,
    app_identifier_id UUID NOT NULL UNIQUE,
    key_alias VARCHAR(255) NOT NULL,
    sealed_keystore TEXT NOT NULL,
    sealed_keystore_password TEXT NOT NULL,
    sealed_key_password TEXT NOT NULL,
    sealed_google_service_account_key TEXT,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT fk_android_credentials_identifier FOREIGN KEY (app_identifier_id) REFERENCES app_identifiers(id) ON DELETE CASCADE
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS android_credentials;
DROP TABLE IF EXISTS app_identifiers;
-- +goose StatementEnd
