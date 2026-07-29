package _0260422_v2_scope_data_under_appid

import (
	"expo-open-ota/internal/bucket"
	"expo-open-ota/internal/bucketmigration"
	"fmt"
	"log"
	"os"
	"time"
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
//
// The move is driven by the validated Bucket's underlying concrete
// backend (via bucket.UnwrapBucket + type assertion on
// *LocalBucket / *S3Bucket / *GCSBucket) because the validating
// decorator rejects root-level listing, it expects scoped appId args.
func init() {
	bucketmigration.Register(bucketmigration.BaseMigration{
		Id:       "20260422_v2_scope_data_under_appid",
		Time:     time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC),
		UpFunc:   up,
		DownFunc: func(b bucket.Bucket) error { return nil },
	})
}

func up(b bucket.Bucket) error {
	appId := os.Getenv("EXPO_APP_ID")
	if appId == "" {
		return nil
	}

	inner := bucket.UnwrapBucket(b)
	log.Printf("🧱 v1→v2 bucket re-path: moving root entries under %q …", appId)
	switch concrete := inner.(type) {
	case *bucket.LocalBucket:
		if err := concrete.MoveRootEntriesUnder(appId); err != nil {
			return fmt.Errorf("LocalBucket re-path: %w", err)
		}
	case *bucket.S3Bucket:
		if err := concrete.MoveRootEntriesUnder(appId); err != nil {
			return fmt.Errorf("S3Bucket re-path: %w", err)
		}
	case *bucket.GCSBucket:
		if err := concrete.MoveRootEntriesUnder(appId); err != nil {
			return fmt.Errorf("GCSBucket re-path: %w", err)
		}
	case *bucket.AzureBucket:
		// The Azure backend shipped after the v2 layout, so no v1 Azure
		// deployment can exist and there is nothing to re-path.
	default:
		return fmt.Errorf("unsupported bucket backend for v1-to-v2 migration: %T", inner)
	}
	log.Println("✅ v1→v2 bucket re-path complete.")
	return nil
}
