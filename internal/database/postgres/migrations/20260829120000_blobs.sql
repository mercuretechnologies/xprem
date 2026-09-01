-- +goose Up
-- +goose StatementBegin

-- One row per unique file bytes, per app. The object lives at {appId}/cas/{hash}.
CREATE TABLE blobs (
    app_id UUID NOT NULL REFERENCES apps (id) ON DELETE CASCADE,
    hash TEXT NOT NULL CHECK (char_length(hash) = 43),
    size BIGINT NOT NULL CHECK (size >= 0),
    content_type TEXT NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (app_id, hash)
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE blobs;

-- +goose StatementEnd
