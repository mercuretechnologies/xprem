package bucket

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"xprem/internal/objectstore"
	"xprem/internal/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testTargetUUID = "6f2b1c4e-1b3a-4b4e-9c1d-0a1b2c3d4e5f"
	testSourceUUID = "0b9a8c7d-6e5f-4a3b-8c2d-1e0f9a8b7c6d"
)

func localBucket(t *testing.T) (*Bucket, string) {
	t.Helper()
	dir := t.TempDir()
	return Open(objectstore.ModeLocal, dir, ""), dir
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))
}

func validUpdate() types.Update {
	return types.Update{AppId: "app-1", Branch: "main", RuntimeVersion: "1.0", UpdateId: "123"}
}

func TestBlobsRoundTrip(t *testing.T) {
	b, _ := localBucket(t)
	ctx := context.Background()

	exists, err := b.BlobStore.Exists(ctx, "app-1", testBlobHash)
	require.NoError(t, err)
	assert.False(t, exists)
	missing, err := b.BlobStore.Get(ctx, "app-1", testBlobHash)
	require.NoError(t, err)
	assert.Nil(t, missing)

	require.NoError(t, b.BlobStore.Put(ctx, "app-1", testBlobHash, bytes.NewReader([]byte("hello"))))
	exists, err = b.BlobStore.Exists(ctx, "app-1", testBlobHash)
	require.NoError(t, err)
	assert.True(t, exists)
	got, err := b.BlobStore.Get(ctx, "app-1", testBlobHash)
	require.NoError(t, err)
	body, err := io.ReadAll(got.Reader)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(body))

	_, err = b.BlobStore.Exists(ctx, "app-1", "not-a-hash")
	assert.Error(t, err)
	_, err = b.BlobStore.PresignPut(ctx, "app-1", testBlobHash, casDir)
	assert.ErrorContains(t, err, "reserved")
}

func TestPatchesRoundTrip(t *testing.T) {
	b, dir := localBucket(t)
	ctx := context.Background()

	require.NoError(t, b.PatchStore.Put(ctx, "app-1", "main", testTargetUUID, testSourceUUID, bytes.NewReader([]byte("BSDIFF40 patch"))))
	assert.FileExists(t, filepath.Join(dir, "app-1", "bsdiff", "main", testTargetUUID, testSourceUUID))
	got, err := b.PatchStore.Get(ctx, "app-1", "main", testTargetUUID, testSourceUUID)
	require.NoError(t, err)
	body, err := io.ReadAll(got.Reader)
	require.NoError(t, err)
	assert.Equal(t, "BSDIFF40 patch", string(body))

	// Same ids on another branch: a separate object, spared when main's patches go.
	require.NoError(t, b.PatchStore.Put(ctx, "app-1", "staging", testTargetUUID, testSourceUUID, bytes.NewReader([]byte("x"))))
	require.NoError(t, b.PatchStore.DeleteBranch(ctx, "app-1", "main"))
	exists, err := b.PatchStore.Exists(ctx, "app-1", "main", testTargetUUID, testSourceUUID)
	require.NoError(t, err)
	assert.False(t, exists)
	exists, err = b.PatchStore.Exists(ctx, "app-1", "staging", testTargetUUID, testSourceUUID)
	require.NoError(t, err)
	assert.True(t, exists)
	require.NoError(t, b.PatchStore.DeleteBranch(ctx, "app-1", "never-existed"))

	_, err = b.PatchStore.Exists(ctx, "app-1", "main", "../etc", testSourceUUID)
	assert.Error(t, err)
	// Uppercase spells the same UUID: it would be a second key for one update.
	_, err = b.PatchStore.Get(ctx, "app-1", "main", strings.ToUpper(testTargetUUID), testSourceUUID)
	assert.Error(t, err)
	assert.Error(t, b.PatchStore.Put(ctx, "app-1", casDir, testTargetUUID, testSourceUUID, bytes.NewReader(nil)))
	assert.Error(t, b.PatchStore.DeleteBranch(ctx, "app-1", "../main"))
}

func TestInstanceIDRoundTrip(t *testing.T) {
	b, _ := localBucket(t)
	id, err := b.InstanceStore.ID(context.Background())
	require.NoError(t, err)
	assert.Empty(t, id)
	require.NoError(t, b.InstanceStore.Persist(context.Background(), "instance-1"))
	id, err = b.InstanceStore.ID(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "instance-1", id)
}

