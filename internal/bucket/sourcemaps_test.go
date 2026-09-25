package bucket

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

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

// The store may share the updates directory: its prefix is reserved there, and
// the shared directory keeps the updates' permissions.
func TestSourcemapStoreSharesTheUpdatesLocation(t *testing.T) {
	dir := localUploadEnv(t)
	store := sourcemapEnv(t, dir)
	ctx := context.Background()
	content := []byte(`{"version":3}`)

	require.NoError(t, store.Put(ctx, "app-1", blobHash(content), bytes.NewReader(content)))
	written, err := os.ReadFile(filepath.Join(dir, "app-1", sourcemapsDir, blobHash(content)))
	require.NoError(t, err)
	assert.Equal(t, content, written)
	info, err := os.Stat(filepath.Join(dir, "app-1", sourcemapsDir))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm())

	branches, err := GetBucket().UpdateStore.Branches(ctx, "app-1")
	require.NoError(t, err)
	assert.Empty(t, branches, "the sourcemaps directory is not a branch")
	assert.True(t, ReservedBranchName(sourcemapsDir))
}

func TestSourcemapStoreLivesUnderTheKeyPrefix(t *testing.T) {
	dir := localUploadEnv(t)
	t.Setenv("BUCKET_KEY_PREFIX", "tenant-a")
	ResetBucketInstance()
	store := sourcemapEnv(t, dir)
	ctx := context.Background()
	content := []byte(`{"version":3}`)

	require.NoError(t, store.Put(ctx, "app-1", blobHash(content), bytes.NewReader(content)))
	assert.FileExists(t, filepath.Join(dir, "tenant-a", "app-1", sourcemapsDir, blobHash(content)))
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
	assert.NoFileExists(t, filepath.Join(dir, "app-1", sourcemapsDir, blobHash([]byte("what the CLI hashed"))))
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
	_, ok = SourcemapKeyHash("app-1/"+sourcemapsDir+"/not-a-hash", "app-1")
	assert.False(t, ok)
}
