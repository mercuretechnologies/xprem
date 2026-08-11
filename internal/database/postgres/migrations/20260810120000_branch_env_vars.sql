-- +goose Up
-- +goose StatementBegin

-- Environment variables of a branch, resolved at build/publish time. key is
-- stored without the EXPO_PUBLIC_ prefix; is_public decides whether injection
-- adds it. Values are sealed with AES-GCM under the deployment master key.
CREATE TABLE branch_env_vars (
    id UUID PRIMARY KEY,
    app_id UUID NOT NULL,
    branch_id BIGINT NOT NULL,
    key VARCHAR(200) NOT NULL,
    is_public BOOLEAN NOT NULL,
    sealed_value TEXT NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT fk_branch_env_vars_app FOREIGN KEY (app_id) REFERENCES apps(id) ON DELETE CASCADE,
    CONSTRAINT fk_branch_env_vars_branch FOREIGN KEY (branch_id) REFERENCES branches(id) ON DELETE CASCADE,
    CONSTRAINT uq_branch_env_var UNIQUE (app_id, branch_id, key)
);

CREATE INDEX idx_branch_env_vars_branch ON branch_env_vars (branch_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS branch_env_vars;
-- +goose StatementEnd
