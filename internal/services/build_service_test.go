package services

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"xprem/internal/bucket"
	"xprem/internal/store"
	"xprem/internal/types"
	"xprem/internal/validation"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

const (
	testBuildApp        = "11111111-1111-4111-8111-111111111111"
	testBuildIdentifier = "22222222-2222-4222-8222-222222222222"
	testBuildID         = "3a3a3a3a-3a3a-4a3a-8a3a-3a3a3a3a3a3a"
	otherBuildID        = "4b4b4b4b-4b4b-4b4b-8b4b-4b4b4b4b4b4b"
)

type memoryBuildRepo struct {
	mu     sync.Mutex
	builds map[string]types.BuildRecord
	shares map[string]types.BuildShare
	logs   []types.BuildLogChunk
	err    error
}

func newMemoryBuildRepo() *memoryBuildRepo {
	return &memoryBuildRepo{builds: map[string]types.BuildRecord{}, shares: map[string]types.BuildShare{}}
}

func (r *memoryBuildRepo) Create(_ context.Context, record types.BuildRecord) (*types.BuildRecord, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return nil, false, r.err
	}
	if existing, ok := r.builds[record.ID]; ok {
		if existing.AppID != record.AppID {
			return nil, false, &store.ErrResourceNotFound{Resource: "build", Identifier: record.ID}
		}
		return &existing, false, nil
	}
	record.CreatedAt, record.UpdatedAt = time.Now(), time.Now()
	r.builds[record.ID] = record
	return &record, true, nil
}

func (r *memoryBuildRepo) Get(_ context.Context, appID, id string) (*types.BuildRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	record, ok := r.builds[id]
	if !ok || record.AppID != appID {
		return nil, &store.ErrResourceNotFound{Resource: "build", Identifier: id}
	}
	return &record, nil
}

func (r *memoryBuildRepo) List(_ context.Context, appID string, _, _ int32, _ *types.BuildCursor) ([]types.BuildRecord, int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var records []types.BuildRecord
	for _, record := range r.builds {
		if record.AppID == appID {
			records = append(records, record)
		}
	}
	return records, int64(len(records)), nil
}

func (r *memoryBuildRepo) Transition(ctx context.Context, appID, id string, decide func(types.BuildRecord) (*types.BuildRecord, error)) (*types.BuildRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	current, ok := r.builds[id]
	if !ok || current.AppID != appID {
		return nil, &store.ErrResourceNotFound{Resource: "build", Identifier: id}
	}
	next, err := decide(current)
	if err != nil {
		return nil, err
	}
	if next == nil {
		return &current, nil
	}
	if next.Status == types.BuildStatusReady {
		if next.ReadyAt == nil {
			now := time.Now()
			next.ReadyAt = &now
		}
	} else {
		next.ReadyAt = nil
	}
	next.UpdatedAt = time.Now()
	r.builds[id] = *next
	return next, nil
}

func (r *memoryBuildRepo) CreateShare(_ context.Context, id, buildID, hash string, expires time.Time) (types.BuildShare, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	share := types.BuildShare{ID: id, CreatedAt: time.Now(), ExpiresAt: expires}
	r.shares[hash] = share
	return share, nil
}

func (r *memoryBuildRepo) ListShares(context.Context, string) ([]types.BuildShare, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var shares []types.BuildShare
	for _, share := range r.shares {
		shares = append(shares, share)
	}
	return shares, nil
}

func (r *memoryBuildRepo) RevokeShare(context.Context, string, string) error { return nil }

func (r *memoryBuildRepo) AppendLogs(_ context.Context, _, _ string, offset int32, content string) error {
	r.logs = append(r.logs, types.BuildLogChunk{Offset: offset, Content: content})
	return nil
}

func (r *memoryBuildRepo) ListLogs(context.Context, string, string, int32) ([]types.BuildLogChunk, error) {
	return r.logs, nil
}

func TestBuildLogsValidateScopeAndSize(t *testing.T) {
	f := newBuildFixture(t)
	ctx := WithCliAuth(context.Background(), CliCredential{AppID: testBuildApp, KeyID: 7})
	_, err := f.service.Start(ctx, testBuildApp, testBuildIdentifier, testBuildID, f.startInput())
	require.NoError(t, err)
	content := `{"buildStepId":"general","buildStepDisplayName":"Build","time":"2026-09-09T10:00:00Z","level":30,"msg":"héllo"}` + "\n"
	require.NoError(t, f.service.AppendLogs(ctx, testBuildApp, testBuildIdentifier, testBuildID, 0, content))
	require.Len(t, f.repo.logs, 1)
	require.Error(t, f.service.AppendLogs(ctx, otherBuildID, testBuildIdentifier, testBuildID, int32(len(content)), content))
	require.Error(t, f.service.AppendLogs(ctx, testBuildApp, otherBuildID, testBuildID, int32(len(content)), content))
	for _, content := range []string{"", "invalid\x00", "invalid\xff", strings.Repeat("x", types.MaxBuildLogChunkBytes+1)} {
		require.Error(t, f.service.AppendLogs(ctx, testBuildApp, testBuildIdentifier, testBuildID, 6, content))
	}
	for _, offset := range []int32{-1, types.MaxBuildLogBytes, 2147483647} {
		require.Error(t, f.service.AppendLogs(ctx, testBuildApp, testBuildIdentifier, testBuildID, offset, content))
	}
	require.Len(t, f.repo.logs, 1)
	_, err = f.service.ListLogs(ctx, otherBuildID, testBuildID, 0)
	require.Error(t, err)
	_, err = f.service.ListLogs(ctx, testBuildApp, testBuildID, -1)
	require.Error(t, err)
	// Final output can arrive after the artifact or the failure has been reported.
	_, err = f.service.Fail(ctx, testBuildApp, testBuildIdentifier, testBuildID, FailBuildInput{FinishedAt: f.now})
	require.NoError(t, err)
	require.NoError(t, f.service.AppendLogs(ctx, testBuildApp, testBuildIdentifier, testBuildID, int32(len(content)), content))
}

