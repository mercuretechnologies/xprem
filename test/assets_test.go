package test

import (
	"bytes"
	"compress/gzip"
	"github.com/andybalholm/brotli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"io"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"xprem/internal/crypto"
	"xprem/internal/types"
	"xprem/internal/update"
)

func TestBadPlatformForAssets(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://localhost:3000/assets?h="+pngBlobHash+"&ext=png&platform=blackberry", nil)
	r.Header.Set("expo-app-id", "test-app-id")
	testContainer().ExpoProtocolHandler.HandleAssets(w, r)
	assert.Equal(t, 400, w.Code, "Expected status code 400 for an invalid platform")
	assert.Equal(t, "Invalid platform\n", w.Body.String())
}

// Blob hashes of the checked-in cas/ fixtures (the android launch bundle and
// the shared png, as published by the CAS pipeline).
const (
	androidBundleBlobHash = "t3kWQ00Lhn5qCGGhNNMxiD_pcTO_4d7I_1zO3S5Me5k"
	pngBlobHash           = "JCcs2u_4LMX6zazNmCpvBbYMRQRwS7-UwZpjiGWYgLs"
)

func blobRequest(t *testing.T, hash, ext, platform string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	assetURL, err := update.BuildBlobAssetURL("http://localhost:3000/assets", hash, ext, types.Platform(platform))
	require.NoError(t, err, "Error building blob URL")
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", assetURL, nil)
	r.Header.Set("expo-app-id", "test-app-id")
	for key, value := range headers {
		r.Header.Set(key, value)
	}
	testContainer().ExpoProtocolHandler.HandleAssets(w, r)
	return w
}

func fixtureBlob(t *testing.T, hash string) []byte {
	t.Helper()
	projectRoot, err := findProjectRoot()
	require.NoError(t, err)
	content, err := os.ReadFile(filepath.Join(projectRoot, "/test/test-updates/test-app-id/cas/"+hash))
	require.NoError(t, err, "Expected the fixture blob to exist")
	return content
}

func TestToRetrieveBundleAsset(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	w := blobRequest(t, androidBundleBlobHash, ".bundle", "android", nil)
	assert.Equal(t, 200, w.Code, "Expected status code 200")
	assert.Equal(t, "application/javascript", w.Header().Get("Content-Type"), "Expected content type 'application/javascript'")
	assert.Equal(t, string(fixtureBlob(t, androidBundleBlobHash)), w.Body.String(), "Expected the exact blob bytes")
	// The CAS integrity contract, as the device verifies it: the served bytes
	// must hash back to the h they were requested by.
	servedHash, err := crypto.CreateHash(w.Body.Bytes(), "sha256", "base64")
	require.NoError(t, err)
	assert.Equal(t, androidBundleBlobHash, crypto.GetBase64URLEncoding(servedHash), "served bytes must hash to the requested h")
}

func TestBlobAssetErrors(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	unknown := blobRequest(t, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", ".png", "android", nil)
	assert.Equal(t, 404, unknown.Code, "An unknown blob must 404")
	assert.Equal(t, "Asset not found\n", unknown.Body.String())

	malformedURL := "http://localhost:3000/assets?h=..%2F..%2Fetc%2Fpasswd&ext=png&platform=android"
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", malformedURL, nil)
	r.Header.Set("expo-app-id", "test-app-id")
	testContainer().ExpoProtocolHandler.HandleAssets(w, r)
	assert.Equal(t, 400, w.Code, "A malformed hash must 400 before touching the bucket")
	assert.Equal(t, "Invalid asset hash\n", w.Body.String())
}

// TestUnknownAppIdForAssets mirrors the manifest-side 404 guard: an
// unknown expo-app-id must fail at the edge before any outbound Expo API
// call is attempted, otherwise the handler ends up proxying a confusing
// upstream 401 as a 500.
func TestUnknownAppIdForAssets(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	assetURL, err := update.BuildBlobAssetURL("http://localhost:3000/assets", androidBundleBlobHash, ".bundle", "android")
	require.NoError(t, err)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", assetURL, nil)
	r.Header.Set("expo-app-id", "this-id-is-not-in-apps-json")
	testContainer().ExpoProtocolHandler.HandleAssets(w, r)
	assert.Equal(t, 404, w.Code, "Unknown app id must fail early with 404")
	assert.Equal(t, "Unknown app id\n", w.Body.String())
}

func TestToRetrieveBundleAssetWithGzipCompression(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	w := blobRequest(t, androidBundleBlobHash, ".bundle", "android", map[string]string{"Accept-Encoding": "gzip"})
	assert.Equal(t, 200, w.Code, "Expected status code 200")
	assert.Equal(t, "application/javascript", w.Header().Get("Content-Type"), "Expected 'application/javascript' content type")
	assert.Equal(t, "gzip", w.Header().Get("Content-Encoding"), "Expected 'gzip' content encoding")
	reader, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatalf("Failed to create gzip reader: %v", err)
	}
	defer reader.Close()
	decompressedBody, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("Failed to read decompressed content: %v", err)
	}
	assert.Equal(t, string(fixtureBlob(t, androidBundleBlobHash)), string(decompressedBody), "Expected content does not match decompressed content")
}

