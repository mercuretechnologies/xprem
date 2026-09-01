package bucket

import (
	"bytes"
	"context"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testBlobHash = "LPJNul-wow4m6DsqxbninhsWHlwfp0JecwQzYpOLmCQ"

func TestValidateBlobHash(t *testing.T) {
	assert.NoError(t, ValidateBlobHash(testBlobHash))
	assert.Error(t, ValidateBlobHash(""))
	assert.Error(t, ValidateBlobHash("short"))
	assert.Error(t, ValidateBlobHash(testBlobHash+"x"))
	assert.Error(t, ValidateBlobHash("LPJNul+wow4m6DsqxbninhsWHlwfp0JecwQzYpOLmCQ"))
	assert.Error(t, ValidateBlobHash("LPJNul/wow4m6DsqxbninhsWHlwfp0JecwQzYpOLmCQ"))
	// Same digest as testBlobHash, non-canonical spelling (trailing bits set).
	assert.Error(t, ValidateBlobHash("LPJNul-wow4m6DsqxbninhsWHlwfp0JecwQzYpOLmCR"))
}

func TestValidateUploadFile(t *testing.T) {
	assert.NoError(t, ValidateUploadFile("assets/icon.png", testBlobHash))
	assert.Error(t, ValidateUploadFile("", testBlobHash))
	assert.Error(t, ValidateUploadFile("../etc/passwd", testBlobHash))
	assert.Error(t, ValidateUploadFile("metadata.json", "short"))
}

func TestBlobObjectKey(t *testing.T) {
	assert.Equal(t, "app-1/cas/"+testBlobHash, BlobObjectKey("app-1", testBlobHash))
}

func TestLocalBucket_CASRoundTrip(t *testing.T) {
	b := &LocalBucket{BasePath: t.TempDir()}
	ctx := context.Background()

	exists, err := b.BlobExists(ctx, "app-1", testBlobHash)
	require.NoError(t, err)
	assert.False(t, exists)

	got, err := b.GetBlob(ctx, "app-1", testBlobHash)
	require.NoError(t, err)
	assert.Nil(t, got)

	require.NoError(t, b.PutBlob(ctx, "app-1", testBlobHash, bytes.NewReader([]byte("hello"))))

	exists, err = b.BlobExists(ctx, "app-1", testBlobHash)
	require.NoError(t, err)
	assert.True(t, exists)

	got, err = b.GetBlob(ctx, "app-1", testBlobHash)
	require.NoError(t, err)
	require.NotNil(t, got)
	defer got.Reader.Close()
	body, err := ConvertReadCloserToBytes(got.Reader)
	require.NoError(t, err)
	assert.Equal(t, []byte("hello"), body)
}

func TestUploadPathIsBlob(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOCAL_BUCKET_BASE_PATH", root)
	t.Setenv("BUCKET_KEY_PREFIX", "")
	t.Setenv("S3_KEY_PREFIX", "")

	blobPath := filepath.Join(root, "app1", casDir, testBlobHash)
	assert.True(t, uploadPathIsBlob(blobPath, "app1"))
	assert.False(t, uploadPathIsBlob(filepath.Join(root, "app1", "main", "1.0", "17", "bundle.js"), "app1"))
	assert.False(t, uploadPathIsBlob(filepath.Join(root, "app1", casDir, "not-a-hash"), "app1"))
	assert.False(t, uploadPathIsBlob(filepath.Join(root, "app2", casDir, testBlobHash), "app1"))
	assert.False(t, uploadPathIsBlob(filepath.Join(root, "app1", casDir)+"/../main/secret", "app1"))
}

func TestValidatingBucket_CAS_RejectsBadHash(t *testing.T) {
	stub := &stubBucket{}
	v := &validatingBucket{Inner: stub}
	_, err := v.BlobExists(context.Background(), "app-1", "not-a-hash")
	assert.Error(t, err)
	assert.False(t, stub.called)
}

func TestValidatingBucket_CAS_DelegatesOnValidInput(t *testing.T) {
	stub := &stubBucket{}
	v := &validatingBucket{Inner: stub}
	_, err := v.BlobExists(context.Background(), "app-1", testBlobHash)
	assert.NoError(t, err)
	assert.True(t, stub.called)
}

func TestLocalBucket_RequestBlobUploadURL_TokenAcceptsCASPath(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOCAL_BUCKET_BASE_PATH", root)
	t.Setenv("BUCKET_KEY_PREFIX", "")
	t.Setenv("S3_KEY_PREFIX", "")
	t.Setenv("BASE_URL", "http://localhost:3000")
	t.Setenv("JWT_SECRET", "test_jwt_secret")
	t.Setenv("DB_URL", "postgres://localhost/xprem")

	b := &LocalBucket{BasePath: root}
	uploadURL, err := b.RequestBlobUploadURL("app-1", testBlobHash, "production")
	require.NoError(t, err)

	parsed, err := url.Parse(uploadURL)
	require.NoError(t, err)
	filePath, appId, branch, err := ValidateUploadTokenAndResolveFilePath(parsed.Query().Get("token"))
	require.NoError(t, err)
	assert.Equal(t, "app-1", appId)
	assert.Equal(t, "production", branch)
	assert.Equal(t, filepath.Join(root, "app-1", casDir, testBlobHash), filePath)
}
