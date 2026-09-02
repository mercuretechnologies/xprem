package cdn

import (
	"strings"
	testing2 "testing"
)

func clearCDNEnv(t *testing2.T) {
	t.Setenv("STORAGE_MODE", "")
	t.Setenv("S3_BUCKET_NAME", "")
	t.Setenv("GCS_BUCKET_NAME", "")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS_B64", "")
	t.Setenv("CDN_BASE_URL", "")
	t.Setenv("S3_CDN_PREFIX", "")
	t.Setenv("BUCKET_KEY_PREFIX", "")
	t.Setenv("S3_KEY_PREFIX", "")
	t.Setenv("KEYS_STORAGE_TYPE", "")
	t.Setenv("CLOUDFRONT_DOMAIN", "")
	t.Setenv("CLOUDFRONT_KEY_PAIR_ID", "")
	t.Setenv("PRIVATE_CLOUDFRONT_KEY_PATH", "")
	t.Setenv("PRIVATE_CLOUDFRONT_KEY_B64", "")
	t.Setenv("AWSSM_CLOUDFRONT_PRIVATE_KEY_SECRET_ID", "")
	t.Setenv("AZURE_BLOB_CONTAINER_NAME", "")
	t.Setenv("AZURE_STORAGE_ACCOUNT_NAME", "")
	t.Setenv("AZURE_STORAGE_ACCOUNT_KEY", "")
	t.Setenv("AZURE_BLOB_ENDPOINT", "")
	t.Setenv("DISABLE_S3_DIRECT_CDN", "")
	t.Setenv("AWS_BASE_ENDPOINT", "")
	t.Setenv("AWS_S3_FORCE_PATH_STYLE", "")
}

// The aws package builds its S3 client once per process, so every s3-direct
// test must set the exact same AWS env before the first presign.
func setS3CDNEnv(t *testing2.T) {
	t.Setenv("STORAGE_MODE", "s3")
	t.Setenv("S3_BUCKET_NAME", "test-bucket")
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "test-access-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test-secret-key")
}

func TestGetCDNReturnsS3DirectWhenS3Configured(t *testing2.T) {
	clearCDNEnv(t)
	setS3CDNEnv(t)
	ResetCDNInstance()
	c := GetCDN()
	if c == nil {
		t.Fatalf("expected CDN instance, got nil")
	}
	if _, ok := c.(*S3DirectCDN); !ok {
		t.Fatalf("expected *S3DirectCDN, got %T", c)
	}
}

func TestGetCDNReturnsGenericWithCDNBaseURLOnS3(t *testing2.T) {
	clearCDNEnv(t)
	setS3CDNEnv(t)
	// s3-direct is always available in s3 mode with credentials, so the
	// explicitly configured base URL must win.
	t.Setenv("CDN_BASE_URL", "https://cdn.example.com")
	ResetCDNInstance()
	c := GetCDN()
	if _, ok := c.(*GenericCDN); !ok {
		t.Fatalf("expected *GenericCDN, got %T", c)
	}
}

func TestGetCDNSkipsS3DirectWhenDisabled(t *testing2.T) {
	clearCDNEnv(t)
	setS3CDNEnv(t)
	t.Setenv("DISABLE_S3_DIRECT_CDN", "true")
	ResetCDNInstance()
	if c := GetCDN(); c != nil {
		t.Fatalf("expected no CDN with s3-direct disabled, got %T", c)
	}
}

func TestS3DirectComputeRedirectionURLForBlob(t *testing2.T) {
	clearCDNEnv(t)
	setS3CDNEnv(t)
	t.Setenv("BUCKET_KEY_PREFIX", "prefix")
	c := &S3DirectCDN{}
	got, err := c.ComputeRedirectionURLForBlob("app-1", "LPJNul-wow4m6DsqxbninhsWHlwfp0JecwQzYpOLmCQ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantPrefix := "https://test-bucket.s3.us-east-1.amazonaws.com/prefix/app-1/cas/LPJNul-wow4m6DsqxbninhsWHlwfp0JecwQzYpOLmCQ?"
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("expected URL to start with %q, got %q", wantPrefix, got)
	}
}

