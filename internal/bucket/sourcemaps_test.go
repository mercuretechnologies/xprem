package bucket

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"xprem/internal/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sourcemapEnv(t *testing.T, dir string) *SourcemapStore {
	t.Helper()
	t.Setenv("UPLOAD_SOURCEMAPS", "true")
	t.Setenv("LOCAL_SOURCEMAPS_BASE_PATH", dir)
	store, err := OpenSourcemapStore()
	require.NoError(t, err)
	require.NotNil(t, store)
	return store
}

func TestOpenSourcemapStoreIsNilWhenOff(t *testing.T) {
	localUploadEnv(t)
	t.Setenv("UPLOAD_SOURCEMAPS", "false")
	store, err := OpenSourcemapStore()
	require.NoError(t, err)
	assert.Nil(t, store)
}

// The store may share the updates directory: maps live outside app branches,
// and the shared directory keeps the updates' permissions.
func TestSourcemapStoreSharesTheUpdatesLocation(t *testing.T) {
	dir := localUploadEnv(t)
	store := sourcemapEnv(t, dir)
	ctx := context.Background()
	content := []byte(`{"version":3}`)

	require.NoError(t, store.Put(ctx, "app-1", blobHash(content), bytes.NewReader(content)))
	written, err := os.ReadFile(filepath.Join(dir, sourcemapsDir, "app-1", blobHash(content)+".map"))
	require.NoError(t, err)
	assert.Equal(t, content, written)
	info, err := os.Stat(filepath.Join(dir, sourcemapsDir, "app-1"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm())

	branches, err := GetBucket().UpdateStore.Branches(ctx, "app-1")
	require.NoError(t, err)
	assert.Empty(t, branches, "the sourcemaps directory is not a branch")
	assert.False(t, ReservedBranchName(sourcemapsDir))
}

func TestSourcemapStoreLivesUnderTheKeyPrefix(t *testing.T) {
	dir := localUploadEnv(t)
	t.Setenv("BUCKET_KEY_PREFIX", "tenant-a")
	ResetBucketInstance()
	store := sourcemapEnv(t, dir)
	ctx := context.Background()
	content := []byte(`{"version":3}`)

	require.NoError(t, store.Put(ctx, "app-1", blobHash(content), bytes.NewReader(content)))
	assert.FileExists(t, filepath.Join(dir, "tenant-a", sourcemapsDir, "app-1", blobHash(content)+".map"))
	exists, err := store.Exists(ctx, "app-1", blobHash(content))
	require.NoError(t, err)
	assert.True(t, exists)

	branches, err := GetBucket().UpdateStore.Branches(ctx, "app-1")
	require.NoError(t, err)
	assert.Empty(t, branches, "the updates bucket shares the prefix and sees no branch")
}

func TestSourcemapStorePutVerifiesTheHash(t *testing.T) {
	localUploadEnv(t)
	dir := t.TempDir()
	store := sourcemapEnv(t, dir)
	ctx := context.Background()

	err := store.Put(ctx, "app-1", blobHash([]byte("what the CLI hashed")), bytes.NewReader([]byte("what it sent")))
	require.ErrorIs(t, err, ErrBlobHashMismatch)
	assert.NoFileExists(t, filepath.Join(dir, sourcemapsDir, "app-1", blobHash([]byte("what the CLI hashed"))+".map"))
}

func TestSourcemapUploadTokenGrantsItsKey(t *testing.T) {
	localUploadEnv(t)
	store := sourcemapEnv(t, t.TempDir())

	upload, err := store.PresignPut(context.Background(), "app-1", testBlobHash, "production")
	require.NoError(t, err)
	key, appId, branch, err := ValidateUploadToken(upload.Headers[LocalUploadTokenHeader])
	require.NoError(t, err)
	assert.Equal(t, SourcemapObjectKey("app-1", testBlobHash), key)
	assert.Equal(t, "app-1", appId)
	assert.Equal(t, "production", branch)
	hash, ok := SourcemapKeyHash(key, "app-1")
	assert.True(t, ok)
	assert.Equal(t, testBlobHash, hash)

	_, ok = SourcemapKeyHash(key, "app-2")
	assert.False(t, ok, "a sourcemap key belongs to one app")
	_, ok = SourcemapKeyHash(sourcemapsDir+"/app-1/not-a-hash.map", "app-1")
	assert.False(t, ok)
}

func TestSourcemapsDoNotHideOrOverwriteAnExistingBranch(t *testing.T) {
	dir := localUploadEnv(t)
	ctx := context.Background()
	update := types.Update{AppId: "app-1", Branch: "sourcemaps", RuntimeVersion: "1", UpdateId: "123"}
	// These files represent a branch already published by a previous release.
	branchFile := filepath.Join(dir, "app-1", "sourcemaps", "1", "123", "metadata.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(branchFile), 0o755))
	require.NoError(t, os.WriteFile(branchFile, []byte("existing update"), 0o644))

	for _, enabled := range []string{"false", "true"} {
		t.Run(enabled, func(t *testing.T) {
			t.Setenv("UPLOAD_SOURCEMAPS", enabled)
			if enabled == "true" {
				store := sourcemapEnv(t, dir)
				content := []byte(`{"version":3}`)
				require.NoError(t, store.Put(ctx, update.AppId, blobHash(content), bytes.NewReader(content)))
				require.NoError(t, store.PutIndex(ctx, update.AppId, blobHash(content), strings.NewReader("map index")))
			}
			b := GetBucket()
			branches, err := b.UpdateStore.Branches(ctx, update.AppId)
			require.NoError(t, err)
			assert.Equal(t, []string{"sourcemaps"}, branches)
			file, err := b.UpdateStore.GetFile(ctx, update, "metadata.json")
			require.NoError(t, err)
			require.NotNil(t, file)
			body, err := io.ReadAll(file.Reader)
			require.NoError(t, err)
			require.NoError(t, file.Reader.Close())
			assert.Equal(t, "existing update", string(body))
			require.NoError(t, b.UpdateStore.PutFile(ctx, update, "expoConfig.json", strings.NewReader(`{}`)))
		})
	}
}

func TestSourcemapKeyHashRequiresTheExactMapNamespace(t *testing.T) {
	for _, key := range []string{
		"app-1/sourcemaps/" + testBlobHash,
		"sourcemaps/app-1/" + testBlobHash,
		"sourcemaps/app-1/" + testBlobHash + ".map.idx",
		"sourcemaps/app-1/" + testBlobHash + ".map/other",
		"sourcemaps/app-1/other/" + testBlobHash + ".map",
	} {
		_, ok := SourcemapKeyHash(key, "app-1")
		assert.False(t, ok, key)
	}
	_, ok := SourcemapKeyHash("sourcemaps/../app-1/"+testBlobHash+".map", "../app-1")
	assert.False(t, ok)
}
