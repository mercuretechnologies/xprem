package bucket

import (
	"context"
	"path/filepath"
	"testing"
	"xprem/internal/objectstore"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every fixture starts as a v1 layout:
//
//	{dir}/branch-a/1/12345/.check
//	{dir}/branch-b/1/67890/.check
//	{dir}/.migrationhistory
func v1Bucket(t *testing.T) (*Bucket, string) {
	t.Helper()
	b, dir := localBucket(t)
	writeFile(t, filepath.Join(dir, "branch-a", "1", "12345", ".check"))
	writeFile(t, filepath.Join(dir, "branch-b", "1", "67890", ".check"))
	writeFile(t, filepath.Join(dir, ".migrationhistory"))
	return b, dir
}

func moveUnder(t *testing.T, b *Bucket, appId string) error {
	t.Helper()
	return b.MoveRootEntriesUnder(context.Background(), appId)
}

func TestMoveRootEntriesUnderMovesBranchesAndKeepsTheLedger(t *testing.T) {
	b, dir := v1Bucket(t)

	require.NoError(t, moveUnder(t, b, "app-1"))

	assert.FileExists(t, filepath.Join(dir, "app-1", "branch-a", "1", "12345", ".check"))
	assert.FileExists(t, filepath.Join(dir, "app-1", "branch-b", "1", "67890", ".check"))
	assert.NoDirExists(t, filepath.Join(dir, "branch-a"))
	assert.NoDirExists(t, filepath.Join(dir, "branch-b"))
	// The ledger is deployment-global: moved along, every migration would re-run.
	assert.FileExists(t, filepath.Join(dir, ".migrationhistory"))
	assert.NoFileExists(t, filepath.Join(dir, "app-1", ".migrationhistory"))
}

// Restarts, retried init containers and manual runs all call the move again:
// a second run, or a run resuming a crash halfway, must converge without a
// {appId}/{appId} loop.
func TestMoveRootEntriesUnderIsIdempotentAndResumable(t *testing.T) {
	b, dir := v1Bucket(t)
	require.NoError(t, moveUnder(t, b, "app-1"))
	require.NoError(t, moveUnder(t, b, "app-1"))
	assert.NoDirExists(t, filepath.Join(dir, "app-1", "app-1"))

	resumed, resumedDir := localBucket(t)
	writeFile(t, filepath.Join(resumedDir, "app-1", "branch-a", "1", "12345", ".check"))
	writeFile(t, filepath.Join(resumedDir, "branch-b", "1", "67890", ".check"))
	require.NoError(t, moveUnder(t, resumed, "app-1"))
	assert.FileExists(t, filepath.Join(resumedDir, "app-1", "branch-a", "1", "12345", ".check"))
	assert.FileExists(t, filepath.Join(resumedDir, "app-1", "branch-b", "1", "67890", ".check"))
	assert.NoDirExists(t, filepath.Join(resumedDir, "branch-b"))

	empty, emptyDir := localBucket(t)
	require.NoError(t, moveUnder(t, empty, "app-1"))
	assert.NoDirExists(t, filepath.Join(emptyDir, "app-1"))
}

// Only entries shaped like a v1 branch move: another app's v2 data and
// unrelated files at the root stay where they are.
func TestMoveRootEntriesUnderSkipsNonBranchShapedEntries(t *testing.T) {
	b, dir := localBucket(t)
	writeFile(t, filepath.Join(dir, "branch-a", "1", "12345", ".check"))
	writeFile(t, filepath.Join(dir, "branch-a", "1", "99999", "bundle.js"))
	writeFile(t, filepath.Join(dir, "branch-a", "1", "12345", "assets", "x.png"))
	writeFile(t, filepath.Join(dir, "other-app-uuid", "branch-a", "1", "67890", ".check"))
	writeFile(t, filepath.Join(dir, "random", "README.md"))

	require.NoError(t, moveUnder(t, b, "app-1"))

	assert.FileExists(t, filepath.Join(dir, "app-1", "branch-a", "1", "12345", ".check"))
	assert.FileExists(t, filepath.Join(dir, "app-1", "branch-a", "1", "12345", "assets", "x.png"))
	assert.FileExists(t, filepath.Join(dir, "app-1", "branch-a", "1", "99999", "bundle.js"), "an aborted upload moves with its branch")
	assert.NoDirExists(t, filepath.Join(dir, "branch-a"))
	assert.FileExists(t, filepath.Join(dir, "other-app-uuid", "branch-a", "1", "67890", ".check"))
	assert.NoDirExists(t, filepath.Join(dir, "app-1", "other-app-uuid"))
	assert.FileExists(t, filepath.Join(dir, "random", "README.md"))
	assert.NoDirExists(t, filepath.Join(dir, "app-1", "random"))
}

// A v1 branch named like the app would end up nested under itself: the
// operator resolves that by hand. A v2-shaped {appId}/ directory is just
// data already migrated.
func TestMoveRootEntriesUnderRefusesAnAppIdCollidingWithAV1Branch(t *testing.T) {
	b, dir := localBucket(t)
	writeFile(t, filepath.Join(dir, "staging", "1", "12345", ".check"))
	writeFile(t, filepath.Join(dir, "branch-other", "1", "67890", ".check"))

	err := moveUnder(t, b, "staging")
	assert.ErrorIs(t, err, ErrAppIdCollidesWithV1Branch)
	assert.FileExists(t, filepath.Join(dir, "branch-other", "1", "67890", ".check"))
	assert.NoDirExists(t, filepath.Join(dir, "staging", "staging"))

	migrated, migratedDir := localBucket(t)
	writeFile(t, filepath.Join(migratedDir, "app-1", "branch-a", "1", "12345", ".check"))
	writeFile(t, filepath.Join(migratedDir, "branch-b", "1", "67890", ".check"))
	require.NoError(t, moveUnder(t, migrated, "app-1"))
	assert.FileExists(t, filepath.Join(migratedDir, "app-1", "branch-a", "1", "12345", ".check"))
	assert.FileExists(t, filepath.Join(migratedDir, "app-1", "branch-b", "1", "67890", ".check"))
}

// With a key prefix the re-path happens inside that subtree: two instances
// sharing one base path must not stomp each other.
func TestMoveRootEntriesUnderRespectsKeyPrefix(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "myapp", "branch-a", "1", "12345", ".check"))
	writeFile(t, filepath.Join(dir, "myapp", ".migrationhistory"))
	writeFile(t, filepath.Join(dir, "other-tenant", ".check"))
	b := Open(objectstore.ModeLocal, dir, "myapp/")

	require.NoError(t, moveUnder(t, b, "app-1"))

	assert.FileExists(t, filepath.Join(dir, "myapp", "app-1", "branch-a", "1", "12345", ".check"))
	assert.FileExists(t, filepath.Join(dir, "myapp", ".migrationhistory"))
	assert.FileExists(t, filepath.Join(dir, "other-tenant", ".check"))
	assert.NoDirExists(t, filepath.Join(dir, "myapp", "app-1", "other-tenant"))
}