func TestS3DirectComputeRedirectionURL(t *testing2.T) {
	clearCDNEnv(t)
	setS3CDNEnv(t)
	t.Setenv("BUCKET_KEY_PREFIX", "prefix")
	c := &S3DirectCDN{}
	got, err := c.ComputeRedirectionURLForAsset("app-1", "production", "1", "1674170951", "bundles/android.js")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantPrefix := "https://test-bucket.s3.us-east-1.amazonaws.com/prefix/app-1/production/1/1674170951/bundles/android.js?"
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("expected URL to start with %q, got %q", wantPrefix, got)
	}
	for _, param := range []string{"X-Amz-Signature=", "X-Amz-Expires=900", "X-Amz-Credential="} {
		if !strings.Contains(got, param) {
			t.Fatalf("expected URL to contain %q, got %q", param, got)
		}
	}
}

func setAzureCDNEnv(t *testing2.T) {
	t.Setenv("STORAGE_MODE", "azure")
	t.Setenv("AZURE_BLOB_CONTAINER_NAME", "updates")
	t.Setenv("AZURE_STORAGE_ACCOUNT_NAME", "devstoreaccount1")
	t.Setenv("AZURE_STORAGE_ACCOUNT_KEY", "Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw==")
}

func TestGetCDNReturnsAzureDirectWhenAzureConfigured(t *testing2.T) {
	clearCDNEnv(t)
	setAzureCDNEnv(t)
	ResetCDNInstance()
	c := GetCDN()
	if c == nil {
		t.Fatalf("expected CDN instance, got nil")
	}
	if _, ok := c.(*AzureBlobDirectCDN); !ok {
		t.Fatalf("expected *AzureBlobDirectCDN, got %T", c)
	}
}

func TestGetCDNReturnsGenericWithCDNBaseURLOnAzure(t *testing2.T) {
	clearCDNEnv(t)
	setAzureCDNEnv(t)
	// azure-direct is always available in azure mode, so the explicitly
	// configured base URL (a Front Door or any CDN in front of a public
	// container) must win or it would never be reachable.
	t.Setenv("CDN_BASE_URL", "https://cdn.example.com")
	ResetCDNInstance()
	c := GetCDN()
	if _, ok := c.(*GenericCDN); !ok {
		t.Fatalf("expected *GenericCDN, got %T", c)
	}
}

func TestAzureDirectComputeRedirectionURLForBlob(t *testing2.T) {
	clearCDNEnv(t)
	setAzureCDNEnv(t)
	t.Setenv("BUCKET_KEY_PREFIX", "prefix")
	c := &AzureBlobDirectCDN{}
	got, err := c.ComputeRedirectionURLForBlob("app-1", "LPJNul-wow4m6DsqxbninhsWHlwfp0JecwQzYpOLmCQ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantPrefix := "https://devstoreaccount1.blob.core.windows.net/updates/prefix/app-1/cas/LPJNul-wow4m6DsqxbninhsWHlwfp0JecwQzYpOLmCQ?"
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("expected URL to start with %q, got %q", wantPrefix, got)
	}
}

func TestAzureDirectComputeRedirectionURL(t *testing2.T) {
	clearCDNEnv(t)
	setAzureCDNEnv(t)
	t.Setenv("BUCKET_KEY_PREFIX", "prefix")
	c := &AzureBlobDirectCDN{}
	got, err := c.ComputeRedirectionURLForAsset("app-1", "production", "1", "1674170951", "bundles/android.js")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantPrefix := "https://devstoreaccount1.blob.core.windows.net/updates/prefix/app-1/production/1/1674170951/bundles/android.js?"
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("expected URL to start with %q, got %q", wantPrefix, got)
	}
	for _, param := range []string{"sig=", "se=", "sp=r"} {
		if !strings.Contains(got, param) {
			t.Fatalf("expected URL to contain %q, got %q", param, got)
		}
	}
}