func (r *memoryBuildRepo) ResolveShare(_ context.Context, hash string) (*types.BuildRecord, time.Time, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return nil, time.Time{}, r.err
	}
	share, ok := r.shares[hash]
	if !ok {
		return nil, time.Time{}, &store.ErrResourceNotFound{Resource: "share", Identifier: "link"}
	}
	for _, record := range r.builds {
		return &record, share.ExpiresAt, nil
	}
	return nil, time.Time{}, &store.ErrResourceNotFound{Resource: "share", Identifier: "link"}
}

type buildFixture struct {
	service *BuildService
	repo    *memoryBuildRepo
	storage *bucket.LocalBucket
	root    string
	now     time.Time
}

func newBuildFixture(t *testing.T) *buildFixture {
	t.Helper()
	t.Setenv("JWT_SECRET", "test-secret")
	identifiers := newFakeIdentifierRepo(testBuildApp)
	identifiers.add(testBuildIdentifier, types.PlatformAndroid, "com.example.app")
	identifiers.add(otherBuildID, types.PlatformIOS, "com.example.ios")
	root := t.TempDir()
	storage := &bucket.LocalBucket{BasePath: root}
	repo := newMemoryBuildRepo()
	service := NewBuildService(repo, identifiers, storage)
	now := time.Now().UTC().Truncate(time.Microsecond)
	service.now = func() time.Time { return now }
	return &buildFixture{service: service, repo: repo, storage: storage, root: root, now: now}
}

func (f *buildFixture) startInput() BuildStartInput {
	return BuildStartInput{ArtifactType: "apk", Metadata: BuildStartMetadata{Profile: "production", Mode: "release", Channel: "stable", CLIVersion: "1.2.3", GitCommit: "abc123", GitMessage: "release", GitDirty: true, StartedAt: f.now.Add(-10 * time.Minute),
		Machine: &types.BuildMachine{Hostname: "ci-mac-1", OS: "macOS 15.5", Arch: "arm64", Node: "22.11.0", Tools: map[string]string{"xcode": "16.4"}, CI: "GitHub Actions", CIRunURL: "https://github.com/acme/app/actions/runs/1"}}}
}

func (f *buildFixture) registerInput(content []byte) RegisterBuildInput {
	sum := sha256.Sum256(content)
	start := f.startInput().Metadata
	return RegisterBuildInput{ArtifactType: "apk", Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:]), Metadata: types.BuildMetadata{
		Profile: start.Profile, Mode: start.Mode, Channel: start.Channel, CLIVersion: start.CLIVersion, GitCommit: start.GitCommit, GitMessage: start.GitMessage, GitDirty: start.GitDirty, Machine: start.Machine, StartedAt: start.StartedAt,
		Version: "1.0.0", BuildNumber: "42", Fingerprint: strings.Repeat("a", 40), RuntimeVersion: "1.0.0", ExpoSDK: "54.0.0", FinishedAt: f.now.Add(-time.Minute), DurationMs: 999999,
	}}
}

func (f *buildFixture) stagedPath(t *testing.T, b types.BuildRecord) string {
	key := artifactRef(b).Key(true)
	return filepath.Join(f.root, key)
}

func (f *buildFixture) finalPath(t *testing.T, b types.BuildRecord) string {
	key := artifactRef(b).Key(false)
	return filepath.Join(f.root, key)
}

func TestBuildStartRecordsBuildingAndIsIdempotent(t *testing.T) {
	f := newBuildFixture(t)
	ctx := WithCliAuth(context.Background(), CliCredential{AppID: testBuildApp, KeyID: 7, KeyName: "ci-key"})
	input := f.startInput()
	input.Metadata.StartedAt = input.Metadata.StartedAt.Add(300 * time.Nanosecond)
	first, err := f.service.Start(ctx, testBuildApp, testBuildIdentifier, testBuildID, input)
	require.NoError(t, err)
	require.Equal(t, types.BuildStatusBuilding, first.Status)
	require.Equal(t, "com.example.app", first.ApplicationID)
	require.Equal(t, types.BuildArtifactAPK, first.ArtifactType)
	require.Equal(t, "release", first.Metadata.Mode)
	require.Zero(t, first.Size)
	require.Empty(t, first.SHA256)
	require.Empty(t, first.Metadata.Fingerprint)
	require.True(t, first.Metadata.FinishedAt.IsZero())
	require.Zero(t, first.Metadata.DurationMs)
	require.Equal(t, "api_key", first.ActorType)
	require.Equal(t, "7", first.ActorID)
	require.Equal(t, "ci-key", first.ActorDisplay)
	require.Equal(t, f.now.Add(-10*time.Minute), first.Metadata.StartedAt, "sub-microsecond precision is dropped before storage")

	again, err := f.service.Start(WithCliAuth(context.Background(), CliCredential{AppID: testBuildApp, KeyID: 9, KeyName: "other"}), testBuildApp, testBuildIdentifier, testBuildID, input)
	require.NoError(t, err)
	require.Equal(t, first.ID, again.ID)
	require.Equal(t, "7", again.ActorID, "the first actor is retained")
	require.Len(t, f.repo.builds, 1)

	changed := input
	changed.Metadata.Profile = "preview"
	_, err = f.service.Start(ctx, testBuildApp, testBuildIdentifier, testBuildID, changed)
	require.ErrorIs(t, err, ErrBuildConflict)
	changed = input
	changed.ArtifactType = "aab"
	_, err = f.service.Start(ctx, testBuildApp, testBuildIdentifier, testBuildID, changed)
	require.ErrorIs(t, err, ErrBuildConflict)
	changed = input
	changed.Metadata.GitDirty = false
	_, err = f.service.Start(ctx, testBuildApp, testBuildIdentifier, testBuildID, changed)
	require.ErrorIs(t, err, ErrBuildConflict)
	changed = input
	changed.Metadata.Mode = "debug"
	_, err = f.service.Start(ctx, testBuildApp, testBuildIdentifier, testBuildID, changed)
	require.ErrorIs(t, err, ErrBuildConflict)
	changedUpload := f.registerInput([]byte("apk"))
	changedUpload.Metadata.Mode = "debug"
	_, err = f.service.RegisterArtifact(ctx, testBuildApp, testBuildIdentifier, testBuildID, changedUpload)
	require.ErrorIs(t, err, ErrBuildConflict)
}

