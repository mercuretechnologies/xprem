package bucket

import (
	"bytes"
	"fmt"
	"github.com/stretchr/testify/assert"
	"io"
	"os"
	testing2 "testing"
)

func setup(t *testing2.T) func() {
	return func() {
		ResetBucketInstance()
	}
}

// unwrap drops the validatingBucket decorator so tests can assert on the
// concrete backend type.
func unwrap(b Bucket) Bucket {
	if vb, ok := b.(*validatingBucket); ok {
		return vb.Inner
	}
	return b
}

func TestResolveLocalBucketType(t *testing2.T) {
	teardown := setup(t)
	defer teardown()
	os.Setenv("STORAGE_MODE", "local")
	bucketType := ResolveBucketType()
	assert.Equal(t, LocalBucketType, bucketType)
}

func TestResolveGCSBucketType(t *testing2.T) {
	teardown := setup(t)
	defer teardown()
	os.Setenv("STORAGE_MODE", "gcs")
	bucketType := ResolveBucketType()
	assert.Equal(t, GCSBucketType, bucketType)
}
func TestResolveS3BucketType(t *testing2.T) {
	teardown := setup(t)
	defer teardown()
	os.Setenv("STORAGE_MODE", "s3")
	bucketType := ResolveBucketType()
	assert.Equal(t, S3BucketType, bucketType)
}

func TestResolveAzureBucketType(t *testing2.T) {
	teardown := setup(t)
	defer teardown()
	os.Setenv("STORAGE_MODE", "azure")
	bucketType := ResolveBucketType()
	assert.Equal(t, AzureBucketType, bucketType)
}

func TestGetAzureBucket(t *testing2.T) {
	teardown := setup(t)
	defer teardown()
	os.Setenv("STORAGE_MODE", "azure")
	os.Setenv("AZURE_BLOB_CONTAINER_NAME", "test-container")
	defer os.Unsetenv("AZURE_BLOB_CONTAINER_NAME")
	resolvedBucket := GetBucket()
	azureBucket, ok := unwrap(resolvedBucket).(*AzureBucket)
	assert.True(t, ok, "expected *AzureBucket, got %T", unwrap(resolvedBucket))
	assert.Equal(t, "test-container", azureBucket.ContainerName)
}

func TestGetAzureBucketAppliesKeyPrefix(t *testing2.T) {
	teardown := setup(t)
	defer teardown()
	os.Setenv("STORAGE_MODE", "azure")
	os.Setenv("AZURE_BLOB_CONTAINER_NAME", "test-container")
	os.Setenv("BUCKET_KEY_PREFIX", "prefix")
	defer func() {
		os.Unsetenv("AZURE_BLOB_CONTAINER_NAME")
		os.Unsetenv("BUCKET_KEY_PREFIX")
	}()
	resolvedBucket := GetBucket()
	azureBucket, ok := unwrap(resolvedBucket).(*AzureBucket)
	assert.True(t, ok)
	assert.Equal(t, "prefix/", azureBucket.KeyPrefix)
	assert.Equal(t, "prefix/app/branch", azureBucket.prefixedKey("app/branch"))
}

func TestRequestUploadUrlsCarryAzureBlobTypeHeader(t *testing2.T) {
	teardown := setup(t)
	defer teardown()
	os.Setenv("STORAGE_MODE", "azure")
	os.Setenv("AZURE_BLOB_CONTAINER_NAME", "test-container")
	os.Setenv("AZURE_STORAGE_ACCOUNT_NAME", "devstoreaccount1")
	os.Setenv("AZURE_STORAGE_ACCOUNT_KEY", "Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw==")
	defer func() {
		os.Unsetenv("AZURE_BLOB_CONTAINER_NAME")
		os.Unsetenv("AZURE_STORAGE_ACCOUNT_NAME")
		os.Unsetenv("AZURE_STORAGE_ACCOUNT_KEY")
	}()
	requests, err := RequestUploadUrlsForFileUpdates("app", "branch", []UploadFile{{Name: "bundles/android.js", Hash: testBlobHash}})
	assert.Nil(t, err)
	assert.Len(t, requests, 1)
	assert.Equal(t, "android.js", requests[0].FileName)
	assert.Equal(t, "bundles/android.js", requests[0].FilePath)
	assert.Equal(t, "bundles/android.js", requests[0].OriginalFileName)
	assert.Equal(t, testBlobHash, requests[0].Hash)
	assert.Equal(t, "BlockBlob", requests[0].Headers["x-ms-blob-type"])
	assert.Contains(t, requests[0].RequestUploadUrl, "sig=")
}