func TestGetCDNReturnsGCSDirectWhenGCSConfigured(t *testing2.T) {
	clearCDNEnv(t)
	t.Setenv("STORAGE_MODE", "gcs")
	t.Setenv("GCS_BUCKET_NAME", "test-bucket")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS_B64", "e3ZhbHVlOiAxfQ==")
	ResetCDNInstance()
	c := GetCDN()
	if c == nil {
		t.Fatalf("expected CDN instance, got nil")
	}
	if _, ok := c.(*GCSDirectCDN); !ok {
		t.Fatalf("expected *GCSDirectCDN, got %T", c)
	}
}

func TestGetCDNReturnsGenericWithLegacyS3CDNPrefix(t *testing2.T) {
	clearCDNEnv(t)
	t.Setenv("STORAGE_MODE", "s3")
	t.Setenv("S3_BUCKET_NAME", "test-bucket")
	t.Setenv("S3_CDN_PREFIX", "https://cdn.example.com")
	ResetCDNInstance()
	c := GetCDN()
	if c == nil {
		t.Fatalf("expected GenericCDN instance, got nil")
	}
	if _, ok := c.(*GenericCDN); !ok {
		t.Fatalf("expected *GenericCDN, got %T", c)
	}
}

func TestGetCDNReturnsGenericWithCDNBaseURLOnGCS(t *testing2.T) {
	clearCDNEnv(t)
	t.Setenv("STORAGE_MODE", "gcs")
	t.Setenv("GCS_BUCKET_NAME", "test-bucket")
	// gcs-direct is also available here; the explicit base URL must win.
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS_B64", "e3ZhbHVlOiAxfQ==")
	t.Setenv("CDN_BASE_URL", "https://cdn.example.com")
	ResetCDNInstance()
	c := GetCDN()
	if c == nil {
		t.Fatalf("expected CDN instance, got nil")
	}
	if _, ok := c.(*GenericCDN); !ok {
		t.Fatalf("expected *GenericCDN, got %T", c)
	}
}

func TestGenericCDNUnavailableWithLocalStorage(t *testing2.T) {
	clearCDNEnv(t)
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("CDN_BASE_URL", "https://cdn.example.com")
	ResetCDNInstance()
	if c := GetCDN(); c != nil {
		t.Fatalf("expected no CDN with local storage, got %T", c)
	}
}

func TestGetCDNPrefersCloudfrontOverGeneric(t *testing2.T) {
	clearCDNEnv(t)
	t.Setenv("STORAGE_MODE", "s3")
	t.Setenv("S3_BUCKET_NAME", "test-bucket")
	t.Setenv("CDN_BASE_URL", "https://cdn.example.com")
	t.Setenv("CLOUDFRONT_DOMAIN", "https://cloudfront.example.com")
	t.Setenv("CLOUDFRONT_KEY_PAIR_ID", "test")
	t.Setenv("PRIVATE_CLOUDFRONT_KEY_PATH", "../../test/keys/private-key-cloudfront-test.pem")
	ResetCDNInstance()
	c := GetCDN()
	if _, ok := c.(*CloudfrontCDN); !ok {
		t.Fatalf("expected *CloudfrontCDN, got %T", c)
	}
}