func TestBuildModePreservedAndOptionalForOlderClients(t *testing.T) {
	for _, mode := range []string{"release", "debug", ""} {
		t.Run("mode="+mode, func(t *testing.T) {
			f := newBuildFixture(t)
			input := f.startInput()
			input.Metadata.Mode = mode
			started, err := f.service.Start(context.Background(), testBuildApp, testBuildIdentifier, testBuildID, input)
			require.NoError(t, err)
			require.Equal(t, mode, started.Metadata.Mode)
			upload := f.registerInput([]byte("apk"))
			upload.Metadata.Mode = mode
			registered, err := f.service.RegisterArtifact(context.Background(), testBuildApp, testBuildIdentifier, testBuildID, upload)
			require.NoError(t, err)
			require.Equal(t, mode, registered.Build.Metadata.Mode)
		})
	}
}

func TestBuildStartValidation(t *testing.T) {
	f := newBuildFixture(t)
	ctx := context.Background()
	for name, mutate := range map[string]func(*BuildStartInput){
		"artifact type":  func(i *BuildStartInput) { i.ArtifactType = "ipa" },
		"profile":        func(i *BuildStartInput) { i.Metadata.Profile = "" },
		"mode":           func(i *BuildStartInput) { i.Metadata.Mode = "production" },
		"cli version":    func(i *BuildStartInput) { i.Metadata.CLIVersion = "" },
		"zero start":     func(i *BuildStartInput) { i.Metadata.StartedAt = time.Time{} },
		"future start":   func(i *BuildStartInput) { i.Metadata.StartedAt = f.now.Add(10 * time.Minute) },
		"long channel":   func(i *BuildStartInput) { i.Metadata.Channel = strings.Repeat("c", 256) },
		"long message":   func(i *BuildStartInput) { i.Metadata.GitMessage = strings.Repeat("m", 1001) },
		"long hostname":  func(i *BuildStartInput) { i.Metadata.Machine.Hostname = strings.Repeat("h", 256) },
		"script run url": func(i *BuildStartInput) { i.Metadata.Machine.CIRunURL = "javascript:alert(1)" },
	} {
		t.Run(name, func(t *testing.T) {
			input := f.startInput()
			mutate(&input)
			_, err := f.service.Start(ctx, testBuildApp, testBuildIdentifier, testBuildID, input)
			require.True(t, validation.IsValidationError(err), "%v", err)
			require.Empty(t, f.repo.builds)
		})
	}
	_, err := f.service.Start(ctx, testBuildApp, testBuildIdentifier, "not-a-uuid", f.startInput())
	require.True(t, validation.IsValidationError(err))
	_, err = f.service.Start(ctx, testBuildApp, testBuildIdentifier, strings.ToUpper(testBuildID), f.startInput())
	require.True(t, validation.IsValidationError(err), "only canonical lowercase UUIDs")
	_, err = f.service.Start(ctx, testBuildApp, otherBuildID, testBuildID, f.startInput())
	require.True(t, validation.IsValidationError(err), "iOS identifiers are refused")
	var missing *store.ErrResourceNotFound
	_, err = f.service.Start(ctx, testBuildApp, "55555555-5555-4555-8555-555555555555", testBuildID, f.startInput())
	require.ErrorAs(t, err, &missing)
	_, err = f.service.Start(ctx, "other-app", testBuildIdentifier, testBuildID, f.startInput())
	require.ErrorAs(t, err, &missing)
	require.Empty(t, f.repo.builds)

	stateless := NewBuildService(nil, nil, nil)
	_, err = stateless.Start(ctx, testBuildApp, testBuildIdentifier, testBuildID, f.startInput())
	require.ErrorIs(t, err, store.ErrNotSupportedInStatelessMode)
	_, err = stateless.Fail(ctx, testBuildApp, testBuildIdentifier, testBuildID, FailBuildInput{FinishedAt: time.Now().Add(-time.Hour)})
	require.ErrorIs(t, err, store.ErrNotSupportedInStatelessMode)
}

func TestBuildRegisterValidation(t *testing.T) {
	f := newBuildFixture(t)
	ctx := context.Background()
	for name, mutate := range map[string]func(*RegisterBuildInput){
		"size zero":          func(i *RegisterBuildInput) { i.Size = 0 },
		"size too large":     func(i *RegisterBuildInput) { i.Size = MaxBuildSize + 1 },
		"sha uppercase":      func(i *RegisterBuildInput) { i.SHA256 = strings.ToUpper(i.SHA256) },
		"sha short":          func(i *RegisterBuildInput) { i.SHA256 = i.SHA256[:63] },
		"version code zero":  func(i *RegisterBuildInput) { i.Metadata.BuildNumber = "0" },
		"version code limit": func(i *RegisterBuildInput) { i.Metadata.BuildNumber = "2100000001" },
		"version code text":  func(i *RegisterBuildInput) { i.Metadata.BuildNumber = "1.2" },
		"fingerprint short":  func(i *RegisterBuildInput) { i.Metadata.Fingerprint = strings.Repeat("a", 39) },
		"fingerprint upper":  func(i *RegisterBuildInput) { i.Metadata.Fingerprint = strings.Repeat("A", 64) },
		"fingerprint empty":  func(i *RegisterBuildInput) { i.Metadata.Fingerprint = "" },
		"finished before":    func(i *RegisterBuildInput) { i.Metadata.FinishedAt = i.Metadata.StartedAt.Add(-time.Second) },
		"finished zero":      func(i *RegisterBuildInput) { i.Metadata.FinishedAt = time.Time{} },
		"finished future":    func(i *RegisterBuildInput) { i.Metadata.FinishedAt = f.now.Add(time.Hour) },
		"too long":           func(i *RegisterBuildInput) { i.Metadata.StartedAt = f.now.Add(-8 * 24 * time.Hour) },
		"profile":            func(i *RegisterBuildInput) { i.Metadata.Profile = "" },
		"mode":               func(i *RegisterBuildInput) { i.Metadata.Mode = "production" },
		"long version":       func(i *RegisterBuildInput) { i.Metadata.Version = strings.Repeat("v", 256) },
	} {
		t.Run(name, func(t *testing.T) {
			input := f.registerInput([]byte("apk"))
			mutate(&input)
			_, err := f.service.RegisterArtifact(ctx, testBuildApp, testBuildIdentifier, testBuildID, input)
			require.True(t, validation.IsValidationError(err), "%v", err)
			require.Empty(t, f.repo.builds)
		})
	}
	input := f.registerInput([]byte("apk"))
	input.Metadata.BuildNumber = "2100000000"
	input.Metadata.Fingerprint = strings.Repeat("f", 64)
	_, err := f.service.RegisterArtifact(ctx, testBuildApp, testBuildIdentifier, testBuildID, input)
	require.NoError(t, err)
}