func TestRequestUploadUrlsCarryNoHeadersOnLocal(t *testing2.T) {
	teardown := setup(t)
	defer teardown()
	os.Setenv("STORAGE_MODE", "local")
	os.Setenv("LOCAL_BUCKET_BASE_PATH", t.TempDir())
	os.Setenv("BASE_URL", "http://localhost:3000")
	os.Setenv("JWT_SECRET", "test_jwt_secret")
	requests, err := RequestUploadUrlsForFileUpdates("app", "branch", []UploadFile{{Name: "bundles/android.js", Hash: testBlobHash}})
	assert.Nil(t, err)
	assert.Len(t, requests, 1)
	assert.Equal(t, "android.js", requests[0].FileName)
	assert.Equal(t, "bundles/android.js", requests[0].OriginalFileName)
	assert.Nil(t, requests[0].Headers)
}

func TestConvertReadCloserToBytes(t *testing2.T) {
	teardown := setup(t)
	defer teardown()
	rc := io.NopCloser(bytes.NewReader([]byte("test")))
	bytes, err := ConvertReadCloserToBytes(rc)
	assert.Nil(t, err)
	assert.Equal(t, []byte("test"), bytes)
}

func TestErrorOnConvertReadCloserToBytes(t *testing2.T) {
	teardown := setup(t)
	defer teardown()
	errorReader := &ErrorReadCloser{
		ReadErr:  fmt.Errorf("simulated read error"),
		CloseErr: nil,
	}

	_, err := ConvertReadCloserToBytes(errorReader)

	assert.NotNil(t, err)
	assert.Contains(t, err.Error(), "error copying file to buffer")
	assert.Contains(t, err.Error(), "simulated read error")
}

type ErrorReadCloser struct {
	ReadErr  error
	CloseErr error
}

func (e *ErrorReadCloser) Read(p []byte) (int, error) {
	return 0, e.ReadErr
}

func (e *ErrorReadCloser) Close() error {
	return e.CloseErr
}

func TestGetS3Bucket(t *testing2.T) {
	teardown := setup(t)
	defer teardown()
	os.Setenv("STORAGE_MODE", "s3")
	os.Setenv("S3_BUCKET_NAME", "test")
	bucket := unwrap(GetBucket())
	assert.IsType(t, &S3Bucket{}, bucket)
}

func TestPrefixedKeyWithPrefix(t *testing2.T) {
	b := &S3Bucket{KeyPrefix: "myapp/"}
	assert.Equal(t, "myapp/branch/main", b.prefixedKey("branch/main"))
}

func TestPrefixedKeyWithoutPrefix(t *testing2.T) {
	b := &S3Bucket{KeyPrefix: ""}
	assert.Equal(t, "branch/main", b.prefixedKey("branch/main"))
}

func TestGetS3BucketWithKeyPrefix(t *testing2.T) {
	teardown := setup(t)
	defer teardown()
	os.Setenv("STORAGE_MODE", "s3")
	os.Setenv("S3_BUCKET_NAME", "test")
	os.Setenv("BUCKET_KEY_PREFIX", "myapp")
	defer os.Unsetenv("BUCKET_KEY_PREFIX")
	bucket := unwrap(GetBucket())
	s3b, ok := bucket.(*S3Bucket)
	assert.True(t, ok)
	assert.Equal(t, "myapp/", s3b.KeyPrefix)
}

func TestGetS3BucketKeyPrefixAlreadyHasSlash(t *testing2.T) {
	teardown := setup(t)
	defer teardown()
	os.Setenv("STORAGE_MODE", "s3")
	os.Setenv("S3_BUCKET_NAME", "test")
	os.Setenv("BUCKET_KEY_PREFIX", "myapp/")
	defer os.Unsetenv("BUCKET_KEY_PREFIX")
	bucket := unwrap(GetBucket())
	s3b, ok := bucket.(*S3Bucket)
	assert.True(t, ok)
	assert.Equal(t, "myapp/", s3b.KeyPrefix)
}

func TestGetS3BucketWithoutKeyPrefix(t *testing2.T) {
	teardown := setup(t)
	defer teardown()
	os.Setenv("STORAGE_MODE", "s3")
	os.Setenv("S3_BUCKET_NAME", "test")
	os.Unsetenv("BUCKET_KEY_PREFIX")
	os.Unsetenv("S3_KEY_PREFIX")
	bucket := unwrap(GetBucket())
	s3b, ok := bucket.(*S3Bucket)
	assert.True(t, ok)
	assert.Equal(t, "", s3b.KeyPrefix)
}

