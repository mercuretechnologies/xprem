package repository_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"xprem/internal/database"
	"xprem/internal/database/postgres"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/repository"
	"xprem/internal/types"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

type buildStoreFixture struct {
	builds      *repository.PostgresBuildRepository
	identifiers *repository.PostgresAppIdentifierRepository
	pool        *pgxpool.Pool
	app         string
	identifier  string
}

func setupBuildStore(t *testing.T) *buildStoreFixture {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_DATABASE_URL must be set in CI: these tests cover SQL constraints the in-memory fakes cannot reach")
		}
		t.Skip("TEST_DATABASE_URL not set, start a Postgres and set it to run the build store tests")
	}
	t.Setenv("ADMIN_EMAIL", "seed-admin@example.com")
	t.Setenv("ADMIN_PASSWORD", "Sup3rSecret!")
	postgres.RunDBMigrations(dbURL)
	pool, err := pgxpool.New(context.Background(), dbURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	engine := &database.Engine{Queries: pgdb.New(pool), DB: pool}
	f := &buildStoreFixture{builds: repository.NewPostgresBuildRepository(engine), identifiers: repository.NewPostgresAppIdentifierRepository(engine), pool: pool}
	f.app = insertBareApp(t, pool)
	f.identifier = insertIdentifier(t, f.identifiers, f.app, types.PlatformAndroid, "com.example.builds")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM apps WHERE id = $1", f.app)
	})
	return f
}

func (f *buildStoreFixture) record(id string, status types.BuildStatus) types.BuildRecord {
	return types.BuildRecord{
		ID: id, AppID: f.app, AppIdentifierID: f.identifier, Platform: types.PlatformAndroid, ApplicationID: "com.example.builds", Status: status, ArtifactType: types.BuildArtifactAPK,
		ArtifactKey: "builds/android/" + f.identifier + "/" + id + ".apk", ActorType: "api_key", ActorID: "7", ActorDisplay: "ci",
		Metadata: types.BuildMetadata{Profile: "production", Mode: "release", Channel: "stable", CLIVersion: "1.0.0", GitCommit: "abc", GitDirty: true, StartedAt: time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)},
	}
}

func withArtifact(record types.BuildRecord) types.BuildRecord {
	record.Size = 1234
	record.SHA256 = strings.Repeat("ab", 32)
	record.Metadata.BuildNumber = "42"
	record.Metadata.Version = "1.2.3"
	record.Metadata.Fingerprint = strings.Repeat("f", 40)
	record.Metadata.FinishedAt = record.Metadata.StartedAt.Add(5 * time.Minute)
	record.Metadata.DurationMs = 5 * 60 * 1000
	return record
}

