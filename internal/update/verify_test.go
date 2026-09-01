package update

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"xprem/internal/bucket"
	"xprem/internal/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupLocalBucket(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_BUCKET_BASE_PATH", base)
	t.Setenv("BASE_URL", "http://localhost:3000")
	t.Setenv("JWT_SECRET", "test_jwt_secret")
	bucket.ResetBucketInstance()
	t.Cleanup(bucket.ResetBucketInstance)
	return base
}

func writeUpdateFile(t *testing.T, base string, u types.Update, name, content string) {
	t.Helper()
	path := filepath.Join(base, u.AppId, u.Branch, u.RuntimeVersion, u.UpdateId, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func writeFolderUpdate(t *testing.T, base string, u types.Update, withAsset bool) {
	t.Helper()
	metadata := types.MetadataObject{FileMetadata: types.FileMetadata{IOS: types.PlatformMetadata{
		Bundle: "bundles/ios.js",
		Assets: []types.Asset{{Path: "assets/a", Ext: "png"}},
	}}}
	raw, err := json.Marshal(metadata)
	require.NoError(t, err)
	writeUpdateFile(t, base, u, "metadata.json", string(raw))
	writeUpdateFile(t, base, u, "bundles/ios.js", "bundle")
	if withAsset {
		writeUpdateFile(t, base, u, "assets/a", "asset")
	}
}

func TestVerifyUploadedUpdate_NilMappingVerifiesTheFolder(t *testing.T) {
	base := setupLocalBucket(t)
	u := types.Update{AppId: "app", Branch: "main", RuntimeVersion: "1", UpdateId: "100"}
	writeFolderUpdate(t, base, u, true)

	assert.NoError(t, VerifyUploadedUpdate(context.Background(), u, nil))
}

func TestVerifyUploadedUpdate_NilMappingReportsAMissingFile(t *testing.T) {
	base := setupLocalBucket(t)
	u := types.Update{AppId: "app", Branch: "main", RuntimeVersion: "1", UpdateId: "101"}
	writeFolderUpdate(t, base, u, false)

	err := VerifyUploadedUpdate(context.Background(), u, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing file: assets/a")
}
