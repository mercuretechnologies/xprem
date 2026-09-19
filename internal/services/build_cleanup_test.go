package services

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
	"xprem/internal/bucket"
	"xprem/internal/database/postgres"
	"xprem/internal/database/postgres/pgtest"
	"xprem/internal/types"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// The Postgres-backed tests share TEST_DATABASE_URL with the store packages;
// pgtest serializes them so wholesale cleanups cannot wipe each other's rows.
func TestMain(m *testing.M) {
	os.Exit(pgtest.RunSerialized(m))
}

type recordingDeleter struct {
	mu      sync.Mutex
	deleted []string
	failOn  map[string]error
}

func (r *recordingDeleter) DeleteBuildArtifact(_ context.Context, ref bucket.BuildArtifact, staging bool) error {
	return r.delete(ref.Key(staging))
}

func (r *recordingDeleter) DeleteBuildCache(_ context.Context, ref bucket.BuildCacheObject) error {
	return r.delete(ref.Key())
}

func (r *recordingDeleter) delete(key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.failOn[key]; err != nil {
		return err
	}
	r.deleted = append(r.deleted, key)
	return nil
}

func (r *recordingDeleter) keys() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.deleted...)
}

func setupBuildCleanup(t *testing.T) (*pgxpool.Pool, *recordingDeleter, *BuildCleanup) {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_DATABASE_URL must be set in CI: these tests cover a trigger and locking SQL that no fake can reach")
		}
		t.Skip("TEST_DATABASE_URL not set, start a Postgres and set it to run the build cleanup tests")
	}
	t.Setenv("ADMIN_EMAIL", "seed-admin@example.com")
	t.Setenv("ADMIN_PASSWORD", "Sup3rSecret!")
	postgres.RunDBMigrations(dbURL)
	pool, err := pgxpool.New(context.Background(), dbURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	_, err = pool.Exec(context.Background(), "DELETE FROM build_artifact_cleanup")
	require.NoError(t, err)
	deleter := &recordingDeleter{failOn: map[string]error{}}
	return pool, deleter, NewBuildCleanup(pool, deleter)
}

func TestBuildCacheCleanupRetriesBucketFailure(t *testing.T) {
	pool, deleter, cleanup := setupBuildCleanup(t)
	ctx := context.Background()
	ref := bucket.BuildCacheObject{AppID: uuid.NewString(), IdentifierID: uuid.NewString(), Namespace: types.BuildCacheGradle, ID: uuid.NewString()}
	key := ref.Key()
	_, err := pool.Exec(ctx, "INSERT INTO build_cache_cleanup (id, app_id, app_identifier_id, namespace, size, due_at) VALUES ($1, $2, $3, $4, 2048, now())", ref.ID, ref.AppID, ref.IdentifierID, ref.Namespace)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = pool.Exec(ctx, "DELETE FROM build_cache_cleanup WHERE id = $1", ref.ID) })
	deleter.failOn[key] = errors.New("bucket unavailable")
	_, err = cleanup.SweepCache(ctx)
	require.NoError(t, err)
	var postponed bool
	require.NoError(t, pool.QueryRow(ctx, "SELECT due_at > now() FROM build_cache_cleanup WHERE id = $1", ref.ID).Scan(&postponed))
	require.True(t, postponed)
	require.NotContains(t, deleter.keys(), key)

	delete(deleter.failOn, key)
	_, err = pool.Exec(ctx, "UPDATE build_cache_cleanup SET due_at = now() WHERE id = $1", ref.ID)
	require.NoError(t, err)
	_, err = cleanup.SweepCache(ctx)
	require.NoError(t, err)
	require.Contains(t, deleter.keys(), key)
	require.ErrorIs(t, pool.QueryRow(ctx, "SELECT true FROM build_cache_cleanup WHERE id = $1", ref.ID).Scan(&postponed), pgx.ErrNoRows)
}

type cleanupFixture struct {
	appID        string
	identifierID string
}

