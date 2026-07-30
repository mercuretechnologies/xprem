package _0250417_persist_uuid

import (
	"time"
	"xprem/internal/bucket"
	"xprem/internal/bucketmigration"
)

// 20250417_persist_uuid was written against the v1 single-app bucket layout
// ({prefix}/{branch}/{runtimeVersion}/{updateId}). Its purpose was to backfill
// update.json UUIDs on pre-existing v1 data.
//
// In v2 the migration is a no-op: v2 writes update.json at publish time, so
// any data created by a v2 server already satisfies the invariant this
// migration enforced. The only remaining path that can carry un-UUID-ed data
// is a v1→v2 in-place upgrade, and that path is handled by
// 20260422_v2_scope_data_under_appid, which re-paths v1 data into the new
// {appId}-scoped layout and preserves whatever UUIDs the v1 server had
// already written. Running the old iteration against the v2 layout would
// fail anyway: the outer loop used a bucket-level branch listing that no
// longer exists now that data is scoped by appId.
//
// Kept as a registered no-op to preserve the migration ledger entry so
// existing ledgers stay valid after upgrade.
func init() {
	bucketmigration.Register(bucketmigration.BaseMigration{
		Id:       "20250417_persist_uuid",
		Time:     time.Date(2025, 4, 17, 0, 0, 0, 0, time.UTC),
		UpFunc:   func(b bucket.Bucket) error { return nil },
		DownFunc: func(b bucket.Bucket) error { return nil },
	})
}