func TestBuildRegistrationPlatformRules(t *testing.T) {
	for _, tc := range []struct {
		name         string
		platform     types.Platform
		artifactType types.BuildArtifactType
		buildNumber  string
		wantError    string
	}{
		{"android apk", types.PlatformAndroid, types.BuildArtifactAPK, "42", ""},
		{"android aab", types.PlatformAndroid, types.BuildArtifactAAB, "2100000000", ""},
		{"android dotted number", types.PlatformAndroid, types.BuildArtifactAPK, "1.2.3", "buildNumber"},
		{"android zero", types.PlatformAndroid, types.BuildArtifactAPK, "0", "buildNumber"},
		{"android overflow", types.PlatformAndroid, types.BuildArtifactAPK, "2100000001", "buildNumber"},
		{"ios dotted number", types.PlatformIOS, types.BuildArtifactIPA, "1.2.3", ""},
		{"ios beyond android limit", types.PlatformIOS, types.BuildArtifactIPA, "2100000001", ""},
		{"ios invalid number", types.PlatformIOS, types.BuildArtifactIPA, "1.02.3", "buildNumber"},
		{"ipa for android", types.PlatformAndroid, types.BuildArtifactIPA, "42", "does not match identifier platform"},
		{"apk for ios", types.PlatformIOS, types.BuildArtifactAPK, "42", "does not match identifier platform"},
		{"aab for ios", types.PlatformIOS, types.BuildArtifactAAB, "42", "does not match identifier platform"},
		{"unknown artifact", types.PlatformAndroid, "zip", "42", "unsupported build artifact type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBuildFixture(t)
			input := f.registerInput([]byte("artifact"))
			input.ArtifactType = tc.artifactType
			input.Metadata.BuildNumber = tc.buildNumber
			err := f.service.validateRegister(tc.platform, &input)
			if tc.wantError == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.wantError)
				require.True(t, validation.IsValidationError(err))
			}
		})
	}
}

func TestBuildAcceptsIosArtifacts(t *testing.T) {
	f := newBuildFixture(t)
	input := f.startInput()
	input.ArtifactType = types.BuildArtifactIPA
	build, err := f.service.Start(context.Background(), testBuildApp, otherBuildID, testBuildID, input)
	require.NoError(t, err)
	require.Equal(t, types.PlatformIOS, build.Platform)
	require.Equal(t, types.BuildArtifactIPA, build.ArtifactType)
}

func TestBuildRejectsArtifactForAnotherPlatform(t *testing.T) {
	for _, operation := range []string{"start", "register"} {
		t.Run(operation, func(t *testing.T) {
			f := newBuildFixture(t)
			f.service.storage = nil
			var err error
			if operation == "start" {
				input := f.startInput()
				input.ArtifactType = types.BuildArtifactIPA
				_, err = f.service.Start(context.Background(), testBuildApp, testBuildIdentifier, testBuildID, input)
			} else {
				input := f.registerInput([]byte("artifact"))
				input.ArtifactType = types.BuildArtifactIPA
				_, err = f.service.RegisterArtifact(context.Background(), testBuildApp, testBuildIdentifier, testBuildID, input)
			}
			require.ErrorContains(t, err, "does not match identifier platform")
			require.True(t, validation.IsValidationError(err))
			require.Empty(t, f.repo.builds)
		})
	}
}

func TestBuildRegistrationLocalUploadURL(t *testing.T) {
	for _, tc := range []struct {
		baseURL string
		wantURL string
	}{
		{baseURL: "https://ota.example.com", wantURL: "https://ota.example.com/" + testBuildApp + "/build/" + testBuildIdentifier + "/artifacts/" + testBuildID + "/upload"},
		{baseURL: "https://ota.example.com/sub/path/", wantURL: "https://ota.example.com/sub/path/" + testBuildApp + "/build/" + testBuildIdentifier + "/artifacts/" + testBuildID + "/upload"},
	} {
		t.Run(tc.baseURL, func(t *testing.T) {
			f := newBuildFixture(t)
			t.Setenv("BASE_URL", tc.baseURL)
			registration, err := f.service.RegisterArtifact(context.Background(), testBuildApp, testBuildIdentifier, testBuildID, f.registerInput([]byte("apk")))
			require.NoError(t, err)
			require.NotNil(t, registration.Upload)
			require.Equal(t, tc.wantURL, registration.Upload.URL)
			require.Equal(t, "PUT", registration.Upload.Method)
			require.NotEmpty(t, registration.Upload.Headers[bucket.LocalUploadTokenHeader])
		})
	}
}

