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

-- Restores the old default and nothing else. Rewriting '' back to '*' would
-- hand the widest possible pattern to every channel that simply never opened
-- branch surfing — including channels created after this migration, which never
-- held '*' at all — and the dashboard enables the switch as soon as a pattern is
-- present, so one click would then expose every branch in the app. A rollback
-- must not be able to widen access. Nothing needs the rewrite: '' matches no
-- branch under the old code too, so a channel left at '' simply stays unable to
-- surf, which is where it already was.
ALTER TABLE channels ALTER COLUMN branch_surfing_pattern SET DEFAULT '*';

-- +goose StatementEnd
