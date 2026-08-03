-- +goose Up
-- +goose StatementBegin

-- The pattern is what decides which branch names a device may read, so its
-- default must not be the permissive end. '' matches no branch (MatchPattern
-- compares literally when the pattern holds no "*", and branch names are never
-- empty), and validation.NamePattern refuses to write it back — so it is the
-- never-configured state and nothing else.
--
-- Only channels that never opened branch surfing are touched: a channel that
-- deliberately holds '*' keeps it.
ALTER TABLE channels ALTER COLUMN branch_surfing_pattern SET DEFAULT '';

UPDATE channels
SET branch_surfing_pattern = ''
WHERE NOT branch_surfing_enabled AND branch_surfing_pattern = '*';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE channels ALTER COLUMN branch_surfing_pattern SET DEFAULT '*';

UPDATE channels
SET branch_surfing_pattern = '*'
WHERE branch_surfing_pattern = '';

-- +goose StatementEnd
