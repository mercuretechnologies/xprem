package bucket

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
)

// v2_migration.go, one-shot data re-path from the v1 bucket layout
// ({prefix}/{branch}/{rv}/{updateId}/…) to the v2 layout
// ({prefix}/{appId}/{branch}/{rv}/{updateId}/…). Driven by the migration
// 20260422_v2_scope_data_under_appid registered in internal/bucketmigrations/.
//
// Each backend exposes a MoveRootEntriesUnder(appId) method that is
// idempotent under interruption: entries already moved are detected
// (the appId prefix is preserved on retry) and re-running the migration
// only processes what's still at the root.
//
// .migrationhistory itself is deployment-global and explicitly excluded -
// it must stay at the bucket root so the migration ledger keeps working
// across deploys.

// ErrAppIdCollidesWithV1Branch is returned when a v1 bucket contains a
// branch whose name happens to equal the target appId. Auto-migrating
// would nest v1 data under itself and corrupt the layout, so the
// migration refuses and the operator renames the branch before retrying.
//
// EXPO_APP_ID is an Expo project id and therefore a UUID, so a branch
// colliding with it is close to unreachable in practice, the guard is
// here because the corruption it prevents is silent and unrecoverable,
// not because it is expected to fire.
var ErrAppIdCollidesWithV1Branch = fmt.Errorf("app id collides with a v1 branch of the same name; rename that branch in the bucket, then reboot to retry the migration")

// migrationConcurrency is how many objects MoveRootEntriesUnder moves in
// parallel on the remote backends, tunable with BUCKET_MIGRATION_CONCURRENCY.
// Copy+delete is a pair of network round-trips per object, so the move is
// latency-bound: the default cuts hours to minutes on large buckets while
// staying far below S3/GCS per-prefix rate limits.
func migrationConcurrency() int {
	if v := os.Getenv("BUCKET_MIGRATION_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
		log.Printf("⚠️ Ignoring invalid BUCKET_MIGRATION_CONCURRENCY=%q, using the default", v)
	}
	return 16
}

// moveProgress counts moved objects across workers and logs a heartbeat line
// every 500 objects, so a long migration shows life in the pod logs instead
// of going silent for its whole duration.
type moveProgress struct {
	moved atomic.Int64
}

func (p *moveProgress) tick() {
	if n := p.moved.Add(1); n%500 == 0 {
		log.Printf("🚚 [BUCKET] v1 re-path progress: %d objects moved...", n)
	}
}

// v1BranchTripleFromMarker returns (triple, true) iff relKey is exactly
// {branch}/{rv}/{updateId}/{marker} where {marker} is a v1-only sentinel
// file. The 4-segment requirement is important: the same sentinel at
// segment 5 ({appId}/{branch}/{rv}/{updateId}/.check) identifies a v2
// branch belonging to some OTHER app and must not seed a triple here,
// otherwise we'd re-parent that app's data under the current appId.
func v1BranchTripleFromMarker(relKey string) (string, bool) {
	parts := strings.Split(relKey, "/")
	if len(parts) != 4 {
		return "", false
	}
	if parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", false
	}
	if parts[3] != ".check" && parts[3] != "update-metadata.json" {
		return "", false
	}
	return parts[0] + "/" + parts[1] + "/" + parts[2], true
}

// inConfirmedTriple returns true when relKey's first three segments
// match a triple that was positively confirmed as v1 in pass 1. Any
// depth under the triple is allowed, v1 nested assets like
// branch/rv/updateId/assets/foo.png must be moved along with their
// branch.
func inConfirmedTriple(relKey string, confirmed map[string]bool) bool {
	parts := strings.SplitN(relKey, "/", 4)
	if len(parts) < 4 {
		return false
	}
	if parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return false
	}
	return confirmed[parts[0]+"/"+parts[1]+"/"+parts[2]]
}

// escapeKeyForCopySource URL-escapes an S3 object key for use in the
// CopySource header. The key is escaped per path segment so literal
// slashes separating segments survive, url.PathEscape on the whole key
// would turn them into %2F, which S3 accepts as part of the key but not
// as a bucket/key separator.
func escapeKeyForCopySource(key string) string {
	segs := strings.Split(key, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}
