-- +goose Up
-- +goose StatementBegin

DROP TABLE IF EXISTS branch_env_vars;

-- An environment is a named set of variables (production, staging, ...),
-- independent of branches. A channel may point to one as its default.
CREATE TABLE environments (
    id UUID PRIMARY KEY,
    app_id UUID NOT NULL,
    name VARCHAR(128) NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT fk_environments_app FOREIGN KEY (app_id) REFERENCES apps(id) ON DELETE CASCADE,
    CONSTRAINT uq_environment_per_app UNIQUE (app_id, name)
);

-- key is stored without the EXPO_PUBLIC_ prefix; is_public decides whether
-- injection adds it. Values are sealed with AES-GCM under the deployment
-- master key.
CREATE TABLE environment_vars (
    id UUID PRIMARY KEY,
    environment_id UUID NOT NULL,
    key VARCHAR(200) NOT NULL,
    is_public BOOLEAN NOT NULL,
    sealed_value TEXT NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT fk_environment_vars_environment FOREIGN KEY (environment_id) REFERENCES environments(id) ON DELETE CASCADE,
    CONSTRAINT uq_environment_var UNIQUE (environment_id, key)
);

ALTER TABLE channels
    ADD COLUMN environment_id UUID,
    ADD CONSTRAINT fk_channels_environment FOREIGN KEY (environment_id) REFERENCES environments(id) ON DELETE RESTRICT;

CREATE INDEX idx_channels_environment ON channels (environment_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE channels DROP COLUMN environment_id;
DROP TABLE IF EXISTS environment_vars;
DROP TABLE IF EXISTS environments;
-- +goose StatementEnd
