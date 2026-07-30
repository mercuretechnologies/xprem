package bucket

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Only the LocalBucket re-path is exercised here; S3/GCS require a live
// backend and are covered by integration tests outside the Go test suite.
//
// Shared layout for every fixture:
//   {basePath}/branch-a/1/12345/.check
//   {basePath}/branch-b/1/67890/.check
//   {basePath}/.migrationhistory
// After MoveRootEntriesUnder("app-1"):
//   {basePath}/app-1/branch-a/1/12345/.check
//   {basePath}/app-1/branch-b/1/67890/.check
//   {basePath}/.migrationhistory  (stays at root)

func newV1LayoutBucket(t *testing.T) *LocalBucket {
	t.Helper()
	base := t.TempDir()
	writeFile(t, filepath.Join(base, "branch-a", "1", "12345", ".check"))
	writeFile(t, filepath.Join(base, "branch-b", "1", "67890", ".check"))
	writeFile(t, filepath.Join(base, ".migrationhistory"))
	return &LocalBucket{BasePath: base}
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))
}

func TestLocalBucket_MoveRootEntriesUnder_MovesBranches(t *testing.T) {
	b := newV1LayoutBucket(t)
	base := b.BasePath

	require.NoError(t, b.MoveRootEntriesUnder("app-1"))

	// Branch directories are now under app-1/
	assert.FileExists(t, filepath.Join(base, "app-1", "branch-a", "1", "12345", ".check"))
	assert.FileExists(t, filepath.Join(base, "app-1", "branch-b", "1", "67890", ".check"))

	// Roots should not contain branch-a / branch-b anymore.
	assert.NoFileExists(t, filepath.Join(base, "branch-a", "1", "12345", ".check"))
	assert.NoFileExists(t, filepath.Join(base, "branch-b", "1", "67890", ".check"))
}

func TestLocalBucket_MoveRootEntriesUnder_PreservesMigrationHistory(t *testing.T) {
	// The ledger is deployment-global, it MUST stay at root, otherwise
	// the migration runner can't find it on the next boot and every
	// applied migration silently re-runs.
	b := newV1LayoutBucket(t)
	base := b.BasePath

	require.NoError(t, b.MoveRootEntriesUnder("app-1"))

	assert.FileExists(t, filepath.Join(base, ".migrationhistory"))
	assert.NoFileExists(t, filepath.Join(base, "app-1", ".migrationhistory"))
}

func TestLocalBucket_MoveRootEntriesUnder_IdempotentOnReRun(t *testing.T) {
	// Re-running on an already-migrated tree must be a no-op, operators
	// restart pods, Kubernetes retries init containers, the migration
	// runner can be manually invoked. Any of those can trigger a second
	// call; dropping data or erroring here would be devastating.
	b := newV1LayoutBucket(t)
	base := b.BasePath

	require.NoError(t, b.MoveRootEntriesUnder("app-1"))
	// Second run: app-1/ is now the only top-level entry besides the
	// ledger. MoveRootEntriesUnder skips `name == appId` and the ledger,
	// so nothing to move, must not error and must not touch anything.
	require.NoError(t, b.MoveRootEntriesUnder("app-1"))

	assert.FileExists(t, filepath.Join(base, "app-1", "branch-a", "1", "12345", ".check"))
	assert.FileExists(t, filepath.Join(base, "app-1", "branch-b", "1", "67890", ".check"))
	// Critically, there is no app-1/app-1 loop.
	assert.NoDirExists(t, filepath.Join(base, "app-1", "app-1"))
}

func TestLocalBucket_MoveRootEntriesUnder_ResumesPartialMigration(t *testing.T) {
	// Simulate a crash mid-migration: branch-a was already moved, branch-b
	// is still at root. The second run must complete the job without
	// clobbering branch-a.
	base := t.TempDir()
	writeFile(t, filepath.Join(base, "app-1", "branch-a", "1", "12345", ".check"))
	writeFile(t, filepath.Join(base, "branch-b", "1", "67890", ".check"))
	writeFile(t, filepath.Join(base, ".migrationhistory"))
	b := &LocalBucket{BasePath: base}

	require.NoError(t, b.MoveRootEntriesUnder("app-1"))

	assert.FileExists(t, filepath.Join(base, "app-1", "branch-a", "1", "12345", ".check"))
	assert.FileExists(t, filepath.Join(base, "app-1", "branch-b", "1", "67890", ".check"))
	assert.NoFileExists(t, filepath.Join(base, "branch-b", "1", "67890", ".check"))
}

func TestLocalBucket_MoveRootEntriesUnder_EmptyBucketIsNoOp(t *testing.T) {
	// Fresh install: no data at all, but the migration runner still
	// invokes up(). The move must return nil and not create a stray
	// {appId}/ directory to clutter the bucket.
	base := t.TempDir()
	b := &LocalBucket{BasePath: base}

	require.NoError(t, b.MoveRootEntriesUnder("app-1"))
}