func TestToRetrieveBundleAssetWithBrotliCompression(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	w := blobRequest(t, androidBundleBlobHash, ".bundle", "android", map[string]string{"Accept-Encoding": "br"})
	assert.Equal(t, 200, w.Code, "Expected status code 200")
	assert.Equal(t, "application/javascript", w.Header().Get("Content-Type"), "Expected 'application/javascript' content type")
	assert.Equal(t, "br", w.Header().Get("Content-Encoding"), "Expected 'br' content encoding")
	decompressedBody := new(bytes.Buffer)
	brReader := brotli.NewReader(w.Body)
	if _, err := io.Copy(decompressedBody, brReader); err != nil {
		t.Fatalf("Failed to decompress Brotli content: %v", err)
	}
	assert.Equal(t, string(fixtureBlob(t, androidBundleBlobHash)), decompressedBody.String(), "Expected content does not match decompressed content")
}

func TestToRetrievePNGAssetWithGzipCompression(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	w := blobRequest(t, pngBlobHash, ".png", "android", map[string]string{"Accept-Encoding": "gzip"})
	assert.Equal(t, 200, w.Code, "Expected status code 200")
	assert.Equal(t, "image/png", w.Header().Get("Content-Type"), "Expected 'image/png' content type")
	assert.Equal(t, "gzip", w.Header().Get("Content-Encoding"), "Expected 'gzip' content encoding")
	reader, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatalf("Failed to create gzip reader: %v", err)
	}
	defer reader.Close()
}

func TestAutomaticUrlRedirectionIfCDNIsSet(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	projectRoot, _ := findProjectRoot()
	os.Setenv("PRIVATE_CLOUDFRONT_KEY_PATH", filepath.Join(projectRoot, "/test/keys/private-key-cloudfront-test.pem"))
	os.Setenv("CLOUDFRONT_DOMAIN", "https://cdn.expoopenota.com")
	os.Setenv("CLOUDFRONT_KEY_PAIR_ID", "test")

	w := blobRequest(t, androidBundleBlobHash, ".bundle", "android", nil)
	assert.Equal(t, 302, w.Code, "Expected status code 302")
	parsedUrl, err := url.Parse(w.Header().Get("Location"))
	require.NoError(t, err, "Error while parsing the redirect URL")
	assert.Equal(t, "https://cdn.expoopenota.com/test-app-id/cas/"+androidBundleBlobHash,
		parsedUrl.Scheme+"://"+parsedUrl.Host+parsedUrl.Path, "The redirect must target the blob key")
	queryParams := parsedUrl.Query()
	assert.NotEmpty(t, queryParams.Get("Policy"), "Policy should not be empty")
	assert.NotEmpty(t, queryParams.Get("Signature"), "Signature should not be empty")
	assert.NotEmpty(t, queryParams.Get("Key-Pair-Id"), "Key-Pair-Id should not be empty")
}

// The header used to let any client turn the CDN off and make the origin serve
// the bytes itself.
func TestCDNRedirectionCannotBeTurnedOffByAHeader(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	projectRoot, _ := findProjectRoot()
	os.Setenv("PRIVATE_CLOUDFRONT_KEY_PATH", filepath.Join(projectRoot, "/test/keys/private-key-cloudfront-test.pem"))
	os.Setenv("CLOUDFRONT_DOMAIN", "https://cdn.expoopenota.com")
	os.Setenv("CLOUDFRONT_KEY_PAIR_ID", "test")

	w := blobRequest(t, androidBundleBlobHash, ".bundle", "android", map[string]string{"prevent-cdn-redirection": "true"})
	assert.Equal(t, 302, w.Code, "the CDN redirect must happen despite the header")
}
