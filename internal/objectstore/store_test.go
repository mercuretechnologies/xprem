package objectstore

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenDedicatedRefusesTheUpdatesLocation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_BUCKET_BASE_PATH", dir)
	t.Setenv("LOCAL_ARCHIVE_DIR", dir)
	_, err := OpenDedicated(map[Mode]string{ModeLocal: "LOCAL_ARCHIVE_DIR"})
	require.ErrorContains(t, err, "dedicated directory")

	t.Setenv("STORAGE_MODE", "s3")
	t.Setenv("S3_BUCKET_NAME", "updates")
	t.Setenv("S3_ARCHIVE_BUCKET", "updates")
	_, err = OpenDedicated(map[Mode]string{ModeS3: "S3_ARCHIVE_BUCKET"})
	require.ErrorContains(t, err, "dedicated bucket")
}

func TestOpenDedicatedRequiresALocation(t *testing.T) {
	t.Setenv("STORAGE_MODE", "s3")
	t.Setenv("S3_ARCHIVE_BUCKET", "")
	_, err := OpenDedicated(map[Mode]string{ModeS3: "S3_ARCHIVE_BUCKET"})
	require.ErrorContains(t, err, "S3_ARCHIVE_BUCKET is not set")
}

func put(t *testing.T, store Store, key, content string) {
	t.Helper()
	require.NoError(t, store.Put(context.Background(), key, strings.NewReader(content)))
}

func read(t *testing.T, store Store, key string) string {
	t.Helper()
	file, err := store.Get(context.Background(), key)
	require.NoError(t, err)
	require.NotNil(t, file, key)
	content, err := io.ReadAll(file.Reader)
	require.NoError(t, file.Reader.Close())
	require.NoError(t, err)
	return string(content)
}

func TestLocalStoreRoundtrip(t *testing.T) {
	ctx := context.Background()
	store := Open(ModeLocal, t.TempDir())

	put(t, store, "2026/07/22/1-5.ndjson", "{\"stream\":\"audit\"}\n")

	exists, err := store.Exists(ctx, "2026/07/22/1-5.ndjson")
	require.NoError(t, err)
	assert.True(t, exists)
	assert.Equal(t, "{\"stream\":\"audit\"}\n", read(t, store, "2026/07/22/1-5.ndjson"))

	missing, err := store.Get(ctx, "2026/07/22/missing.ndjson")
	require.NoError(t, err)
	assert.Nil(t, missing)
}

func TestLocalStoreRefusesEscapingKeys(t *testing.T) {
	dir := t.TempDir()
	store := Open(ModeLocal, dir)

	err := store.Put(context.Background(), "../escape.ndjson", strings.NewReader("x"))
	require.ErrorContains(t, err, "invalid object key")
	_, statErr := os.Stat(filepath.Join(filepath.Dir(dir), "escape.ndjson"))
	require.True(t, os.IsNotExist(statErr))
}

// A filesystem has directories where an object store has only keys: listing
// must hide them, and deleting the last key of a directory must not leave
// the directory behind as a phantom prefix.
func TestLocalStoreListsAndDeletesLikeAnObjectStore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := Open(ModeLocal, dir)
	put(t, store, "app/main/1/100/.check", "")
	put(t, store, "app/main/1/100/assets/a.png", "a")
	put(t, store, "app/main/1/200/.check", "")
	put(t, store, "app/cas/blob", "b")

	keys, err := store.List(ctx, "app/main/1/100/")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"app/main/1/100/.check", "app/main/1/100/assets/a.png"}, keys)

	names, err := store.ListPrefixes(ctx, "app/")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"main", "cas"}, names)
	names, err = store.ListPrefixes(ctx, "app/missing/")
	require.NoError(t, err)
	assert.Empty(t, names)

	require.NoError(t, store.Copy(ctx, "app/cas/blob", "app/main/1/200/blob"))
	assert.Equal(t, "b", read(t, store, "app/main/1/200/blob"))
	require.Error(t, store.Copy(ctx, "app/cas/absent", "app/elsewhere"))

	require.NoError(t, store.Delete(ctx, "app/main/1/100/assets/a.png"))
	assert.NoDirExists(t, filepath.Join(dir, "app", "main", "1", "100", "assets"))
	assert.FileExists(t, filepath.Join(dir, "app", "main", "1", "100", ".check"))

	require.NoError(t, store.DeletePrefix(ctx, "app/main/1/200/"))
	names, err = store.ListPrefixes(ctx, "app/main/1/")
	require.NoError(t, err)
	assert.Equal(t, []string{"100"}, names)
}

func TestWithPrefixKeepsKeysRelative(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := WithPrefix(Open(ModeLocal, dir), "tenant/")
	put(t, store, "app/cas/blob", "b")

	assert.FileExists(t, filepath.Join(dir, "tenant", "app", "cas", "blob"))
	keys, err := store.List(ctx, "app/")
	require.NoError(t, err)
	assert.Equal(t, []string{"app/cas/blob"}, keys)
	names, err := store.ListPrefixes(ctx, "app/")
	require.NoError(t, err)
	assert.Equal(t, []string{"cas"}, names)
}

// CopySource is decoded like a query string, segment by segment: a space
// travels as "+" and a literal "+" as %2B, the slashes stay.
func TestEscapeCopySourceKey(t *testing.T) {
	assert.Equal(t, "a/b/c", escapeCopySourceKey("a/b/c"))
	assert.Equal(t, "foo+bar/a%2Bb", escapeCopySourceKey("foo bar/a+b"))
	assert.Equal(t, "branch/1/12345/update-metadata.json", escapeCopySourceKey("branch/1/12345/update-metadata.json"))
}

// An empty prefix names every object: deleting it must be refused, including
// behind a key prefix where it would name the whole tenant.
func TestDeletePrefixRefusesTheWholeStore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := Open(ModeLocal, dir)
	put(t, store, "tenant/app/cas/blob", "b")

	require.Error(t, store.DeletePrefix(ctx, ""))
	require.Error(t, WithPrefix(store, "tenant/").DeletePrefix(ctx, ""))
	assert.FileExists(t, filepath.Join(dir, "tenant", "app", "cas", "blob"))
}

// The audit archive holds emails and IPs: other local users must not list,
// replace or delete it. Update files stay readable by a web server.
func TestLocalFilePermissions(t *testing.T) {
	archiveDir := filepath.Join(t.TempDir(), "archive")
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_BUCKET_BASE_PATH", t.TempDir())
	t.Setenv("LOCAL_ARCHIVE_DIR", archiveDir)
	archive, err := OpenDedicated(map[Mode]string{ModeLocal: "LOCAL_ARCHIVE_DIR"})
	require.NoError(t, err)
	put(t, archive, "2026/07/22/1-5.ndjson", "{}")

	for _, dir := range []string{archiveDir, filepath.Join(archiveDir, "2026", "07", "22")} {
		info, err := os.Stat(dir)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(), dir)
	}
	info, err := os.Stat(filepath.Join(archiveDir, "2026", "07", "22", "1-5.ndjson"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	updatesDir := t.TempDir()
	put(t, Open(ModeLocal, updatesDir), "app/main/1/100/metadata.json", "{}")
	info, err = os.Stat(filepath.Join(updatesDir, "app", "main", "1", "100", "metadata.json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())
}
