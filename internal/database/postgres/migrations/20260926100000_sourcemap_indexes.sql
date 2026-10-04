-- +goose Up
-- +goose StatementBegin

-- One row per update whose source map the index job handles; the durable
-- record behind the dashboard, since River purges its own rows within days.
CREATE TABLE sourcemap_indexes (
    branch_id BIGINT NOT NULL,
    update_id BIGINT NOT NULL,
    hash TEXT NOT NULL,
    -- pending, running, stored, failed, cancelled
    status VARCHAR(20) NOT NULL,
    reason TEXT,
    segments INT,
    index_size BIGINT,
    attempts INT NOT NULL DEFAULT 0,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT pk_sourcemap_indexes PRIMARY KEY (branch_id, update_id),
    CONSTRAINT fk_sourcemap_indexes_update FOREIGN KEY (branch_id, update_id) REFERENCES updates(branch_id, id) ON DELETE CASCADE
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS sourcemap_indexes;

-- +goose StatementEnd
