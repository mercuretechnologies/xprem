-- +goose Up
-- +goose StatementBegin

-- Branch surfing: a device polling this channel may ask to be served a branch
-- other than the one the channel maps to. Off unless an admin turns it on, per
-- channel, so an existing deployment is unaffected by this migration.
ALTER TABLE channels
    ADD COLUMN branch_surfing_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    -- Which branches a device on this channel may reach. Same pattern language
    -- as api_key_branch_rules.pattern: a branch name, or a name with "*"
    -- standing for any run of characters ("pr-*", "*-eu", "*"). Sized like
    -- branches.name so a pattern can always name what exists.
    ADD COLUMN branch_surfing_pattern VARCHAR(255) NOT NULL DEFAULT '*';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE channels
    DROP COLUMN branch_surfing_enabled,
    DROP COLUMN branch_surfing_pattern;

-- +goose StatementEnd
