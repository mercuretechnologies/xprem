package bucket

import (
	"os"
	"path/filepath"
	"testing"
	"xprem/internal/helpers"
	"xprem/internal/types"

	"github.com/stretchr/testify/assert"
)

func TestGetFile_ValidAssetPath(t *testing.T) {
	teardown := setup(t)
	defer teardown()

	projectRoot, err := os.Getwd()
	assert.Nil(t, err)
	basePath := filepath.Join(projectRoot, "..", "..", "test", "test-updates")

	b := &LocalBucket{BasePath: basePath}
	update := types.Update{
		AppId:          "test-app-id",
		Branch:         "branch-1",
		RuntimeVersion: "1",
		UpdateId:       "1674170951",
		CreatedAt:      helpers.NormalizeTimestampToDuration(1674170951),
	}

	file, err := b.GetFile(update, "metadata.json")
	assert.Nil(t, err)
	assert.NotNil(t, file)
	file.Reader.Close()
}

func TestGetFile_PathTraversalBlocked(t *testing.T) {
	teardown := setup(t)
	defer teardown()

	b := &LocalBucket{BasePath: "/tmp/test-bucket"}
	update := types.Update{
		Branch:         "branch-1",
		RuntimeVersion: "1",
		UpdateId:       "123",
	}

	file, err := b.GetFile(update, "../../../etc/passwd")
	assert.NotNil(t, err)
	assert.Contains(t, err.Error(), "invalid asset path")
	assert.Nil(t, file)
}

func TestGetFile_PathTraversalMultipleLevels(t *testing.T) {
	teardown := setup(t)
	defer teardown()

	b := &LocalBucket{BasePath: "/tmp/test-bucket"}
	update := types.Update{
		Branch:         "branch-1",
		RuntimeVersion: "1",
		UpdateId:       "123",
	}

	file, err := b.GetFile(update, "../../../../etc/shadow")
	assert.NotNil(t, err)
	assert.Contains(t, err.Error(), "invalid asset path")
	assert.Nil(t, file)
}

func TestGetFile_SiblingDirWithSharedPrefixBlocked(t *testing.T) {
	teardown := setup(t)
	defer teardown()

	b := &LocalBucket{BasePath: "/tmp/test-bucket"}
	update := types.Update{
		Branch:         "branch-1",
		RuntimeVersion: "1",
		UpdateId:       "123",
	}

	// updateId="123", assetPath escapes into sibling "1234abc" which shares the "123" string prefix.
	file, err := b.GetFile(update, "../1234abc/secret")
	assert.NotNil(t, err)
	assert.Contains(t, err.Error(), "invalid asset path")
	assert.Nil(t, file)
}

// TestLocalBucket_CrossAppIsolation proves the core multi-tenant promise:
// data written under app-1 is invisible to app-2 via every bucket read
// path. A regression that forgot to thread appId through a listing would
// surface as one tenant's branches / runtime versions / updates bleeding
// into another tenant's dashboard.
func TestLocalBucket_CrossAppIsolation(t *testing.T) {
	teardown := setup(t)
	defer teardown()

	base := t.TempDir()
	// app-1: branch-a with runtime version 1.0 and one update.
	writeFile(t, filepath.Join(base, "app-1", "branch-a", "1.0", "100", ".check"))
	writeFile(t, filepath.Join(base, "app-1", "branch-a", "1.0", "100", "update-metadata.json"))
	// app-2: a completely different branch / runtime / update.
	writeFile(t, filepath.Join(base, "app-2", "branch-b", "2.0", "200", ".check"))
	writeFile(t, filepath.Join(base, "app-2", "branch-b", "2.0", "200", "update-metadata.json"))

	b := &LocalBucket{BasePath: base}

	b1, err := b.GetBranches("app-1")
	assert.Nil(t, err)
	assert.Equal(t, []string{"branch-a"}, b1)
	assert.NotContains(t, b1, "branch-b")

	b2, err := b.GetBranches("app-2")
	assert.Nil(t, err)
	assert.Equal(t, []string{"branch-b"}, b2)
	assert.NotContains(t, b2, "branch-a")

	// Each app's runtime versions only reflect its own layout.
	rv1, err := b.GetRuntimeVersions("app-1", "branch-a")
	assert.Nil(t, err)
	assert.Len(t, rv1, 1)
	assert.Equal(t, "1.0", rv1[0].RuntimeVersion)

	rv2, err := b.GetRuntimeVersions("app-2", "branch-b")
	assert.Nil(t, err)
	assert.Len(t, rv2, 1)
	assert.Equal(t, "2.0", rv2[0].RuntimeVersion)

	// Cross queries fall back to the filesystem "dir not found" error
	// instead of silently leaking the other tenant's data, that's the
	// safe default. We assert on emptiness of the result set, not on the
	// nil-vs-error distinction.
	u1, _ := b.GetUpdates("app-1", "branch-b", "2.0")
	assert.Empty(t, u1, "asking app-1 for app-2's data must not leak app-2's update")

	// Positive path on each app returns exactly its own update.
	u2, err := b.GetUpdates("app-2", "branch-b", "2.0")
	assert.Nil(t, err)
	assert.Len(t, u2, 1)
	assert.Equal(t, "app-2", u2[0].AppId)
	assert.Equal(t, "200", u2[0].UpdateId)

	u1self, err := b.GetUpdates("app-1", "branch-a", "1.0")
	assert.Nil(t, err)
	assert.Len(t, u1self, 1)
	assert.Equal(t, "app-1", u1self[0].AppId)
	assert.Equal(t, "100", u1self[0].UpdateId)
}

func TestGetFile_NormalSubdirectoryAllowed(t *testing.T) {
	teardown := setup(t)
	defer teardown()

	b := &LocalBucket{BasePath: "/tmp/test-bucket"}
	update := types.Update{
		Branch:         "branch-1",
		RuntimeVersion: "1",
		UpdateId:       "123",
	}

	// This path is valid (stays within the update dir), just won't exist on disk
	file, err := b.GetFile(update, "assets/image.png")
	assert.Nil(t, err)
	assert.Nil(t, file) // file doesn't exist, but no path traversal error
}