func TestBuildLifecycleStartUploadComplete(t *testing.T) {
	f := newBuildFixture(t)
	ctx := WithCliAuth(context.Background(), CliCredential{AppID: testBuildApp, KeyID: 7, KeyName: "ci-key"})
	content := []byte("signed apk bytes")
	started, err := f.service.Start(ctx, testBuildApp, testBuildIdentifier, testBuildID, f.startInput())
	require.NoError(t, err)

	registration, err := f.service.RegisterArtifact(context.Background(), testBuildApp, testBuildIdentifier, testBuildID, f.registerInput(content))
	require.NoError(t, err)
	b := registration.Build
	require.Equal(t, types.BuildStatusUploading, b.Status)
	require.Equal(t, int64(len(content)), b.Size)
	require.Equal(t, "42", b.Metadata.BuildNumber)
	require.Equal(t, "stable", b.Metadata.Channel)
	require.Equal(t, "release", b.Metadata.Mode)
	require.Equal(t, started.CreatedAt, b.CreatedAt)
	require.Equal(t, "7", b.ActorID, "the starting actor is retained through the upload")
	require.Equal(t, int64(9*time.Minute/time.Millisecond), b.Metadata.DurationMs, "duration is computed by the server, not taken from the client")
	require.NotNil(t, registration.Upload)
	require.Equal(t, "PUT", registration.Upload.Method)
	require.NotEmpty(t, registration.Upload.URL, "registration returns complete upload instructions")
	require.NotEmpty(t, registration.Upload.Headers[bucket.LocalUploadTokenHeader])

	retry, err := f.service.RegisterArtifact(context.Background(), testBuildApp, testBuildIdentifier, testBuildID, f.registerInput(content))
	require.NoError(t, err)
	require.Equal(t, types.BuildStatusUploading, retry.Build.Status)
	require.NotEmpty(t, retry.Upload.Headers[bucket.LocalUploadTokenHeader], "an identical retry gets a fresh upload grant")

	_, err = f.service.Complete(ctx, testBuildApp, testBuildIdentifier, testBuildID)
	require.ErrorIs(t, err, ErrBuildIntegrity, "nothing staged yet")
	afterMissing, err := f.service.Get(ctx, testBuildApp, testBuildID)
	require.NoError(t, err)
	require.Equal(t, types.BuildStatusFailed, afterMissing.Status)
	require.Equal(t, f.now, afterMissing.Metadata.FinishedAt, "failure time is the server clock")
	require.Equal(t, int64(len(content)), afterMissing.Size, "the declared artifact is kept on a failed upload")

	retry, err = f.service.RegisterArtifact(context.Background(), testBuildApp, testBuildIdentifier, testBuildID, f.registerInput(content))
	require.NoError(t, err)
	require.Equal(t, types.BuildStatusUploading, retry.Build.Status, "a failed upload can be retried with the same artifact")
	require.NoError(t, f.service.UploadLocal(ctx, testBuildApp, testBuildIdentifier, testBuildID, retry.Upload.Headers[bucket.LocalUploadTokenHeader], bytes.NewReader(content)))
	staged, err := os.ReadFile(f.stagedPath(t, *retry.Build))
	require.NoError(t, err)
	require.Equal(t, content, staged)

	ready, err := f.service.Complete(ctx, testBuildApp, testBuildIdentifier, testBuildID)
	require.NoError(t, err)
	require.Equal(t, types.BuildStatusReady, ready.Status)
	require.Equal(t, "release", ready.Metadata.Mode)
	require.NotNil(t, ready.ReadyAt)
	require.Equal(t, f.now.Add(-time.Minute), ready.Metadata.FinishedAt, "ready keeps the client's compile end time")
	final, err := os.ReadFile(f.finalPath(t, *ready))
	require.NoError(t, err)
	require.Equal(t, content, final)
	_, err = os.Stat(f.stagedPath(t, *ready))
	require.True(t, os.IsNotExist(err), "staging is cleaned after publication")

	again, err := f.service.Complete(ctx, testBuildApp, testBuildIdentifier, testBuildID)
	require.NoError(t, err)
	require.Equal(t, ready.ReadyAt, again.ReadyAt, "complete is idempotent")

	registration, err = f.service.RegisterArtifact(context.Background(), testBuildApp, testBuildIdentifier, testBuildID, f.registerInput(content))
	require.NoError(t, err)
	require.Equal(t, types.BuildStatusReady, registration.Build.Status)
	require.Nil(t, registration.Upload, "a ready build gets no upload grant")

	failed, err := f.service.Fail(ctx, testBuildApp, testBuildIdentifier, testBuildID, FailBuildInput{FinishedAt: f.now})
	require.NoError(t, err)
	require.Equal(t, types.BuildStatusReady, failed.Status, "ready never regresses")

	file, err := f.service.Download(ctx, *ready)
	require.NoError(t, err)
	downloaded, err := io.ReadAll(file.Reader)
	require.NoError(t, err)
	require.NoError(t, file.Reader.Close())
	require.Equal(t, content, downloaded)
	_, err = f.service.Download(ctx, *afterMissing)
	require.ErrorIs(t, err, ErrBuildNotReady)
}