// The S3 and GCS path moves keys one by one instead of renaming directories;
// it runs here on a local object store.
func TestMoveRootKeysUnderMovesConfirmedUpdates(t *testing.T) {
	b, dir := v1Bucket(t)
	writeFile(t, filepath.Join(dir, "other-app-uuid", "branch-a", "1", "67890", ".check"))

	require.NoError(t, moveRootKeysUnder(context.Background(), b.ObjectStore, "app-1"))

	assert.FileExists(t, filepath.Join(dir, "app-1", "branch-a", "1", "12345", ".check"))
	assert.FileExists(t, filepath.Join(dir, "app-1", "branch-b", "1", "67890", ".check"))
	assert.FileExists(t, filepath.Join(dir, ".migrationhistory"))
	assert.FileExists(t, filepath.Join(dir, "other-app-uuid", "branch-a", "1", "67890", ".check"))
	require.NoError(t, moveRootKeysUnder(context.Background(), b.ObjectStore, "app-1"))
	assert.NoDirExists(t, filepath.Join(dir, "app-1", "app-1"))

	colliding, collidingDir := localBucket(t)
	writeFile(t, filepath.Join(collidingDir, "staging", "1", "12345", ".check"))
	assert.ErrorIs(t, moveRootKeysUnder(context.Background(), colliding.ObjectStore, "staging"), ErrAppIdCollidesWithV1Branch)
}

func TestV1BranchTripleFromMarker(t *testing.T) {
	cases := map[string]struct {
		triple string
		ok     bool
	}{
		"branch-a/1/12345/.check":                         {"branch-a/1/12345", true},
		"branch-a/1/12345/update-metadata.json":           {"branch-a/1/12345", true},
		"other-app/branch-a/1/12345/.check":               {"", false},
		"other-app/branch-a/1/12345/update-metadata.json": {"", false},
		"branch-a/1/12345/bundles/x.js":                   {"", false},
		"branch-a/1/12345":                                {"", false},
		"//12345/.check":                                  {"", false},
	}
	for key, want := range cases {
		got, ok := v1BranchTripleFromMarker(key)
		assert.Equal(t, want.ok, ok, key)
		assert.Equal(t, want.triple, got, key)
	}

	confirmed := map[string]bool{"branch-a/1/12345": true}
	assert.True(t, inConfirmedTriple("branch-a/1/12345/bundles/x.js", confirmed))
	assert.False(t, inConfirmedTriple("other-app/branch-a/1/12345/.check", confirmed))
	assert.False(t, inConfirmedTriple("branch-b/1/67890/.check", confirmed))
	assert.False(t, inConfirmedTriple("branch-a/1/12345", confirmed))
}