func TestLocalBucket_MoveRootEntriesUnder_SkipsNonBranchShapedEntries(t *testing.T) {
	// The big safety net: if another appId-looking directory is already
	// at root (partial earlier migration, wrong env var, leftover from
	// disaster recovery), we must NOT re-parent it under the current
	// target appId. A directory without {rv}/{updateId}/.check depth-3
	// shape isn't a v1 branch, leave it alone.
	base := t.TempDir()
	// A legit v1 branch, should move.
	writeFile(t, filepath.Join(base, "branch-a", "1", "12345", ".check"))
	// A v2-looking appId dir (depth-4 .check), must be left alone.
	writeFile(t, filepath.Join(base, "other-app-uuid", "branch-a", "1", "67890", ".check"))
	// A random directory with no v1 shape at all, must be left alone.
	writeFile(t, filepath.Join(base, "random", "README.md"))
	b := &LocalBucket{BasePath: base}

	require.NoError(t, b.MoveRootEntriesUnder("app-1"))

	// branch-a moved.
	assert.FileExists(t, filepath.Join(base, "app-1", "branch-a", "1", "12345", ".check"))
	assert.NoDirExists(t, filepath.Join(base, "branch-a"))

	// other-app-uuid untouched at root.
	assert.FileExists(t, filepath.Join(base, "other-app-uuid", "branch-a", "1", "67890", ".check"))
	assert.NoDirExists(t, filepath.Join(base, "app-1", "other-app-uuid"))

	// random/ untouched.
	assert.FileExists(t, filepath.Join(base, "random", "README.md"))
	assert.NoDirExists(t, filepath.Join(base, "app-1", "random"))
}

func TestLocalBucket_MoveRootEntriesUnder_RejectsAppIdCollidingWithV1Branch(t *testing.T) {
	// An opera tor upgrades to v2 and picks EXPO_APP_ID="staging", but
	// their v1 bucket already has a branch literally called "staging".
	// Auto-moving everything under staging/ would nest the v1 "staging"
	// branch as staging/staging/..., data loss in place. Require the
	// operator to resolve manually.
	base := t.TempDir()
	writeFile(t, filepath.Join(base, "staging", "1", "12345", ".check")) // v1 branch named "staging"
	writeFile(t, filepath.Join(base, "branch-other", "1", "67890", ".check"))
	writeFile(t, filepath.Join(base, ".migrationhistory"))
	b := &LocalBucket{BasePath: base}

	err := b.MoveRootEntriesUnder("staging")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAppIdCollidesWithV1Branch)

	// Nothing should have moved, the operator has to resolve before any
	// re-parenting happens.
	assert.FileExists(t, filepath.Join(base, "staging", "1", "12345", ".check"))
	assert.FileExists(t, filepath.Join(base, "branch-other", "1", "67890", ".check"))
	assert.NoDirExists(t, filepath.Join(base, "staging", "staging"))
	assert.NoDirExists(t, filepath.Join(base, "staging", "branch-other"))
}

func TestLocalBucket_MoveRootEntriesUnder_AcceptsV2AppIdDirectoryWithSameName(t *testing.T) {
	// The {appId}/ directory pre-exists BUT it is v2-shaped (marker at
	// depth 3 inside it, i.e. {appId}/{branch}/{rv}/{update}/.check). The
	// migration must NOT confuse it for a v1-branch collision, that's
	// just "already migrated" and the run should complete without error.
	base := t.TempDir()
	writeFile(t, filepath.Join(base, "app-1", "branch-a", "1", "12345", ".check")) // v2 shape
	writeFile(t, filepath.Join(base, "branch-b", "1", "67890", ".check"))          // v1 branch
	writeFile(t, filepath.Join(base, ".migrationhistory"))
	b := &LocalBucket{BasePath: base}

	require.NoError(t, b.MoveRootEntriesUnder("app-1"))

	// The existing v2 content stays put, and branch-b gets moved in.
	assert.FileExists(t, filepath.Join(base, "app-1", "branch-a", "1", "12345", ".check"))
	assert.FileExists(t, filepath.Join(base, "app-1", "branch-b", "1", "67890", ".check"))
	assert.NoDirExists(t, filepath.Join(base, "branch-b"))
}

func TestLocalBucket_MoveRootEntriesUnder_NoOpOnFullyV2Bucket(t *testing.T) {
	// If every non-ledger top-level entry is already v2-shaped, the
	// migration must not create a stray empty {appId}/ directory.
	base := t.TempDir()
	writeFile(t, filepath.Join(base, "app-1", "branch-a", "1", "12345", ".check"))
	writeFile(t, filepath.Join(base, ".migrationhistory"))
	b := &LocalBucket{BasePath: base}

	require.NoError(t, b.MoveRootEntriesUnder("app-1"))

	// app-1 still exists with its content, no new nesting.
	assert.FileExists(t, filepath.Join(base, "app-1", "branch-a", "1", "12345", ".check"))
	assert.NoDirExists(t, filepath.Join(base, "app-1", "app-1"))
}