func TestBuildRegisterWithoutStart(t *testing.T) {
	f := newBuildFixture(t)
	ctx := context.Background()
	content := []byte("direct")
	registration, err := f.service.RegisterArtifact(ctx, testBuildApp, testBuildIdentifier, testBuildID, f.registerInput(content))
	require.NoError(t, err)
	require.Equal(t, types.BuildStatusUploading, registration.Build.Status)
	require.Len(t, f.repo.builds, 1)

	changed := f.registerInput([]byte("different"))
	_, err = f.service.RegisterArtifact(ctx, testBuildApp, testBuildIdentifier, testBuildID, changed)
	require.ErrorIs(t, err, ErrBuildConflict, "checksum is immutable once declared")
	changed = f.registerInput(content)
	changed.Metadata.BuildNumber = "43"
	_, err = f.service.RegisterArtifact(ctx, testBuildApp, testBuildIdentifier, testBuildID, changed)
	require.ErrorIs(t, err, ErrBuildConflict, "no second allocation on the same build")
	changed = f.registerInput(content)
	changed.Metadata.Profile = "preview"
	_, err = f.service.RegisterArtifact(ctx, testBuildApp, testBuildIdentifier, testBuildID, changed)
	require.ErrorIs(t, err, ErrBuildConflict)
	changed = f.registerInput(content)
	changed.Metadata.FinishedAt = changed.Metadata.FinishedAt.Add(time.Second)
	_, err = f.service.RegisterArtifact(ctx, testBuildApp, testBuildIdentifier, testBuildID, changed)
	require.ErrorIs(t, err, ErrBuildConflict)

	_, err = f.service.Start(ctx, testBuildApp, testBuildIdentifier, testBuildID, f.startInput())
	require.NoError(t, err, "a late start with the same inputs is a no-op")
	current, err := f.service.Get(ctx, testBuildApp, testBuildID)
	require.NoError(t, err)
	require.Equal(t, types.BuildStatusUploading, current.Status)

	_, err = f.service.Complete(ctx, "other-app", testBuildIdentifier, testBuildID)
	var missing *store.ErrResourceNotFound
	require.ErrorAs(t, err, &missing)
	_, err = f.service.Complete(ctx, testBuildApp, otherBuildID, testBuildID)
	require.ErrorAs(t, err, &missing, "the build belongs to another identifier")
	_, err = f.service.Fail(ctx, testBuildApp, otherBuildID, testBuildID, FailBuildInput{FinishedAt: f.now})
	require.ErrorAs(t, err, &missing)
}

func TestBuildFailFromBuilding(t *testing.T) {
	f := newBuildFixture(t)
	ctx := context.Background()
	_, err := f.service.Start(ctx, testBuildApp, testBuildIdentifier, testBuildID, f.startInput())
	require.NoError(t, err)

	_, err = f.service.Fail(ctx, testBuildApp, testBuildIdentifier, testBuildID, FailBuildInput{})
	require.True(t, validation.IsValidationError(err))
	_, err = f.service.Fail(ctx, testBuildApp, testBuildIdentifier, testBuildID, FailBuildInput{FinishedAt: f.now.Add(time.Hour)})
	require.True(t, validation.IsValidationError(err))
	_, err = f.service.Fail(ctx, testBuildApp, testBuildIdentifier, testBuildID, FailBuildInput{FinishedAt: f.now.Add(-time.Hour)})
	require.True(t, validation.IsValidationError(err), "finishedAt before startedAt")
	current, err := f.service.Get(ctx, testBuildApp, testBuildID)
	require.NoError(t, err)
	require.Equal(t, types.BuildStatusBuilding, current.Status)

	failed, err := f.service.Fail(ctx, testBuildApp, testBuildIdentifier, testBuildID, FailBuildInput{FinishedAt: f.now.Add(-2*time.Minute + 700*time.Nanosecond)})
	require.NoError(t, err)
	require.Equal(t, types.BuildStatusFailed, failed.Status)
	require.Equal(t, f.now.Add(-2*time.Minute), failed.Metadata.FinishedAt)
	require.Equal(t, int64(8*time.Minute/time.Millisecond), failed.Metadata.DurationMs)
	require.Zero(t, failed.Size)
	require.Empty(t, failed.Metadata.Fingerprint)
	require.Nil(t, failed.ReadyAt)

	again, err := f.service.Fail(ctx, testBuildApp, testBuildIdentifier, testBuildID, FailBuildInput{FinishedAt: f.now})
	require.NoError(t, err)
	require.Equal(t, failed.Metadata.FinishedAt, again.Metadata.FinishedAt, "the first failure report wins")

	_, err = f.service.Complete(ctx, testBuildApp, testBuildIdentifier, testBuildID)
	require.ErrorIs(t, err, ErrBuildState)
	still, err := f.service.Get(ctx, testBuildApp, testBuildID)
	require.NoError(t, err)
	require.Equal(t, types.BuildStatusFailed, still.Status)

	_, err = f.service.Start(ctx, testBuildApp, testBuildIdentifier, testBuildID, f.startInput())
	require.NoError(t, err, "re-declaring the failed build with identical inputs is idempotent")
	_, err = f.service.Fail(ctx, testBuildApp, testBuildIdentifier, otherBuildID, FailBuildInput{FinishedAt: f.now})
	var missing *store.ErrResourceNotFound
	require.ErrorAs(t, err, &missing)
}

func TestBuildFailFromUploadingDiscardsStaging(t *testing.T) {
	f := newBuildFixture(t)
	ctx := context.Background()
	content := []byte("partial")
	registration, err := f.service.RegisterArtifact(ctx, testBuildApp, testBuildIdentifier, testBuildID, f.registerInput(content))
	require.NoError(t, err)
	require.NoError(t, f.service.UploadLocal(ctx, testBuildApp, testBuildIdentifier, testBuildID, registration.Upload.Headers[bucket.LocalUploadTokenHeader], bytes.NewReader(content)))
	_, err = os.Stat(f.stagedPath(t, *registration.Build))
	require.NoError(t, err)

	failed, err := f.service.Fail(ctx, testBuildApp, testBuildIdentifier, testBuildID, FailBuildInput{FinishedAt: f.now})
	require.NoError(t, err)
	require.Equal(t, types.BuildStatusFailed, failed.Status)
	require.Equal(t, f.now, failed.Metadata.FinishedAt)
	require.Equal(t, int64(len(content)), failed.Size, "the failed row keeps what was declared")
	_, err = os.Stat(f.stagedPath(t, *registration.Build))
	require.True(t, os.IsNotExist(err))

	require.ErrorIs(t, f.service.UploadLocal(ctx, testBuildApp, testBuildIdentifier, testBuildID, registration.Upload.Headers[bucket.LocalUploadTokenHeader], bytes.NewReader(content)), ErrBuildState, "an old grant cannot upload into a failed build")
	_, err = f.service.Complete(ctx, testBuildApp, testBuildIdentifier, testBuildID)
	require.ErrorIs(t, err, ErrBuildState)
}

