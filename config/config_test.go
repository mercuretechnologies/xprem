package config

import (
	"github.com/stretchr/testify/assert"
	"os"
	"os/exec"
	testing2 "testing"
)

func setup(t *testing2.T) func() {
	return func() {
	}
}
func TestNotValidStorage(t *testing2.T) {
	teardown := setup(t)
	defer teardown()
	isValid := validateStorageMode("bag")
	assert.False(t, isValid)
}

func TestValidLocalStorage(t *testing2.T) {
	teardown := setup(t)
	defer teardown()
	isValid := validateStorageMode("local")
	assert.True(t, isValid)
}

func TestNotValidEmptyBaseUrl(t *testing2.T) {
	teardown := setup(t)
	defer teardown()
	isValid := validateBaseUrl("")
	assert.False(t, isValid)
}

func TestNotValidBaseUrl(t *testing2.T) {
	teardown := setup(t)
	defer teardown()
	isValid := validateBaseUrl("test.com")
	assert.False(t, isValid)
}

func TestMissingBucketParamsForS3(t *testing2.T) {
	teardown := setup(t)
	defer teardown()
	os.Setenv("S3_BUCKET_NAME", "")
	bucketParams := validateBucketParams("s3")
	assert.False(t, bucketParams)
}

func TestValidAzureStorageMode(t *testing2.T) {
	teardown := setup(t)
	defer teardown()
	assert.True(t, validateStorageMode("azure"))
}

func TestMissingBucketParamsForAzure(t *testing2.T) {
	teardown := setup(t)
	defer teardown()
	os.Setenv("AZURE_BLOB_CONTAINER_NAME", "")
	os.Setenv("AZURE_STORAGE_ACCOUNT_NAME", "")
	os.Setenv("AZURE_STORAGE_ACCOUNT_KEY", "")
	assert.False(t, validateBucketParams("azure"))

	os.Setenv("AZURE_BLOB_CONTAINER_NAME", "container")
	os.Setenv("AZURE_STORAGE_ACCOUNT_NAME", "account")
	assert.False(t, validateBucketParams("azure"), "account key must be required")

	os.Setenv("AZURE_STORAGE_ACCOUNT_KEY", "key")
	defer func() {
		os.Unsetenv("AZURE_BLOB_CONTAINER_NAME")
		os.Unsetenv("AZURE_STORAGE_ACCOUNT_NAME")
		os.Unsetenv("AZURE_STORAGE_ACCOUNT_KEY")
	}()
	assert.True(t, validateBucketParams("azure"))
}

func TestMissingBucketParamsForLocal(t *testing2.T) {
	teardown := setup(t)
	defer teardown()
	os.Setenv("LOCAL_BUCKET_BASE_PATH", "")
	bucketParams := validateBucketParams("local")
	// Should be set as ./updates by default config values
	assert.True(t, bucketParams)
}

func TestValidBaseUrl(t *testing2.T) {
	teardown := setup(t)
	defer teardown()
	isValid := validateBaseUrl("http://test.com")
	assert.True(t, isValid)
}

func TestValidBaseUrlWithPath(t *testing2.T) {
	assert.True(t, validateBaseUrl("https://api.example.com/ota"))
	assert.True(t, validateBaseUrl("https://api.example.com/path1/path2"))
}

func TestNotValidBaseUrlWithQueryOrFragment(t *testing2.T) {
	assert.False(t, validateBaseUrl("https://api.example.com/ota?foo=bar"))
	assert.False(t, validateBaseUrl("https://api.example.com/ota#section"))
	assert.False(t, validateBaseUrl("https://api.example.com?"))
}

func TestPublicPath(t *testing2.T) {
	t.Setenv("BASE_URL", "https://ota.example.com")
	assert.Equal(t, "", PublicPath())
	assert.Equal(t, "/dashboard/", PublicHref("/dashboard/"))

	t.Setenv("BASE_URL", "https://ota.example.com/")
	assert.Equal(t, "", PublicPath())

	t.Setenv("BASE_URL", "https://api.example.com/ota")
	assert.Equal(t, "/ota", PublicPath())
	assert.Equal(t, "/ota/dashboard/", PublicHref("/dashboard/"))
	assert.Equal(t, "/ota/manifest", PublicHref("/manifest"))

	t.Setenv("BASE_URL", "https://api.example.com/path1/path2/")
	assert.Equal(t, "/path1/path2", PublicPath())
	assert.Equal(t, "/path1/path2/dashboard", PublicHref("/dashboard"))
}

