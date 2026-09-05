-- +goose Up
-- +goose StatementBegin

-- One row per (target update, source update) pair the bundle diffing job
-- handles; the durable record behind the dashboard, since River purges its
-- own rows within days. Both updates belong to the same branch.
CREATE TABLE bundle_patches (
    branch_id BIGINT NOT NULL,
    target_update_id BIGINT NOT NULL,
    source_update_id BIGINT NOT NULL,
    -- pending, running, stored, skipped, failed, cancelled
    status VARCHAR(20) NOT NULL,
    reason TEXT,
    patch_size BIGINT,
    full_download_size BIGINT,
    attempts INT NOT NULL DEFAULT 0,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT pk_bundle_patches PRIMARY KEY (branch_id, target_update_id, source_update_id),
    CONSTRAINT fk_bundle_patches_target FOREIGN KEY (branch_id, target_update_id) REFERENCES updates(branch_id, id) ON DELETE CASCADE,
    CONSTRAINT fk_bundle_patches_source FOREIGN KEY (branch_id, source_update_id) REFERENCES updates(branch_id, id) ON DELETE CASCADE
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS bundle_patches;

-- +goose StatementEnd