func TestBuildCompleteRequiresUploadingState(t *testing.T) {
	f := newBuildFixture(t)
	ctx := context.Background()
	_, err := f.service.Start(ctx, testBuildApp, testBuildIdentifier, testBuildID, f.startInput())
	require.NoError(t, err)
	_, err = f.service.Complete(ctx, testBuildApp, testBuildIdentifier, testBuildID)
	require.ErrorIs(t, err, ErrBuildState)
	current, err := f.service.Get(ctx, testBuildApp, testBuildID)
	require.NoError(t, err)
	require.Equal(t, types.BuildStatusBuilding, current.Status, "a premature complete does not fail the build")
	_, err = f.service.Complete(ctx, testBuildApp, testBuildIdentifier, otherBuildID)
	var missing *store.ErrResourceNotFound
	require.ErrorAs(t, err, &missing)
	_, err = f.service.Complete(ctx, testBuildApp, testBuildIdentifier, "bad")
	require.True(t, validation.IsValidationError(err))
}

func TestBuildCompleteRejectsTamperedUpload(t *testing.T) {
	f := newBuildFixture(t)
	ctx := context.Background()
	registration, err := f.service.RegisterArtifact(ctx, testBuildApp, testBuildIdentifier, testBuildID, f.registerInput([]byte("declared")))
	require.NoError(t, err)
	require.NoError(t, f.storage.PutBuildArtifact(ctx, artifactRef(*registration.Build), true, strings.NewReader("tampered")))
	_, err = f.service.Complete(ctx, testBuildApp, testBuildIdentifier, testBuildID)
	require.ErrorIs(t, err, ErrBuildIntegrity)
	_, err = os.Stat(f.finalPath(t, *registration.Build))
	require.True(t, os.IsNotExist(err), "nothing is published on a checksum mismatch")
	failed, err := f.service.Get(ctx, testBuildApp, testBuildID)
	require.NoError(t, err)
	require.Equal(t, types.BuildStatusFailed, failed.Status)

	registration, err = f.service.RegisterArtifact(ctx, testBuildApp, testBuildIdentifier, testBuildID, f.registerInput([]byte("declared")))
	require.NoError(t, err)
	require.Equal(t, types.BuildStatusUploading, registration.Build.Status)
	require.NoError(t, f.service.UploadLocal(ctx, testBuildApp, testBuildIdentifier, testBuildID, registration.Upload.Headers[bucket.LocalUploadTokenHeader], strings.NewReader("declared")))
	ready, err := f.service.Complete(ctx, testBuildApp, testBuildIdentifier, testBuildID)
	require.NoError(t, err)
	require.Equal(t, types.BuildStatusReady, ready.Status)
}

func TestBuildUploadLocalBounds(t *testing.T) {
	f := newBuildFixture(t)
	ctx := context.Background()
	registration, err := f.service.RegisterArtifact(ctx, testBuildApp, testBuildIdentifier, testBuildID, f.registerInput([]byte("12345")))
	require.NoError(t, err)

	err = f.service.UploadLocal(ctx, testBuildApp, testBuildIdentifier, testBuildID, registration.Upload.Headers[bucket.LocalUploadTokenHeader], strings.NewReader("123456"))
	require.ErrorIs(t, err, ErrBuildIntegrity)
	_, err = os.Stat(f.stagedPath(t, *registration.Build))
	require.True(t, os.IsNotExist(err), "an oversized body is not kept")

	require.ErrorIs(t, f.service.UploadLocal(ctx, testBuildApp, testBuildIdentifier, testBuildID, "not-a-token", strings.NewReader("12345")), ErrUnauthorized)
	require.ErrorIs(t, f.service.UploadLocal(ctx, testBuildApp, testBuildIdentifier, testBuildID, registration.Upload.Headers[bucket.LocalUploadTokenHeader]+"x", strings.NewReader("12345")), ErrUnauthorized)

	otherUpload, err := f.storage.RequestBuildArtifactUploadURL(ctx, testBuildApp, bucket.BuildArtifact{IdentifierID: otherBuildID, BuildID: testBuildID, Type: types.BuildArtifactAPK})
	require.NoError(t, err)
	require.ErrorIs(t, f.service.UploadLocal(ctx, testBuildApp, testBuildIdentifier, testBuildID, otherUpload.Headers[bucket.LocalUploadTokenHeader], strings.NewReader("12345")), ErrUnauthorized)

	expiredToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "build-upload", "exp": time.Now().Add(-time.Minute).Unix(),
		"appId": testBuildApp, "identifierId": testBuildIdentifier, "buildId": testBuildID,
	}).SignedString([]byte("test-secret"))
	require.NoError(t, err)
	body := strings.NewReader("12345")
	require.ErrorIs(t, f.service.UploadLocal(ctx, testBuildApp, testBuildIdentifier, testBuildID, expiredToken, body), ErrUnauthorized)
	require.Equal(t, 5, body.Len(), "expired grants must not consume the body")
}

func TestBuildTokenRejectsOtherSecret(t *testing.T) {
	f := newBuildFixture(t)
	ctx := context.Background()
	registration, err := f.service.RegisterArtifact(ctx, testBuildApp, testBuildIdentifier, testBuildID, f.registerInput([]byte("x")))
	require.NoError(t, err)
	t.Setenv("JWT_SECRET", "rotated")
	require.ErrorIs(t, f.service.UploadLocal(ctx, testBuildApp, testBuildIdentifier, testBuildID, registration.Upload.Headers[bucket.LocalUploadTokenHeader], strings.NewReader("x")), ErrUnauthorized)
}

