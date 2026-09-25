package _0260422_v2_scope_data_under_appid

import (
	"os"
	"path/filepath"
	"testing"
	"xprem/internal/bucket"
	"xprem/internal/objectstore"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func v1Fixture(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(base, "branch-a", "1", "12345"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(base, "branch-a", "1", "12345", ".check"), []byte("x"), 0o644))
	return base
}

// Without EXPO_APP_ID there is no v1 install to migrate from, typically a
// fresh v2 deploy: the data must stay where it is.
func TestUp_SkipsWhenEXPOAppIdUnset(t *testing.T) {
	t.Setenv("EXPO_APP_ID", "")
	base := v1Fixture(t)

	require.NoError(t, up(bucket.Open(objectstore.ModeLocal, base, "")))

	assert.FileExists(t, filepath.Join(base, "branch-a", "1", "12345", ".check"))
}

func TestUp_RunsOnSingleAppFlatEnv(t *testing.T) {
	t.Setenv("EXPO_APP_ID", "app-1")
	base := v1Fixture(t)

	require.NoError(t, up(bucket.Open(objectstore.ModeLocal, base, "")))

	assert.FileExists(t, filepath.Join(base, "app-1", "branch-a", "1", "12345", ".check"))
}