func TestGenericCDNComputeRedirectionURL(t *testing2.T) {
	cases := []struct {
		name      string
		baseURL   string
		legacyURL string
		keyPrefix string
		expected  string
	}{
		{
			name:     "multi segment asset",
			baseURL:  "https://cdn.example.com",
			expected: "https://cdn.example.com/test-app-id/production/1/1674170951/bundles/android-abc.js",
		},
		{
			name:      "bucket key prefix included once",
			baseURL:   "https://cdn.example.com",
			keyPrefix: "prefix",
			expected:  "https://cdn.example.com/prefix/test-app-id/production/1/1674170951/bundles/android-abc.js",
		},
		{
			name:     "base url with path and trailing slash",
			baseURL:  "https://cdn.example.com/my-bucket/",
			expected: "https://cdn.example.com/my-bucket/test-app-id/production/1/1674170951/bundles/android-abc.js",
		},
		{
			name:      "legacy variable drives the url",
			legacyURL: "https://legacy.example.com",
			expected:  "https://legacy.example.com/test-app-id/production/1/1674170951/bundles/android-abc.js",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing2.T) {
			clearCDNEnv(t)
			t.Setenv("CDN_BASE_URL", tc.baseURL)
			t.Setenv("S3_CDN_PREFIX", tc.legacyURL)
			t.Setenv("BUCKET_KEY_PREFIX", tc.keyPrefix)
			c := &GenericCDN{}
			got, err := c.ComputeRedirectionURLForAsset("test-app-id", "production", "1", "1674170951", "bundles/android-abc.js")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.expected {
				t.Fatalf("expected %q, got %q", tc.expected, got)
			}
		})
	}
}

func TestGenericCDNComputeRedirectionURLForBlob(t *testing2.T) {
	clearCDNEnv(t)
	t.Setenv("CDN_BASE_URL", "https://cdn.example.com")
	t.Setenv("BUCKET_KEY_PREFIX", "prefix")
	c := &GenericCDN{}
	got, err := c.ComputeRedirectionURLForBlob("test-app-id", "LPJNul-wow4m6DsqxbninhsWHlwfp0JecwQzYpOLmCQ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "https://cdn.example.com/prefix/test-app-id/cas/LPJNul-wow4m6DsqxbninhsWHlwfp0JecwQzYpOLmCQ"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestGenericCDNComputeRedirectionURLForPatch(t *testing2.T) {
	clearCDNEnv(t)
	t.Setenv("CDN_BASE_URL", "https://cdn.example.com")
	t.Setenv("BUCKET_KEY_PREFIX", "prefix")
	c := &GenericCDN{}
	got, err := c.ComputeRedirectionURLForPatch("test-app-id", "main", "6f2b1c4e-1b3a-4b4e-9c1d-0a1b2c3d4e5f", "0b9a8c7d-6e5f-4a3b-8c2d-1e0f9a8b7c6d")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "https://cdn.example.com/prefix/test-app-id/bsdiff/main/6f2b1c4e-1b3a-4b4e-9c1d-0a1b2c3d4e5f/0b9a8c7d-6e5f-4a3b-8c2d-1e0f9a8b7c6d"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestDirectCDNModesRefusePatchRedirect(t *testing2.T) {
	for _, c := range []CDN{&S3DirectCDN{}, &GCSDirectCDN{}, &AzureBlobDirectCDN{}} {
		if _, err := c.ComputeRedirectionURLForPatch("a", "b", "c", "d"); err != ErrPatchRedirectUnsupported {
			t.Fatalf("%T: expected ErrPatchRedirectUnsupported, got %v", c, err)
		}
	}
}

func TestResolveCDNBaseURLPrefersNewVariable(t *testing2.T) {
	clearCDNEnv(t)
	t.Setenv("CDN_BASE_URL", "https://new.example.com")
	t.Setenv("S3_CDN_PREFIX", "https://legacy.example.com")
	if got := ResolveCDNBaseURL(); got != "https://new.example.com" {
		t.Fatalf("expected CDN_BASE_URL to win, got %q", got)
	}
	t.Setenv("CDN_BASE_URL", "")
	if got := ResolveCDNBaseURL(); got != "https://legacy.example.com" {
		t.Fatalf("expected S3_CDN_PREFIX fallback, got %q", got)
	}
}