func TestNotValidConfigStorage(t *testing2.T) {
	teardown := setup(t)
	defer teardown()
	os.Setenv("STORAGE_MODE", "bag")
	os.Setenv("BASE_URL", "http://test.com")
	os.Setenv("JWT_SECRET", "test")
	if os.Getenv("TEST_SUBPROCESS") == "1" {
		LoadConfig()
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestNotValidConfig")
	cmd.Env = append(os.Environ(), "TEST_SUBPROCESS=1")
	err := cmd.Run()

	assert.Error(t, err)
	exitError, ok := err.(*exec.ExitError)
	assert.True(t, ok)
	assert.Equal(t, 1, exitError.ExitCode())
}

func TestValidConfig(t *testing2.T) {
	teardown := setup(t)
	defer teardown()
	os.Setenv("STORAGE_MODE", "local")
	os.Setenv("BASE_URL", "http://test.com")
	os.Setenv("JWT_SECRET", "test")
	os.Setenv("LOCAL_BUCKET_BASE_PATH", "./updates")
	LoadConfig()
}

func TestFallbackDefaultEnv(t *testing2.T) {
	teardown := setup(t)
	defer teardown()
	os.Setenv("STORAGE_MODE", "local")
	os.Setenv("BASE_URL", "http://test.com")
	os.Setenv("JWT_SECRET", "test")
	os.Setenv("LOCAL_BUCKET_BASE_PATH", "")
	LoadConfig()
	localBucketBasePath := GetEnv("LOCAL_BUCKET_BASE_PATH")
	assert.Equal(t, DefaultEnvValues["LOCAL_BUCKET_BASE_PATH"], localBucketBasePath)
}

func TestNotSetEnv(t *testing2.T) {
	teardown := setup(t)
	defer teardown()
	os.Setenv("STORAGE_MODE", "local")
	os.Setenv("BASE_URL", "http://test.com")
	os.Setenv("JWT_SECRET", "test")
	os.Setenv("LOCAL_BUCKET_BASE_PATH", "")
	LoadConfig()
	assert.Empty(t, GetEnv("NOT_FOUND"))
}

func TestAwsBaseEndpointSet(t *testing2.T) {
	teardown := setup(t)
	defer teardown()
	os.Setenv("STORAGE_MODE", "local")
	os.Setenv("BASE_URL", "http://test.com")
	os.Setenv("JWT_SECRET", "test")
	os.Setenv("LOCAL_BUCKET_BASE_PATH", "./updates")

	expectedEndpoint := "https://test-account.r2.cloudflarestorage.com"
	os.Setenv("AWS_BASE_ENDPOINT", expectedEndpoint)
	LoadConfig()
	actualEndpoint := GetEnv("AWS_BASE_ENDPOINT")
	assert.Equal(t, expectedEndpoint, actualEndpoint)
}

func TestAwsBaseEndpointNotSet(t *testing2.T) {
	teardown := setup(t)
	defer teardown()
	os.Setenv("STORAGE_MODE", "local")
	os.Setenv("BASE_URL", "http://test.com")
	os.Setenv("JWT_SECRET", "test")
	os.Setenv("LOCAL_BUCKET_BASE_PATH", "./updates")
	os.Unsetenv("AWS_BASE_ENDPOINT")
	LoadConfig()
	endpoint := GetEnv("AWS_BASE_ENDPOINT")
	assert.Equal(t, DefaultEnvValues["AWS_BASE_ENDPOINT"], endpoint)
	assert.Empty(t, endpoint)
}

func TestTestMode(t *testing2.T) {
	teardown := setup(t)
	defer teardown()
	testMode := IsTestMode()
	assert.True(t, testMode)
}

// The switch is off unless the operator says otherwise, and a value that is
// not a boolean is not a way to turn collection off by accident.
func TestDeviceTelemetryDisabled(t *testing2.T) {
	teardown := setup(t)
	defer teardown()
	for value, expected := range map[string]bool{
		"":      false,
		"false": false,
		"0":     false,
		"maybe": false,
		"true":  true,
		"1":     true,
		"TRUE":  true,
	} {
		t.Setenv("DISABLE_DEVICE_TELEMETRY", value)
		assert.Equal(t, expected, IsDeviceTelemetryDisabled(), "DISABLE_DEVICE_TELEMETRY=%q", value)
	}
}