// The reserved directories share the level of the branches, and the data of
// one app must never surface under another.
func TestUpdatesListingsAreScopedToTheApp(t *testing.T) {
	b, dir := localBucket(t)
	writeFile(t, filepath.Join(dir, "app-1", "branch-a", "1.0", "100", ".check"))
	writeFile(t, filepath.Join(dir, "app-1", "branch-a", "1.0", "150", "metadata.json"))
	writeFile(t, filepath.Join(dir, "app-1", "cas", "some-blob-hash"))
	writeFile(t, filepath.Join(dir, "app-1", "bsdiff", "branch-a", testTargetUUID, testSourceUUID))
	writeFile(t, filepath.Join(dir, "app-2", "branch-b", "2.0", "200", ".check"))

	branches, err := b.UpdateStore.Branches(context.Background(), "app-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"branch-a"}, branches)

	// An update without its .check marker is still uploading and does not count.
	stats, err := b.UpdateStore.RuntimeVersions(context.Background(), "app-1", "branch-a")
	require.NoError(t, err)
	require.Len(t, stats, 1)
	assert.Equal(t, "1.0", stats[0].RuntimeVersion)
	assert.Equal(t, 1, stats[0].NumberOfUpdates)

	updates, err := b.UpdateStore.List(context.Background(), "app-1", "branch-a", "1.0")
	require.NoError(t, err)
	assert.Len(t, updates, 2)
	assert.Equal(t, "app-1", updates[0].AppId)

	other, err := b.UpdateStore.List(context.Background(), "app-1", "branch-b", "2.0")
	require.NoError(t, err)
	assert.Empty(t, other, "asking app-1 for app-2's data must not leak app-2's update")
	_, err = b.UpdateStore.RuntimeVersions(context.Background(), "app-1", casDir)
	assert.ErrorContains(t, err, "reserved")
}

func TestUpdatesRejectUnsafeIdentifiers(t *testing.T) {
	b, dir := localBucket(t)
	evilBranch := validUpdate()
	evilBranch.Branch = "../evil"
	evilId := validUpdate()
	evilId.UpdateId = "123/../456"
	reserved := validUpdate()
	reserved.Branch = casDir

	_, err := b.UpdateStore.GetFile(context.Background(), evilBranch, "asset.png")
	assert.Error(t, err)
	_, err = b.UpdateStore.GetFile(context.Background(), validUpdate(), "../../../etc/passwd")
	assert.Error(t, err)
	assert.Error(t, b.UpdateStore.PutFile(context.Background(), validUpdate(), "../evil.js", bytes.NewReader(nil)))
	assert.Error(t, b.UpdateStore.PutFile(context.Background(), reserved, "metadata.json", bytes.NewReader(nil)), "an update folder under cas/ must never reach the store")
	assert.Error(t, b.UpdateStore.Delete(context.Background(), "app-1", "main", "1.0", "123/../456"))
	_, err = b.UpdateStore.PresignPut(context.Background(), "app-1", "main", "1.0", "123", "../etc/passwd")
	assert.Error(t, err)
	_, err = b.UpdateStore.CreateFrom(context.Background(), &evilBranch, "456")
	assert.Error(t, err)
	assert.NoDirExists(t, filepath.Join(filepath.Dir(dir), "evil"))
}

func TestUpdatesFilesRoundTrip(t *testing.T) {
	b, dir := localBucket(t)
	source := validUpdate()

	require.NoError(t, b.UpdateStore.PutFile(context.Background(), source, "assets/img.png", bytes.NewReader([]byte("png-bytes"))))
	require.NoError(t, b.UpdateStore.PutFile(context.Background(), source, ".check", strings.NewReader("")))
	require.NoError(t, b.UpdateStore.PutFile(context.Background(), source, "update-metadata.json", strings.NewReader("{}")))

	file, err := b.UpdateStore.GetFile(context.Background(), source, "assets/img.png")
	require.NoError(t, err)
	body, err := io.ReadAll(file.Reader)
	require.NoError(t, err)
	assert.Equal(t, "png-bytes", string(body))
	missing, err := b.UpdateStore.GetFile(context.Background(), source, "absent.png")
	require.NoError(t, err)
	assert.Nil(t, missing)

	// The markers are not copied: the republished update earns its own.
	created, err := b.UpdateStore.CreateFrom(context.Background(), &source, "300")
	require.NoError(t, err)
	assert.Equal(t, "300", created.UpdateId)
	assert.FileExists(t, filepath.Join(dir, "app-1", "main", "1.0", "300", "assets", "img.png"))
	assert.NoFileExists(t, filepath.Join(dir, "app-1", "main", "1.0", "300", ".check"))
	assert.NoFileExists(t, filepath.Join(dir, "app-1", "main", "1.0", "300", "update-metadata.json"))

	require.NoError(t, b.UpdateStore.Delete(context.Background(), "app-1", "main", "1.0", "300"))
	assert.NoDirExists(t, filepath.Join(dir, "app-1", "main", "1.0", "300"))
	assert.FileExists(t, filepath.Join(dir, "app-1", "main", "1.0", "123", "assets", "img.png"))
}

// Instances sharing one bucket are kept apart by BUCKET_KEY_PREFIX: the bucket
// built from the environment must write under it, not at the root.
func TestGetBucketWritesUnderTheKeyPrefix(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_BUCKET_BASE_PATH", dir)
	t.Setenv("BUCKET_KEY_PREFIX", "tenant-a")
	t.Setenv("S3_KEY_PREFIX", "")
	ResetBucketInstance()
	t.Cleanup(ResetBucketInstance)

	require.NoError(t, GetBucket().UpdateStore.PutFile(context.Background(), validUpdate(), "metadata.json", strings.NewReader("{}")))

	assert.FileExists(t, filepath.Join(dir, "tenant-a", "app-1", "main", "1.0", "123", "metadata.json"))
	assert.NoDirExists(t, filepath.Join(dir, "app-1"))
}

// On disk, ./tenant/ and tenant// name the same directory as tenant/, and a
// republish lists the files back through that directory.
func TestCreateFromUnderNonCanonicalLocalPrefixes(t *testing.T) {
	for _, keyPrefix := range []string{"./tenant/", "tenant//"} {
		t.Run(keyPrefix, func(t *testing.T) {
			dir := t.TempDir()
			b := Open(objectstore.ModeLocal, dir, keyPrefix)
			source := validUpdate()
			require.NoError(t, b.UpdateStore.PutFile(context.Background(), source, "assets/img.png", strings.NewReader("png-bytes")))

			_, err := b.UpdateStore.CreateFrom(context.Background(), &source, "300")

			require.NoError(t, err)
			assert.FileExists(t, filepath.Join(dir, "tenant", "app-1", "main", "1.0", "300", "assets", "img.png"))
		})
	}
}