func TestBuildStoreLifecycle(t *testing.T) {
	f := setupBuildStore(t)
	ctx := context.Background()
	id := uuid.NewString()

	created, inserted, err := f.builds.Create(ctx, f.record(id, types.BuildStatusBuilding))
	require.NoError(t, err)
	require.True(t, inserted)
	require.Equal(t, types.BuildStatusBuilding, created.Status)
	require.Equal(t, "release", created.Metadata.Mode)
	require.Equal(t, "stable", created.Metadata.Channel)
	require.Equal(t, "com.example.builds", created.ApplicationID)
	require.Zero(t, created.Size)
	require.True(t, created.Metadata.FinishedAt.IsZero())
	require.Zero(t, created.Metadata.DurationMs)
	require.Nil(t, created.ReadyAt)
	require.False(t, created.CreatedAt.IsZero())
	require.True(t, created.Metadata.StartedAt.Equal(time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)))

	changed := f.record(id, types.BuildStatusBuilding)
	changed.Metadata.Profile = "preview"
	existing, inserted, err := f.builds.Create(ctx, changed)
	require.NoError(t, err)
	require.False(t, inserted, "a second create returns the stored row untouched")
	require.Equal(t, "production", existing.Metadata.Profile)

	var stored []byte
	require.NoError(t, f.pool.QueryRow(ctx, "SELECT metadata::text FROM builds WHERE id = $1", id).Scan(&stored))
	require.NotContains(t, string(stored), "startedAt", "timing lives in columns, not in the JSON document")
	require.NotContains(t, string(stored), "finishedAt")

	uploading, err := f.builds.Transition(ctx, f.app, id, func(current types.BuildRecord) (*types.BuildRecord, error) {
		require.Equal(t, types.BuildStatusBuilding, current.Status)
		next := withArtifact(current)
		next.Status = types.BuildStatusUploading
		return &next, nil
	})
	require.NoError(t, err)
	require.Equal(t, types.BuildStatusUploading, uploading.Status)
	require.Equal(t, int64(1234), uploading.Size)
	require.Equal(t, "42", uploading.Metadata.BuildNumber)
	require.Equal(t, int64(300000), uploading.Metadata.DurationMs)
	require.True(t, uploading.Metadata.FinishedAt.Equal(created.Metadata.StartedAt.Add(5*time.Minute)))
	require.Nil(t, uploading.ReadyAt)
	require.False(t, uploading.UpdatedAt.Before(created.UpdatedAt))

	unchanged, err := f.builds.Transition(ctx, f.app, id, func(types.BuildRecord) (*types.BuildRecord, error) { return nil, nil })
	require.NoError(t, err)
	require.Equal(t, uploading.UpdatedAt, unchanged.UpdatedAt)

	sentinel := errors.New("verification failed")
	_, err = f.builds.Transition(ctx, f.app, id, func(types.BuildRecord) (*types.BuildRecord, error) { return nil, sentinel })
	require.ErrorIs(t, err, sentinel)

	ready, err := f.builds.Transition(ctx, f.app, id, func(current types.BuildRecord) (*types.BuildRecord, error) {
		next := current
		next.Status = types.BuildStatusReady
		return &next, nil
	})
	require.NoError(t, err)
	require.Equal(t, types.BuildStatusReady, ready.Status)
	require.NotNil(t, ready.ReadyAt)

	again, err := f.builds.Transition(ctx, f.app, id, func(current types.BuildRecord) (*types.BuildRecord, error) { return &current, nil })
	require.NoError(t, err)
	require.Equal(t, ready.ReadyAt.UnixMicro(), again.ReadyAt.UnixMicro(), "ready_at is set once")

	fetched, err := f.builds.Get(ctx, f.app, id)
	require.NoError(t, err)
	require.Equal(t, types.BuildStatusReady, fetched.Status)
	require.Equal(t, "release", fetched.Metadata.Mode)
	require.Equal(t, "stable", fetched.Metadata.Channel)
	require.Equal(t, "7", fetched.ActorID)

	failedID := uuid.NewString()
	failedRecord := f.record(failedID, types.BuildStatusBuilding)
	_, _, err = f.builds.Create(ctx, failedRecord)
	require.NoError(t, err)
	failed, err := f.builds.Transition(ctx, f.app, failedID, func(current types.BuildRecord) (*types.BuildRecord, error) {
		next := current
		next.Status = types.BuildStatusFailed
		next.Metadata.FinishedAt = current.Metadata.StartedAt.Add(time.Minute)
		next.Metadata.DurationMs = 60000
		return &next, nil
	})
	require.NoError(t, err)
	require.Equal(t, types.BuildStatusFailed, failed.Status)
	require.Zero(t, failed.Size)
	require.Equal(t, int64(60000), failed.Metadata.DurationMs)

	list, count, err := f.builds.List(ctx, f.app, 10, 0, nil)
	require.NoError(t, err)
	require.Equal(t, int64(2), count)
	require.Len(t, list, 2)
	require.Equal(t, failedID, list[0].ID, "newest first")
	page, count, err := f.builds.List(ctx, f.app, 1, 1, nil)
	require.NoError(t, err)
	require.Equal(t, int64(2), count)
	require.Len(t, page, 1)
	require.Equal(t, id, page[0].ID)
	require.Equal(t, "release", page[0].Metadata.Mode)
	require.Equal(t, "stable", page[0].Metadata.Channel)

	var missing *repository.ErrResourceNotFound
	_, err = f.builds.Get(ctx, uuid.NewString(), id)
	require.ErrorAs(t, err, &missing, "another app cannot read the build")
	_, err = f.builds.Transition(ctx, uuid.NewString(), id, func(types.BuildRecord) (*types.BuildRecord, error) { t.Fatal("must not run"); return nil, nil })
	require.ErrorAs(t, err, &missing)
	_, err = f.builds.Get(ctx, f.app, uuid.NewString())
	require.ErrorAs(t, err, &missing)
}

