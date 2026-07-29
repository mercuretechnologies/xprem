package _0260422_v2_scope_data_under_appid

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"xprem/internal/bucket"
	"xprem/internal/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// unreachableBucket fails the test if any Bucket method gets called.
// Wrapping it in a validatingBucket (via bucket.GetBucket) would also
// fail because UnwrapBucket expects a concrete *LocalBucket/*S3Bucket/
// *GCSBucket, but the migration's up() bails out before unwrapping when
// EXPO_APP_ID is unset — which is exactly what we want to prove.
type unreachableBucket struct{ t *testing.T }

func (u unreachableBucket) GetBranches(string) ([]string, error) {
	u.t.Fatal("migration should have skipped; GetBranches should not be called")
	return nil, nil
}
func (u unreachableBucket) GetRuntimeVersions(string, string) ([]types.RuntimeVersionWithStats, error) {
	u.t.Fatal("migration should have skipped")
	return nil, nil
}
func (u unreachableBucket) GetUpdates(string, string, string) ([]types.Update, error) {
	u.t.Fatal("migration should have skipped")
	return nil, nil
}
func (u unreachableBucket) GetFile(types.Update, string) (*types.BucketFile, error) {
	u.t.Fatal("migration should have skipped")
	return nil, nil
}
func (u unreachableBucket) RequestUploadUrlForFileUpdate(string, string, string, string, string) (string, error) {
	u.t.Fatal("migration should have skipped")
	return "", nil
}
func (u unreachableBucket) UploadFileIntoUpdate(types.Update, string, io.Reader) error {
	u.t.Fatal("migration should have skipped")
	return nil
}
func (u unreachableBucket) DeleteUpdateFolder(string, string, string, string) error {
	u.t.Fatal("migration should have skipped")
	return nil
}
func (u unreachableBucket) CreateUpdateFrom(*types.Update, string) (*types.Update, error) {
	u.t.Fatal("migration should have skipped")
	return nil, nil
}
func (u unreachableBucket) RetrieveMigrationHistory() ([]string, error) {
	u.t.Fatal("migration should have skipped")
	return nil, nil
}
func (u unreachableBucket) ApplyMigration(string) error {
	u.t.Fatal("migration should have skipped")
	return nil
}
func (u unreachableBucket) RemoveMigrationFromHistory(string) error {
	u.t.Fatal("migration should have skipped")
	return nil
}

// resetEnv unsets every env var up() reads, then restores the previous
// values on cleanup. Prevents leakage across tests that run in the same
// process.
func resetEnv(t *testing.T) {
	t.Helper()
	vars := []string{"EXPO_APP_ID"}
	prev := map[string]string{}
	for _, v := range vars {
		prev[v] = os.Getenv(v)
		os.Unsetenv(v)
	}
	t.Cleanup(func() {
		for _, v := range vars {
			if prev[v] == "" {
				os.Unsetenv(v)
			} else {
				os.Setenv(v, prev[v])
			}
		}
	})
}

func TestUp_SkipsWhenEXPOAppIdUnset(t *testing.T) {
	// Without EXPO_APP_ID there is no v1 install to migrate from —
	// typically a fresh v2 deploy. Must no-op cleanly.
	resetEnv(t)
	assert.NoError(t, up(unreachableBucket{t: t}))
}

func TestUp_RunsOnSingleAppFlatEnv(t *testing.T) {
	// Positive path: single-app flat env → up() must reach the bucket.
	// Use a real LocalBucket on a v1 fixture to prove end-to-end that the
	// migration actually runs (not just returns nil).
	resetEnv(t)
	os.Setenv("EXPO_APP_ID", "app-1")

	base := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(base, "branch-a", "1", "12345"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(base, "branch-a", "1", "12345", ".check"), []byte("x"), 0o644))

	b := &bucket.LocalBucket{BasePath: base}
	require.NoError(t, up(b))

	// The v1 branch should now live under app-1/.
	_, err := os.Stat(filepath.Join(base, "app-1", "branch-a", "1", "12345", ".check"))
	assert.NoError(t, err)
}