func TestV1BranchTripleFromMarker(t *testing.T) {
	cases := map[string]struct {
		triple string
		ok     bool
	}{
		// Confirmed v1 markers at exactly segment 4.
		"branch-a/1/12345/.check":               {"branch-a/1/12345", true},
		"branch-a/1/12345/update-metadata.json": {"branch-a/1/12345", true},
		"branch-a/1.0.0/12345/.check":           {"branch-a/1.0.0/12345", true},

		// v2 key from another app, marker at segment 5. Must NOT confirm
		// a triple, otherwise its data gets re-parented under our appId.
		"other-app/branch-a/1/12345/.check":               {"", false},
		"other-app/branch-a/1/12345/update-metadata.json": {"", false},

		// v1 nested assets are not markers themselves, their triple is
		// confirmed by a sibling .check / update-metadata.json object.
		"branch-a/1/12345/bundles/x.js": {"", false},
		"branch-a/1/12345/assets/x.png": {"", false},

		// Shape mismatches.
		"branch-a":               {"", false},
		"branch-a/1":             {"", false},
		"branch-a/1/12345":       {"", false},
		"//12345/.check":         {"", false},
		"branch-a//12345/.check": {"", false},
	}
	for k, want := range cases {
		t.Run(k, func(t *testing.T) {
			got, ok := v1BranchTripleFromMarker(k)
			assert.Equal(t, want.ok, ok)
			assert.Equal(t, want.triple, got)
		})
	}
}

func TestInConfirmedTriple(t *testing.T) {
	confirmed := map[string]bool{"branch-a/1/12345": true}

	// v1 own-branch objects (flat and nested), both belong to the triple.
	assert.True(t, inConfirmedTriple("branch-a/1/12345/.check", confirmed))
	assert.True(t, inConfirmedTriple("branch-a/1/12345/update-metadata.json", confirmed))
	assert.True(t, inConfirmedTriple("branch-a/1/12345/bundles/x.js", confirmed))

	// v2 keys from another app, first 3 segments do not match any
	// confirmed triple.
	assert.False(t, inConfirmedTriple("other-app/branch-a/1/12345/.check", confirmed))

	// Unrelated branch.
	assert.False(t, inConfirmedTriple("branch-b/1/67890/.check", confirmed))

	// Too shallow to be a branch object at all.
	assert.False(t, inConfirmedTriple("branch-a", confirmed))
	assert.False(t, inConfirmedTriple("branch-a/1/12345", confirmed))
}

func TestEscapeKeyForCopySource(t *testing.T) {
	// Slashes separating path segments must survive; everything else that
	// needs URL escaping (spaces, +, unicode) must be percent-encoded.
	assert.Equal(t, "a/b/c", escapeKeyForCopySource("a/b/c"))
	assert.Equal(t, "foo%20bar/baz", escapeKeyForCopySource("foo bar/baz"))
	// `+` is path-unreserved, so it survives un-escaped, url.PathEscape
	// does not encode it. What matters is that it did not turn `/` into
	// `%2F`.
	assert.Equal(t, "a+b/c", escapeKeyForCopySource("a+b/c"))
	assert.Equal(t, "branch/1/12345/update-metadata.json", escapeKeyForCopySource("branch/1/12345/update-metadata.json"))
}

func TestLocalBucket_MoveRootEntriesUnder_RespectsKeyPrefix(t *testing.T) {
	// When KeyPrefix is set, the bucket root is {BasePath}/{KeyPrefix}/.
	// The re-path must happen inside that subtree, never at the outer
	// BasePath, otherwise a multi-tenant host that co-locates two
	// xprem instances under the same BasePath would have them
	// stomp each other.
	base := t.TempDir()
	writeFile(t, filepath.Join(base, "myapp", "branch-a", "1", "12345", ".check"))
	writeFile(t, filepath.Join(base, "myapp", ".migrationhistory"))
	writeFile(t, filepath.Join(base, "other-tenant", ".check"))
	b := &LocalBucket{BasePath: base, KeyPrefix: "myapp/"}

	require.NoError(t, b.MoveRootEntriesUnder("app-1"))

	assert.FileExists(t, filepath.Join(base, "myapp", "app-1", "branch-a", "1", "12345", ".check"))
	assert.FileExists(t, filepath.Join(base, "myapp", ".migrationhistory"))
	// Sibling tenant data outside the prefix is untouched.
	assert.FileExists(t, filepath.Join(base, "other-tenant", ".check"))
	assert.NoDirExists(t, filepath.Join(base, "myapp", "app-1", "other-tenant"))
}