func constraintName(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.ConstraintName
	}
	return ""
}

func TestBuildStoreConstraints(t *testing.T) {
	f := setupBuildStore(t)
	ctx := context.Background()

	_, _, err := f.builds.Create(ctx, f.record(uuid.NewString(), types.BuildStatusUploading))
	require.Equal(t, "builds_artifact_required", constraintName(err), "uploading needs a declared artifact")
	_, _, err = f.builds.Create(ctx, f.record(uuid.NewString(), types.BuildStatusReady))
	require.Equal(t, "builds_artifact_required", constraintName(err))

	failedNoTime := f.record(uuid.NewString(), types.BuildStatusFailed)
	_, _, err = f.builds.Create(ctx, failedNoTime)
	require.Equal(t, "builds_finished", constraintName(err), "failed needs a finish time")

	buildingFinished := f.record(uuid.NewString(), types.BuildStatusBuilding)
	buildingFinished.Metadata.FinishedAt = buildingFinished.Metadata.StartedAt.Add(time.Minute)
	_, _, err = f.builds.Create(ctx, buildingFinished)
	require.Equal(t, "builds_finished", constraintName(err), "building cannot already be finished")

	halfArtifact := f.record(uuid.NewString(), types.BuildStatusBuilding)
	halfArtifact.Size = 10
	_, _, err = f.builds.Create(ctx, halfArtifact)
	require.Equal(t, "builds_artifact_declared", constraintName(err), "size and checksum come together")

	uploading := withArtifact(f.record(uuid.NewString(), types.BuildStatusUploading))
	uploading.Metadata.FinishedAt, uploading.Metadata.DurationMs = time.Time{}, 0
	_, _, err = f.builds.Create(ctx, uploading)
	require.Equal(t, "builds_finished", constraintName(err), "an uploading artifact reports when compilation finished")

	durationOnly := f.record(uuid.NewString(), types.BuildStatusBuilding)
	durationOnly.Metadata.DurationMs = 1000
	_, _, err = f.builds.Create(ctx, durationOnly)
	require.Equal(t, "builds_duration", constraintName(err), "a duration needs a finish time")

	noStart := f.record(uuid.NewString(), types.BuildStatusBuilding)
	noStart.Metadata.StartedAt = time.Time{}
	_, _, err = f.builds.Create(ctx, noStart)
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr, "a build cannot start at the zero time")
	require.Equal(t, "started_at", pgErr.ColumnName)

	uploading = withArtifact(f.record(uuid.NewString(), types.BuildStatusUploading))
	uploading.SHA256 = strings.ToUpper(uploading.SHA256)
	_, _, err = f.builds.Create(ctx, uploading)
	require.Contains(t, constraintName(err), "sha256")

	uploading = withArtifact(f.record(uuid.NewString(), types.BuildStatusUploading))
	uploading.Size = 2147483649
	_, _, err = f.builds.Create(ctx, uploading)
	require.Contains(t, constraintName(err), "size")

	uploading = withArtifact(f.record(uuid.NewString(), types.BuildStatusUploading))
	uploading.Status = "compiling"
	_, _, err = f.builds.Create(ctx, uploading)
	require.Contains(t, constraintName(err), "status")

	iosApk := f.record(uuid.NewString(), types.BuildStatusBuilding)
	iosApk.Platform = types.PlatformIOS
	_, _, err = f.builds.Create(ctx, iosApk)
	require.Equal(t, "builds_artifact_platform", constraintName(err), "an apk is an android artifact")

	ok := withArtifact(f.record(uuid.NewString(), types.BuildStatusUploading))
	_, inserted, err := f.builds.Create(ctx, ok)
	require.NoError(t, err)
	require.True(t, inserted)
	duplicateKey := withArtifact(f.record(uuid.NewString(), types.BuildStatusUploading))
	duplicateKey.ArtifactKey = ok.ArtifactKey
	_, _, err = f.builds.Create(ctx, duplicateKey)
	require.Equal(t, "builds_artifact_key_key", constraintName(err), "two builds cannot publish to the same object")

	_, err = f.pool.Exec(ctx, "UPDATE builds SET status='ready' WHERE id=$1", ok.ID)
	require.Equal(t, "builds_ready_at", constraintName(err), "ready rows carry ready_at")
	_, err = f.pool.Exec(ctx, "UPDATE builds SET ready_at=now() WHERE id=$1", ok.ID)
	require.Equal(t, "builds_ready_at", constraintName(err), "only ready rows carry ready_at")
	_, err = f.pool.Exec(ctx, "UPDATE builds SET duration_ms=NULL WHERE id=$1", ok.ID)
	require.Equal(t, "builds_duration", constraintName(err))
	_, err = f.pool.Exec(ctx, "UPDATE builds SET duration_ms=-1 WHERE id=$1", ok.ID)
	require.Contains(t, constraintName(err), "duration_ms")

	otherApp := insertBareApp(t, f.pool)
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), "DELETE FROM apps WHERE id = $1", otherApp) })
	otherIdentifier := insertIdentifier(t, f.identifiers, otherApp, types.PlatformAndroid, "com.example.other")
	crossApp := f.record(uuid.NewString(), types.BuildStatusBuilding)
	crossApp.AppIdentifierID = otherIdentifier
	crossApp.ArtifactKey = "builds/android/" + otherIdentifier + "/" + crossApp.ID + ".apk"
	_, _, err = f.builds.Create(ctx, crossApp)
	require.Equal(t, "builds_app_id_app_identifier_id_fkey", constraintName(err), "an identifier cannot be claimed by a foreign app")

	require.NoError(t, f.identifiers.DeleteAppIdentifier(ctx, f.app, f.identifier))
	var remaining int
	require.NoError(t, f.pool.QueryRow(ctx, "SELECT count(*) FROM builds WHERE app_identifier_id=$1", f.identifier).Scan(&remaining))
	require.Zero(t, remaining, "builds follow their identifier")
}

