package _0260422_v2_scope_data_under_appid

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"
	"xprem/internal/bucket"
	"xprem/internal/bucketmigration"
)

// 20260422_v2_scope_data_under_appid moves v1 bucket data into the v2
// {appId}-scoped layout exactly once on boot. v1 stored updates under
// {prefix}/{branch}/{runtimeVersion}/{updateId}/…; v2 requires
// {prefix}/{appId}/{branch}/…. Without this migration, a v1 deploy that
// upgrades in place loses visibility of every previously published update.
//
// Only runs on the single-app flat-env path (EXPO_APP_ID set). A
// control-plane deploy (DB mode) sets no EXPO_APP_ID, so this no-ops
// there, fresh v2 installs have no v1 root-level data to re-path.
//
// There is no opt-out: upgrading to v2 means accepting the re-path. The
// move is idempotent, so a run interrupted anywhere converges on retry.
func init() {
	bucketmigration.Register(bucketmigration.BaseMigration{
		Id:       "20260422_v2_scope_data_under_appid",
		Time:     time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC),
		UpFunc:   up,
		DownFunc: func(b *bucket.Bucket) error { return nil },
	})
}

func up(b *bucket.Bucket) error {
	appId := os.Getenv("EXPO_APP_ID")
	if appId == "" {
		return nil
	}
	log.Printf("🧱 v1→v2 bucket re-path: moving root entries under %q …", appId)
	if err := b.MoveRootEntriesUnder(context.Background(), appId); err != nil {
		return fmt.Errorf("bucket re-path: %w", err)
	}
	log.Println("✅ v1→v2 bucket re-path complete.")
	return nil
}