func insertCleanupFixture(t *testing.T, pool *pgxpool.Pool) cleanupFixture {
	t.Helper()
	ctx := context.Background()
	appID := uuid.NewString()
	_, err := pool.Exec(ctx, "INSERT INTO apps (id, name) VALUES ($1, $2)", appID, "cleanup-"+appID[:8])
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DELETE FROM apps WHERE id = $1", appID) })
	identifierID := uuid.NewString()
	_, err = pool.Exec(ctx, "INSERT INTO app_identifiers (id, app_id, platform, identifier) VALUES ($1, $2, 'android', $3)", identifierID, appID, "com.example."+identifierID[:8])
	require.NoError(t, err)
	return cleanupFixture{appID: appID, identifierID: identifierID}
}

func (f cleanupFixture) insertBuild(t *testing.T, pool *pgxpool.Pool, status string, age time.Duration) bucket.BuildArtifact {
	t.Helper()
	ref := bucket.BuildArtifact{IdentifierID: f.identifierID, BuildID: uuid.NewString(), Type: types.BuildArtifactAPK}
	key := ref.Key(false)
	_, err := pool.Exec(context.Background(), `INSERT INTO builds (id, app_id, app_identifier_id, platform, application_id, status, artifact_type, size, sha256, artifact_key, metadata, actor_type, actor_id, actor_display, started_at, finished_at, duration_ms, ready_at, created_at, updated_at)
VALUES ($1, $2, $3, 'android', 'com.example.app', $4, 'apk',
        CASE WHEN $4 = 'building' THEN 0 ELSE 1 END,
        CASE WHEN $4 = 'building' THEN '' ELSE repeat('a', 64) END,
        $5, '{}', 'user', 'tester', 'Tester',
        now() - $6::interval,
        CASE WHEN $4 = 'building' THEN NULL ELSE now() - $6::interval END,
        CASE WHEN $4 = 'building' THEN NULL ELSE 1000 END,
        CASE WHEN $4 = 'ready' THEN now() - $6::interval ELSE NULL END,
        now() - $6::interval, now() - $6::interval)`,
		ref.BuildID, f.appID, f.identifierID, status, key, pgInterval(age))
	require.NoError(t, err)
	return ref
}

func pgInterval(d time.Duration) pgtype.Interval {
	return pgtype.Interval{Microseconds: d.Microseconds(), Valid: true}
}

func duration(i pgtype.Interval) time.Duration {
	return time.Duration(i.Microseconds)*time.Microsecond + time.Duration(i.Days)*24*time.Hour + time.Duration(i.Months)*30*24*time.Hour
}

func keysOf(t *testing.T, ref bucket.BuildArtifact) (final, staging string) {
	t.Helper()
	final = ref.Key(false)
	staging = ref.Key(true)
	return final, staging
}

func outboxRows(t *testing.T, pool *pgxpool.Pool, buildID string) int {
	t.Helper()
	var count int
	require.NoError(t, pool.QueryRow(context.Background(), "SELECT count(*) FROM build_artifact_cleanup WHERE build_id = $1", buildID).Scan(&count))
	return count
}

func makeDue(t *testing.T, pool *pgxpool.Pool, buildID string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), "UPDATE build_artifact_cleanup SET due_at = now() - interval '1 second' WHERE build_id = $1", buildID)
	require.NoError(t, err)
}

func TestBuildCleanupTriggerOutlivesTheCascade(t *testing.T) {
	pool, _, _ := setupBuildCleanup(t)
	ctx := context.Background()
	fixture := insertCleanupFixture(t, pool)
	first := fixture.insertBuild(t, pool, "ready", 0)
	second := fixture.insertBuild(t, pool, "uploading", 0)

	_, err := pool.Exec(ctx, "DELETE FROM app_identifiers WHERE id = $1", fixture.identifierID)
	require.NoError(t, err)
	for _, ref := range []bucket.BuildArtifact{first, second} {
		var platform, artifactType string
		var identifier string
		var dueIn pgtype.Interval
		require.NoError(t, pool.QueryRow(ctx, "SELECT platform, app_identifier_id::text, artifact_type, due_at - now() FROM build_artifact_cleanup WHERE build_id = $1", ref.BuildID).Scan(&platform, &identifier, &artifactType, &dueIn))
		require.Equal(t, "android", platform)
		require.Equal(t, fixture.identifierID, identifier)
		require.Equal(t, "apk", artifactType)
		require.Greater(t, duration(dueIn), 14*time.Minute, "the row waits out the signed upload expiry")
	}

	other := insertCleanupFixture(t, pool)
	third := other.insertBuild(t, pool, "failed", 0)
	_, err = pool.Exec(ctx, "DELETE FROM apps WHERE id = $1", other.appID)
	require.NoError(t, err)
	require.Equal(t, 1, outboxRows(t, pool, third.BuildID), "an app delete cascades through identifiers into the outbox")
}

