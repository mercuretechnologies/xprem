package bucket

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func blobHash(content []byte) string {
	sum := sha256.Sum256(content)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// localUploadEnv points GetBucket at a temporary local bucket and returns its
// directory.
func localUploadEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_BUCKET_BASE_PATH", dir)
	t.Setenv("BUCKET_KEY_PREFIX", "")
	t.Setenv("S3_KEY_PREFIX", "")
	t.Setenv("BASE_URL", "http://localhost:3000")
	t.Setenv("JWT_SECRET", "test_jwt_secret")
	t.Setenv("DB_URL", "postgres://localhost/xprem")
	ResetBucketInstance()
	t.Cleanup(ResetBucketInstance)
	return dir
}

func TestLocalUploadTokenGrantsOneKey(t *testing.T) {
	localUploadEnv(t)
	b := GetBucket()

	blob, err := b.BlobStore.PresignPut(context.Background(), "app-1", testBlobHash, "production")
	require.NoError(t, err)
	parsed, err := url.Parse(blob.URL)
	require.NoError(t, err)
	assert.Equal(t, "/app-1/uploadLocalFile", parsed.Path)
	assert.Empty(t, parsed.RawQuery, "the grant travels in a header, not in URLs and request logs")
	key, appId, branch, err := ValidateUploadToken(blob.Headers[LocalUploadTokenHeader])
	require.NoError(t, err)
	assert.Equal(t, BlobObjectKey("app-1", testBlobHash), key)
	assert.Equal(t, "app-1", appId)
	assert.Equal(t, "production", branch)

	file, err := b.UpdateStore.PresignPut(context.Background(), "app-1", "main", "1.0.0", "17", "bundle.js")
	require.NoError(t, err)
	key, _, branch, err = ValidateUploadToken(file.Headers[LocalUploadTokenHeader])
	require.NoError(t, err)
	assert.Equal(t, "app-1/main/1.0.0/17/bundle.js", key)
	assert.Equal(t, "main", branch)
	assert.NotEqual(t, blob.Headers[LocalUploadTokenHeader], file.Headers[LocalUploadTokenHeader], "each file has its own grant")
}

// A token is only ever minted for a key inside the branch it names, or for a
// blob; anything else is forged.
func TestValidateUploadTokenPinsTheKeyToItsBranch(t *testing.T) {
	localUploadEnv(t)
	for name, claims := range map[string]uploadClaims{
		"another branch":            {Key: "app1/other/1.0.0/17/bundle.js", AppID: "app1", Branch: "main"},
		"a branch sharing a prefix": {Key: "app1/mainline/1.0.0/17/bundle.js", AppID: "app1", Branch: "main"},
		"another app":               {Key: "app2/main/1.0.0/17/bundle.js", AppID: "app1", Branch: "main"},
		"the branch directory":      {Key: "app1/main/", AppID: "app1", Branch: "main"},
		"traversal":                 {Key: "app1/main/../../etc/cron.d/pwn", AppID: "app1", Branch: "main"},
		"a blob of another app":     {Key: BlobObjectKey("app2", testBlobHash), AppID: "app1", Branch: "main"},
		"a blob without a branch":   {Key: BlobObjectKey("app1", testBlobHash), AppID: "app1", Branch: ""},
	} {
		t.Run(name, func(t *testing.T) {
			token, err := mintUploadToken(claims)
			require.NoError(t, err)
			_, _, _, err = ValidateUploadToken(token)
			assert.Error(t, err)
		})
	}
}

func TestHandleUploadVerifiesBlobsAndWritesAtomically(t *testing.T) {
	dir := localUploadEnv(t)
	ctx := context.Background()
	content := []byte("bundle bytes")

	require.NoError(t, HandleUpload(ctx, "app-1", BlobObjectKey("app-1", blobHash(content)), bytes.NewReader(content)))
	written, err := os.ReadFile(filepath.Join(dir, "app-1", casDir, blobHash(content)))
	require.NoError(t, err)
	assert.Equal(t, content, written)

	tampered := BlobObjectKey("app-1", blobHash([]byte("what the CLI hashed")))
	require.ErrorIs(t, HandleUpload(ctx, "app-1", tampered, bytes.NewReader([]byte("what it sent"))), ErrBlobHashMismatch)
	assert.NoFileExists(t, filepath.Join(dir, "app-1", casDir, blobHash([]byte("what the CLI hashed"))), "a blob that does not match its hash is not written")

	interrupted := BlobObjectKey("app-1", blobHash([]byte("full payload")))
	body := io.MultiReader(strings.NewReader("full pay"), iotest.ErrReader(errors.New("connection reset")))
	require.Error(t, HandleUpload(ctx, "app-1", interrupted, body))
	assert.NoFileExists(t, filepath.Join(dir, "app-1", casDir, blobHash([]byte("full payload"))), "an interrupted upload leaves no partial blob")

	require.NoError(t, HandleUpload(ctx, "app-1", "app-1/production/1/1737455526/metadata.json", strings.NewReader(`{"version":0}`)))
	written, err = os.ReadFile(filepath.Join(dir, "app-1", "production", "1", "1737455526", "metadata.json"))
	require.NoError(t, err)
	assert.Equal(t, `{"version":0}`, string(written), "update folder files are stored without a hash check")

	require.NoError(t, filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		require.NoError(t, err)
		require.False(t, strings.HasPrefix(d.Name(), ".upload-"), "temporary file left behind: %s", path)
		return nil
	}))
}

// One publish, one call: the config files land in the update folder, the
// assets in cas/, and a blob named twice is presigned once.
func TestRequestUploadUrlsRouteByDestination(t *testing.T) {
	localUploadEnv(t)

	requests, err := RequestUploadUrlsForFileUpdates(context.Background(), "app", "branch", "1", "100", []UploadFile{
		{Name: "metadata.json", Hash: testBlobHash, InUpdateFolder: true},
		{Name: "bundles/android.js", Hash: testBlobHash},
		{Name: "assets/copy-of-bundle", Hash: testBlobHash},
	})
	require.NoError(t, err)
	require.Len(t, requests, 2)

	byPath := map[string]FileUploadRequest{}
	for _, request := range requests {
		byPath[request.FilePath] = request
		assert.Equal(t, "http://localhost:3000/app/uploadLocalFile", request.RequestUploadUrl)
		assert.NotEmpty(t, request.Headers[LocalUploadTokenHeader])
	}
	assert.Equal(t, "android.js", byPath["bundles/android.js"].FileName)
	assert.Contains(t, byPath, "metadata.json")
	assert.NotContains(t, byPath, "assets/copy-of-bundle", "same blob presigned once")
}

func TestRequestUploadUrlsCarryAzureBlobTypeHeader(t *testing.T) {
	t.Setenv("STORAGE_MODE", "azure")
	t.Setenv("AZURE_BLOB_CONTAINER_NAME", "test-container")
	t.Setenv("AZURE_STORAGE_ACCOUNT_NAME", "devstoreaccount1")
	t.Setenv("AZURE_STORAGE_ACCOUNT_KEY", "Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw==")
	ResetBucketInstance()
	t.Cleanup(ResetBucketInstance)

	requests, err := RequestUploadUrlsForFileUpdates(context.Background(), "app", "branch", "1", "100", []UploadFile{{Name: "bundles/android.js", Hash: testBlobHash}})
	require.NoError(t, err)
	require.Len(t, requests, 1)
	assert.Equal(t, "BlockBlob", requests[0].Headers["x-ms-blob-type"])
	assert.Contains(t, requests[0].RequestUploadUrl, "sig=")
}