func TestBuildLocalUploadTokenIsBoundToRequestedTarget(t *testing.T) {
	f := newBuildFixture(t)
	registration, err := f.service.RegisterArtifact(context.Background(), testBuildApp, testBuildIdentifier, testBuildID, f.registerInput([]byte("apk")))
	require.NoError(t, err)
	token := registration.Upload.Headers[bucket.LocalUploadTokenHeader]
	require.NotEmpty(t, token)
	for _, target := range []struct{ app, identifier, build string }{
		{otherBuildID, testBuildIdentifier, testBuildID},
		{testBuildApp, otherBuildID, testBuildID},
		{testBuildApp, testBuildIdentifier, otherBuildID},
	} {
		body := strings.NewReader("apk")
		err := f.service.UploadLocal(context.Background(), target.app, target.identifier, target.build, token, body)
		require.ErrorIs(t, err, ErrUnauthorized)
		require.Equal(t, 3, body.Len(), "rejected grants must not consume the body")
	}
	_, err = os.Stat(f.stagedPath(t, *registration.Build))
	require.True(t, os.IsNotExist(err))
}

func TestBuildConcurrentRegistrationsAgree(t *testing.T) {
	f := newBuildFixture(t)
	ctx := context.Background()
	content := []byte("race")
	const workers = 12
	var wg sync.WaitGroup
	results := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var err error
			if i%2 == 0 {
				_, err = f.service.Start(ctx, testBuildApp, testBuildIdentifier, testBuildID, f.startInput())
			} else {
				_, err = f.service.RegisterArtifact(ctx, testBuildApp, testBuildIdentifier, testBuildID, f.registerInput(content))
			}
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	require.Len(t, f.repo.builds, 1)
	current, err := f.service.Get(ctx, testBuildApp, testBuildID)
	require.NoError(t, err)
	require.Equal(t, types.BuildStatusUploading, current.Status)
}

func TestBuildSharesRequireReadyAPK(t *testing.T) {
	f := newBuildFixture(t)
	ctx := context.Background()
	content := []byte("apk")
	registration, err := f.service.RegisterArtifact(ctx, testBuildApp, testBuildIdentifier, testBuildID, f.registerInput(content))
	require.NoError(t, err)
	_, _, err = f.service.CreateShare(ctx, testBuildApp, testBuildID, 24)
	require.True(t, validation.IsValidationError(err))
	require.NoError(t, f.service.UploadLocal(ctx, testBuildApp, testBuildIdentifier, testBuildID, registration.Upload.Headers[bucket.LocalUploadTokenHeader], bytes.NewReader(content)))
	_, err = f.service.Complete(ctx, testBuildApp, testBuildIdentifier, testBuildID)
	require.NoError(t, err)
	_, _, err = f.service.CreateShare(ctx, testBuildApp, testBuildID, 0)
	require.True(t, validation.IsValidationError(err))
	_, _, err = f.service.CreateShare(ctx, testBuildApp, testBuildID, 721)
	require.True(t, validation.IsValidationError(err))
	share, token, err := f.service.CreateShare(ctx, testBuildApp, testBuildID, 48)
	require.NoError(t, err)
	require.Len(t, token, 64)
	require.Equal(t, f.now.Add(48*time.Hour), share.ExpiresAt)
	require.NotContains(t, f.repo.shares, token, "only the hash is stored")
	require.Contains(t, f.repo.shares, tokenHash(token))

	resolved, expiry, err := f.service.ResolveShare(ctx, token)
	require.NoError(t, err)
	require.Equal(t, testBuildID, resolved.ID)
	require.Equal(t, share.ExpiresAt, expiry)
	var missing *store.ErrResourceNotFound
	_, _, err = f.service.ResolveShare(ctx, "short")
	require.ErrorAs(t, err, &missing)
	_, _, err = f.service.ResolveShare(ctx, strings.Repeat("0", 64))
	require.ErrorAs(t, err, &missing)
	f.repo.err = errors.New("database down")
	_, _, err = f.service.ResolveShare(ctx, token)
	require.False(t, errors.As(err, &missing), "outages are not reported as missing links")
}

func TestBuildSharesRequireAdHocOverHTTPSForIos(t *testing.T) {
	f := newBuildFixture(t)
	ctx := context.Background()
	share := func(distribution types.IosDistribution) error {
		f.repo.builds[testBuildID] = types.BuildRecord{
			ID: testBuildID, AppID: testBuildApp, AppIdentifierID: otherBuildID, Platform: types.PlatformIOS,
			Status: types.BuildStatusReady, ArtifactType: types.BuildArtifactIPA, Metadata: types.BuildMetadata{Distribution: distribution},
		}
		_, _, err := f.service.CreateShare(ctx, testBuildApp, testBuildID, 24)
		return err
	}
	t.Setenv("BASE_URL", "https://ota.example.com")
	require.ErrorContains(t, share(types.IosDistributionAppStore), "only ready APK and iOS Ad Hoc builds can be shared")
	require.ErrorContains(t, share(""), "only ready APK and iOS Ad Hoc builds can be shared")
	require.NoError(t, share(types.IosDistributionAdHoc))
	t.Setenv("BASE_URL", "http://localhost:3000")
	err := share(types.IosDistributionAdHoc)
	require.ErrorContains(t, err, "HTTPS")
	require.True(t, validation.IsValidationError(err))
}

func TestBuildDistributionIsAnIosField(t *testing.T) {
	f := newBuildFixture(t)
	android := f.startInput()
	android.Metadata.Distribution = types.IosDistributionAdHoc
	_, err := f.service.Start(context.Background(), testBuildApp, testBuildIdentifier, testBuildID, android)
	require.ErrorContains(t, err, "only iOS builds have a distribution")

	ios := f.startInput()
	ios.ArtifactType, ios.Metadata.Distribution = types.BuildArtifactIPA, "enterprise"
	_, err = f.service.Start(context.Background(), testBuildApp, otherBuildID, testBuildID, ios)
	require.ErrorContains(t, err, "metadata.distribution")

	ios.Metadata.Distribution = types.IosDistributionAdHoc
	build, err := f.service.Start(context.Background(), testBuildApp, otherBuildID, testBuildID, ios)
	require.NoError(t, err)
	require.Equal(t, types.IosDistributionAdHoc, build.Metadata.Distribution)
}
