-- +goose Up

-- content_key replaces content_hash as the identity a retried row is folded
-- onto at read time. A straight replacement, not a second column beside it:
-- observe has never shipped, so no deployment carries rows written with the
-- old fingerprint and there is no era to keep readable.
--
-- Two things were wrong with content_hash, and only one of them is width.
--
-- The width: 64 bits collides by birthday at roughly 2.7% once an app holds a
-- billion distinct contents, and a collision here is silent data loss, since
-- the reads GROUP BY it and one of the two rows simply stops being shown.
--
-- The shape, which was the worse half: parts were joined with a NUL separator
-- and no length, so a NUL inside a part could take the place of a separator.
-- Two DIFFERENT metric points, one with routeName "/checkout" and customParams
-- "b\0c", the other with routeName "/checkout\0b" and customParams "c", fed
-- the hash the same bytes and collided. Both fields arrive raw from an
-- unauthenticated public endpoint, so that collision was chosen rather than
-- drawn: it let a caller make someone else's row disappear from a view.
-- Length-prefixing the parts is what fixes that; a wider hash alone would not
-- have, because the two inputs really were the same bytes.
--
-- UUID rather than UInt128: ClickHouse stores it as 128 bits and the Go driver
-- speaks it natively as [16]byte, where UInt128 costs a big.Int allocation per
-- row on the insert path.
ALTER TABLE observe_logs ADD COLUMN IF NOT EXISTS content_key UUID DEFAULT toUUID('00000000-0000-0000-0000-000000000000');
ALTER TABLE observe_metrics ADD COLUMN IF NOT EXISTS content_key UUID DEFAULT toUUID('00000000-0000-0000-0000-000000000000');
ALTER TABLE observe_logs DROP COLUMN IF EXISTS content_hash;
ALTER TABLE observe_metrics DROP COLUMN IF EXISTS content_hash;

-- +goose Down
ALTER TABLE observe_logs ADD COLUMN IF NOT EXISTS content_hash UInt64 DEFAULT 0;
ALTER TABLE observe_metrics ADD COLUMN IF NOT EXISTS content_hash UInt64 DEFAULT 0;
ALTER TABLE observe_metrics DROP COLUMN IF EXISTS content_key;
ALTER TABLE observe_logs DROP COLUMN IF EXISTS content_key;