func TestBuildCleanupDrainsDueRowsWithExactKeys(t *testing.T) {
	pool, deleter, cleanup := setupBuildCleanup(t)
	ctx := context.Background()
	fixture := insertCleanupFixture(t, pool)
	due := fixture.insertBuild(t, pool, "ready", 0)
	pending := fixture.insertBuild(t, pool, "ready", 0)
	_, err := pool.Exec(ctx, "DELETE FROM builds WHERE id = ANY($1)", []string{due.BuildID, pending.BuildID})
	require.NoError(t, err)
	makeDue(t, pool, due.BuildID)

	count, err := cleanup.DrainOutbox(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	final, staging := keysOf(t, due)
	require.ElementsMatch(t, []string{final, staging}, deleter.keys())
	require.Equal(t, 0, outboxRows(t, pool, due.BuildID))
	require.Equal(t, 1, outboxRows(t, pool, pending.BuildID), "a row inside the upload expiry window waits")
}

func TestBuildCleanupRetriesFailedDeletesWithBackoff(t *testing.T) {
	pool, deleter, cleanup := setupBuildCleanup(t)
	ctx := context.Background()
	fixture := insertCleanupFixture(t, pool)
	ref := fixture.insertBuild(t, pool, "ready", 0)
	final, staging := keysOf(t, ref)
	// The "staging: " prefix puts the first byte of é at the truncation limit.
	deleter.failOn[staging] = errors.New(strings.Repeat("a", 990) + "é")
	_, err := pool.Exec(ctx, "DELETE FROM builds WHERE id = $1", ref.BuildID)
	require.NoError(t, err)
	makeDue(t, pool, ref.BuildID)

	count, err := cleanup.DrainOutbox(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, count)
	var attempts int
	var lastError string
	var dueIn pgtype.Interval
	require.NoError(t, pool.QueryRow(ctx, "SELECT attempts, last_error, due_at - now() FROM build_artifact_cleanup WHERE build_id = $1", ref.BuildID).Scan(&attempts, &lastError, &dueIn))
	require.Equal(t, 1, attempts)
	require.Equal(t, "staging: "+strings.Repeat("a", 990), lastError)
	require.True(t, utf8.ValidString(lastError))
	require.Greater(t, duration(dueIn), 30*time.Second)
	require.Equal(t, []string{final}, deleter.keys(), "the final object was still removed on the failed attempt")

	count, err = cleanup.DrainOutbox(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, count, "a backed-off row is not retried early")

	delete(deleter.failOn, staging)
	makeDue(t, pool, ref.BuildID)
	count, err = cleanup.DrainOutbox(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.Equal(t, 0, outboxRows(t, pool, ref.BuildID))
	require.ElementsMatch(t, []string{final, staging, final}, deleter.keys(), "retries re-issue exact idempotent deletes")
}

func TestBuildCleanupSkipsRowsHeldByAnotherWorker(t *testing.T) {
	pool, deleter, cleanup := setupBuildCleanup(t)
	ctx := context.Background()
	fixture := insertCleanupFixture(t, pool)
	ref := fixture.insertBuild(t, pool, "ready", 0)
	_, err := pool.Exec(ctx, "DELETE FROM builds WHERE id = $1", ref.BuildID)
	require.NoError(t, err)
	makeDue(t, pool, ref.BuildID)

	holder, err := pool.BeginTx(ctx, pgx.TxOptions{})
	require.NoError(t, err)
	_, err = holder.Exec(ctx, "SELECT id FROM build_artifact_cleanup WHERE build_id = $1 FOR UPDATE", ref.BuildID)
	require.NoError(t, err)
	count, err := cleanup.DrainOutbox(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, count)
	require.Empty(t, deleter.keys())
	require.NoError(t, holder.Rollback(ctx))

	count, err = cleanup.DrainOutbox(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count)
}

func TestBuildStagingSweepOnlyTouchesStaleStaging(t *testing.T) {
	pool, deleter, cleanup := setupBuildCleanup(t)
	ctx := context.Background()
	fixture := insertCleanupFixture(t, pool)
	oldReady := fixture.insertBuild(t, pool, "ready", 25*time.Hour)
	oldFailed := fixture.insertBuild(t, pool, "failed", 25*time.Hour)
	oldUploading := fixture.insertBuild(t, pool, "uploading", 25*time.Hour)
	youngReady := fixture.insertBuild(t, pool, "ready", time.Hour)
	youngUploading := fixture.insertBuild(t, pool, "uploading", 23*time.Hour)
	oldBuilding := fixture.insertBuild(t, pool, "building", 25*time.Hour)
	retried := fixture.insertBuild(t, pool, "uploading", 25*time.Hour)
	_, err := pool.Exec(ctx, "UPDATE builds SET updated_at = now() WHERE id = $1", retried.BuildID)
	require.NoError(t, err)

	count, err := cleanup.SweepStaging(ctx)
	require.NoError(t, err)
	require.Equal(t, 3, count)
	var expected []string
	for _, ref := range []bucket.BuildArtifact{oldReady, oldFailed, oldUploading} {
		_, staging := keysOf(t, ref)
		expected = append(expected, staging)
	}
	require.ElementsMatch(t, expected, deleter.keys(), "only staging keys of stale builds are removed")
	for _, ref := range []bucket.BuildArtifact{youngReady, youngUploading, oldBuilding, retried} {
		_, staging := keysOf(t, ref)
		require.NotContains(t, deleter.keys(), staging)
	}

	count, err = cleanup.SweepStaging(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, count, "swept builds are not visited again")

	_, err = pool.Exec(ctx, "UPDATE build_staging_sweeps SET swept_at = now() - interval '2 days' WHERE build_id = ANY($1)", []string{oldReady.BuildID, oldUploading.BuildID})
	require.NoError(t, err)
	count, err = cleanup.SweepStaging(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count, "only a build that can still receive uploads is swept again")
	_, uploadingStaging := keysOf(t, oldUploading)
	require.Equal(t, uploadingStaging, deleter.keys()[len(deleter.keys())-1])
}

func TestBuildStagingSweepKeepsLedgerOnDeleteFailureAndSkipsLockedBuilds(t *testing.T) {
	pool, deleter, cleanup := setupBuildCleanup(t)
	ctx := context.Background()
	fixture := insertCleanupFixture(t, pool)
	failing := fixture.insertBuild(t, pool, "ready", 25*time.Hour)
	locked := fixture.insertBuild(t, pool, "uploading", 25*time.Hour)
	_, failingStaging := keysOf(t, failing)
	deleter.failOn[failingStaging] = errors.New("storage unavailable")

	holder, err := pool.BeginTx(ctx, pgx.TxOptions{})
	require.NoError(t, err)
	_, err = holder.Exec(ctx, "SELECT id FROM builds WHERE id = $1 FOR UPDATE", locked.BuildID)
	require.NoError(t, err)
	count, err := cleanup.SweepStaging(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, count)
	require.NoError(t, holder.Rollback(ctx))

	var ledger int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM build_staging_sweeps WHERE build_id = ANY($1)", []string{failing.BuildID, locked.BuildID}).Scan(&ledger))
	require.Equal(t, 0, ledger)

	delete(deleter.failOn, failingStaging)
	count, err = cleanup.SweepStaging(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, count)
	_, lockedStaging := keysOf(t, locked)
	require.ElementsMatch(t, []string{failingStaging, lockedStaging}, deleter.keys())
}

func TestFailStaleBuildsOnlyFailsAbandonedBuilds(t *testing.T) {
	pool, _, cleanup := setupBuildCleanup(t)
	ctx := context.Background()
	fixture := insertCleanupFixture(t, pool)
	oldBuilding := fixture.insertBuild(t, pool, "building", 25*time.Hour)
	oldUploading := fixture.insertBuild(t, pool, "uploading", 25*time.Hour)
	youngBuilding := fixture.insertBuild(t, pool, "building", 23*time.Hour)
	oldReady := fixture.insertBuild(t, pool, "ready", 25*time.Hour)

	count, err := cleanup.FailStaleBuilds(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, count)

	for ref, want := range map[bucket.BuildArtifact]string{oldBuilding: "failed", oldUploading: "failed", youngBuilding: "building", oldReady: "ready"} {
		var status string
		var durationMs *int64
		require.NoError(t, pool.QueryRow(ctx, "SELECT status, duration_ms FROM builds WHERE id = $1", ref.BuildID).Scan(&status, &durationMs))
		require.Equal(t, want, status)
		if ref == oldBuilding {
			require.NotNil(t, durationMs)
			require.GreaterOrEqual(t, *durationMs, (25 * time.Hour).Milliseconds())
		}
	}
}

func TestBuildCleanupStartStopsCleanly(t *testing.T) {
	pool, _, cleanup := setupBuildCleanup(t)
	_ = pool
	stop := cleanup.Start(context.Background())
	done := make(chan struct{})
	go func() {
		stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not return")
	}
}

func TestBuildCleanupBatchLimits(t *testing.T) {
	for _, tc := range []struct {
		name          string
		outbox        bool
		limit         int
		deletesPerRow int
	}{
		{name: "outbox", outbox: true, limit: 4, deletesPerRow: 2},
		{name: "staging", limit: 9, deletesPerRow: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool, deleter, cleanup := setupBuildCleanup(t)
			ctx := context.Background()
			fixture := insertCleanupFixture(t, pool)
			for i := 0; i < tc.limit+1; i++ {
				ref := fixture.insertBuild(t, pool, "ready", 25*time.Hour)
				if tc.outbox {
					_, err := pool.Exec(ctx, "DELETE FROM builds WHERE id = $1", ref.BuildID)
					require.NoError(t, err)
					makeDue(t, pool, ref.BuildID)
				}
			}
			run := cleanup.SweepStaging
			if tc.outbox {
				run = cleanup.DrainOutbox
			}
			count, err := run(ctx)
			require.NoError(t, err)
			require.Equal(t, tc.limit, count)
			require.Len(t, deleter.keys(), tc.limit*tc.deletesPerRow)
			count, err = run(ctx)
			require.NoError(t, err)
			require.Equal(t, 1, count, "the next batch handles only the remaining build")
			count, err = run(ctx)
			require.NoError(t, err)
			require.Zero(t, count, "completed work is committed and not selected again")
		})
	}
}

func TestTruncateErrorPreservesUTF8(t *testing.T) {
	for _, tc := range []struct {
		name    string
		message string
		want    string
	}{
		{name: "empty"},
		{name: "short unicode", message: "échec 🧹", want: "échec 🧹"},
		{name: "exact limit", message: strings.Repeat("é", 500), want: strings.Repeat("é", 500)},
		{name: "ASCII", message: strings.Repeat("a", 1001), want: strings.Repeat("a", 1000)},
		{name: "two bytes", message: strings.Repeat("a", 999) + "é", want: strings.Repeat("a", 999)},
		{name: "three bytes", message: strings.Repeat("a", 998) + "€", want: strings.Repeat("a", 998)},
		{name: "four bytes", message: strings.Repeat("a", 997) + "🧹", want: strings.Repeat("a", 997)},
		{name: "complete rune at limit", message: strings.Repeat("a", 998) + "éx", want: strings.Repeat("a", 998) + "é"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateError(errors.New(tc.message))
			require.True(t, utf8.ValidString(got))
			require.LessOrEqual(t, len(got), 1000)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestBuildOutboxBackoffIsBoundedAndGrows(t *testing.T) {
	require.Equal(t, time.Minute, buildOutboxBackoff(0))
	require.Equal(t, 2*time.Minute, buildOutboxBackoff(1))
	require.Equal(t, 8*time.Minute, buildOutboxBackoff(3))
	require.Equal(t, buildOutboxMaxBackoff, buildOutboxBackoff(9))
	require.Equal(t, buildOutboxMaxBackoff, buildOutboxBackoff(60))
	require.Equal(t, time.Minute, buildOutboxBackoff(-3))
}