// TODO: remove once S3_KEY_PREFIX backward-compat is dropped.
func TestGetS3BucketWithLegacyS3KeyPrefix(t *testing2.T) {
	teardown := setup(t)
	defer teardown()
	os.Setenv("STORAGE_MODE", "s3")
	os.Setenv("S3_BUCKET_NAME", "test")
	os.Unsetenv("BUCKET_KEY_PREFIX")
	os.Setenv("S3_KEY_PREFIX", "legacy")
	defer os.Unsetenv("S3_KEY_PREFIX")
	bucket := unwrap(GetBucket())
	s3b, ok := bucket.(*S3Bucket)
	assert.True(t, ok)
	assert.Equal(t, "legacy/", s3b.KeyPrefix)
}

// TODO: remove once S3_KEY_PREFIX backward-compat is dropped.
func TestBucketKeyPrefixTakesPrecedenceOverLegacy(t *testing2.T) {
	teardown := setup(t)
	defer teardown()
	os.Setenv("STORAGE_MODE", "s3")
	os.Setenv("S3_BUCKET_NAME", "test")
	os.Setenv("BUCKET_KEY_PREFIX", "new")
	os.Setenv("S3_KEY_PREFIX", "legacy")
	defer os.Unsetenv("BUCKET_KEY_PREFIX")
	defer os.Unsetenv("S3_KEY_PREFIX")
	bucket := unwrap(GetBucket())
	s3b, ok := bucket.(*S3Bucket)
	assert.True(t, ok)
	assert.Equal(t, "new/", s3b.KeyPrefix)
}

func TestGetGCSBucketWithKeyPrefix(t *testing2.T) {
	teardown := setup(t)
	defer teardown()
	os.Setenv("STORAGE_MODE", "gcs")
	os.Setenv("GCS_BUCKET_NAME", "test-bucket")
	os.Setenv("BUCKET_KEY_PREFIX", "myapp")
	defer os.Unsetenv("BUCKET_KEY_PREFIX")
	bucket := unwrap(GetBucket())
	gcsb, ok := bucket.(*GCSBucket)
	assert.True(t, ok)
	assert.Equal(t, "myapp/", gcsb.KeyPrefix)
}

func TestKeyPrefixRejectsDotDotSegment(t *testing2.T) {
	teardown := setup(t)
	defer teardown()
	os.Setenv("STORAGE_MODE", "local")
	os.Setenv("LOCAL_BUCKET_BASE_PATH", "test")
	os.Setenv("BUCKET_KEY_PREFIX", "../etc")
	defer os.Unsetenv("BUCKET_KEY_PREFIX")
	assert.PanicsWithValue(t, "bucket key prefix must not contain '..' segments", func() { GetBucket() })
}

func TestKeyPrefixRejectsAbsolutePath(t *testing2.T) {
	teardown := setup(t)
	defer teardown()
	os.Setenv("STORAGE_MODE", "local")
	os.Setenv("LOCAL_BUCKET_BASE_PATH", "test")
	os.Setenv("BUCKET_KEY_PREFIX", "/etc")
	defer os.Unsetenv("BUCKET_KEY_PREFIX")
	assert.PanicsWithValue(t, "bucket key prefix must not be absolute (starts with '/')", func() { GetBucket() })
}

func TestKeyPrefixRejectsDotDotInMiddle(t *testing2.T) {
	teardown := setup(t)
	defer teardown()
	os.Setenv("STORAGE_MODE", "s3")
	os.Setenv("S3_BUCKET_NAME", "test")
	os.Setenv("BUCKET_KEY_PREFIX", "myapp/../other")
	defer os.Unsetenv("BUCKET_KEY_PREFIX")
	assert.PanicsWithValue(t, "bucket key prefix must not contain '..' segments", func() { GetBucket() })
}

func TestGetLocalBucketWithKeyPrefix(t *testing2.T) {
	teardown := setup(t)
	defer teardown()
	os.Setenv("STORAGE_MODE", "local")
	os.Setenv("LOCAL_BUCKET_BASE_PATH", "test")
	os.Setenv("BUCKET_KEY_PREFIX", "myapp")
	defer os.Unsetenv("BUCKET_KEY_PREFIX")
	bucket := unwrap(GetBucket())
	lb, ok := bucket.(*LocalBucket)
	assert.True(t, ok)
	assert.Equal(t, "myapp/", lb.KeyPrefix)
}

func TestGetLocalBucket(t *testing2.T) {
	teardown := setup(t)
	defer teardown()
	os.Setenv("STORAGE_MODE", "local")
	os.Setenv("LOCAL_BUCKET_BASE_PATH", "test")
	bucket := unwrap(GetBucket())
	assert.IsType(t, &LocalBucket{}, bucket)
}

func TestGetGCSBucket(t *testing2.T) {
	teardown := setup(t)
	defer teardown()
	os.Setenv("STORAGE_MODE", "gcs")
	os.Setenv("GCS_BUCKET_NAME", "test-bucket")
	bucket := unwrap(GetBucket())
	assert.IsType(t, &GCSBucket{}, bucket)
}