func TestBuildStoreFailWaitsForCompletion(t *testing.T) {
	f := setupBuildStore(t)
	ctx := context.Background()
	id := uuid.NewString()
	_, _, err := f.builds.Create(ctx, withArtifact(f.record(id, types.BuildStatusUploading)))
	require.NoError(t, err)

	locked := make(chan struct{})
	release := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	var completed *types.BuildRecord
	var completeErr error
	go func() {
		defer wg.Done()
		completed, completeErr = f.builds.Transition(ctx, f.app, id, func(current types.BuildRecord) (*types.BuildRecord, error) {
			close(locked)
			<-release
			next := current
			next.Status = types.BuildStatusReady
			return &next, nil
		})
	}()
	<-locked

	failDone := make(chan struct{})
	var failed *types.BuildRecord
	var failErr error
	go func() {
		defer close(failDone)
		failed, failErr = f.builds.Transition(ctx, f.app, id, func(current types.BuildRecord) (*types.BuildRecord, error) {
			if current.Status == types.BuildStatusReady {
				return nil, nil
			}
			next := current
			next.Status = types.BuildStatusFailed
			return &next, nil
		})
	}()
	select {
	case <-failDone:
		t.Fatal("the failure report must wait for the row lock held by completion")
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	wg.Wait()
	<-failDone
	require.NoError(t, completeErr)
	require.NoError(t, failErr)
	require.Equal(t, types.BuildStatusReady, completed.Status)
	require.Equal(t, types.BuildStatusReady, failed.Status, "the late failure sees the committed publication")
	current, err := f.builds.Get(ctx, f.app, id)
	require.NoError(t, err)
	require.Equal(t, types.BuildStatusReady, current.Status)
	require.NotNil(t, current.ReadyAt)
}

func TestBuildStoreShares(t *testing.T) {
	f := setupBuildStore(t)
	ctx := context.Background()
	id := uuid.NewString()
	_, _, err := f.builds.Create(ctx, withArtifact(f.record(id, types.BuildStatusUploading)))
	require.NoError(t, err)
	hash := strings.Repeat("1", 64)
	share, err := f.builds.CreateShare(ctx, uuid.NewString(), id, hash, time.Now().Add(time.Hour))
	require.NoError(t, err)
	var missing *repository.ErrResourceNotFound
	_, _, err = f.builds.ResolveShare(ctx, hash)
	require.ErrorAs(t, err, &missing, "only ready builds resolve")

	_, err = f.builds.Transition(ctx, f.app, id, func(current types.BuildRecord) (*types.BuildRecord, error) {
		next := current
		next.Status = types.BuildStatusReady
		return &next, nil
	})
	require.NoError(t, err)
	resolved, expiry, err := f.builds.ResolveShare(ctx, hash)
	require.NoError(t, err)
	require.Equal(t, id, resolved.ID)
	require.Equal(t, share.ExpiresAt.UnixMicro(), expiry.UnixMicro())
	_, _, err = f.builds.ResolveShare(ctx, strings.Repeat("2", 64))
	require.ErrorAs(t, err, &missing)

	expiredHash := strings.Repeat("3", 64)
	_, err = f.builds.CreateShare(ctx, uuid.NewString(), id, expiredHash, time.Now().Add(-time.Second))
	require.NoError(t, err)
	_, _, err = f.builds.ResolveShare(ctx, expiredHash)
	require.ErrorAs(t, err, &missing)

	shares, err := f.builds.ListShares(ctx, id)
	require.NoError(t, err)
	require.Len(t, shares, 2)

	require.NoError(t, f.builds.RevokeShare(ctx, id, share.ID))
	_, _, err = f.builds.ResolveShare(ctx, hash)
	require.ErrorAs(t, err, &missing)
	require.NoError(t, f.builds.RevokeShare(ctx, id, share.ID), "revoking twice is harmless")
	require.ErrorAs(t, f.builds.RevokeShare(ctx, uuid.NewString(), share.ID), &missing, "shares are scoped to their build")
	require.ErrorAs(t, f.builds.RevokeShare(ctx, id, uuid.NewString()), &missing)

	aab := withArtifact(f.record(uuid.NewString(), types.BuildStatusUploading))
	aab.ArtifactType = "aab"
	aab.ArtifactKey = strings.TrimSuffix(aab.ArtifactKey, ".apk") + ".aab"
	_, _, err = f.builds.Create(ctx, aab)
	require.NoError(t, err)
	_, err = f.builds.Transition(ctx, f.app, aab.ID, func(current types.BuildRecord) (*types.BuildRecord, error) {
		next := current
		next.Status = types.BuildStatusReady
		return &next, nil
	})
	require.NoError(t, err)
	aabHash := strings.Repeat("4", 64)
	_, err = f.builds.CreateShare(ctx, uuid.NewString(), aab.ID, aabHash, time.Now().Add(time.Hour))
	require.NoError(t, err)
	_, _, err = f.builds.ResolveShare(ctx, aabHash)
	require.ErrorAs(t, err, &missing, "a store bundle is not installable from a link")

	ipa := withArtifact(f.record(uuid.NewString(), types.BuildStatusUploading))
	ipa.Platform, ipa.ArtifactType = types.PlatformIOS, types.BuildArtifactIPA
	ipa.ArtifactKey = strings.TrimSuffix(ipa.ArtifactKey, ".apk") + ".ipa"
	ipa.Metadata.Distribution = types.IosDistributionAdHoc
	_, _, err = f.builds.Create(ctx, ipa)
	require.NoError(t, err)
	_, err = f.builds.Transition(ctx, f.app, ipa.ID, func(current types.BuildRecord) (*types.BuildRecord, error) {
		next := current
		next.Status = types.BuildStatusReady
		return &next, nil
	})
	require.NoError(t, err)
	ipaHash := strings.Repeat("5", 64)
	_, err = f.builds.CreateShare(ctx, uuid.NewString(), ipa.ID, ipaHash, time.Now().Add(time.Hour))
	require.NoError(t, err)
	resolved, _, err = f.builds.ResolveShare(ctx, ipaHash)
	require.NoError(t, err)
	require.Equal(t, types.IosDistributionAdHoc, resolved.Metadata.Distribution)

	_, err = f.pool.Exec(ctx, "DELETE FROM apps WHERE id = $1", f.app)
	require.NoError(t, err)
	var remaining int
	require.NoError(t, f.pool.QueryRow(ctx, "SELECT count(*) FROM build_shares WHERE build_id=$1", id).Scan(&remaining))
	require.Zero(t, remaining, "shares follow their app")
}
