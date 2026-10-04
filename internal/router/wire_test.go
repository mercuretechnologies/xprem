package infrastructure

import (
	"testing"
	"xprem/internal/bucket"
	"xprem/internal/cdn"

	"github.com/stretchr/testify/require"
)

func TestBuildsTurnOffWhileTheirArtifactsWouldBePublic(t *testing.T) {
	t.Setenv("STORAGE_MODE", "s3")
	t.Setenv("S3_BUCKET_NAME", "updates")
	t.Setenv("BUCKET_KEY_PREFIX", "")
	t.Setenv("S3_CDN_PREFIX", "")
	t.Setenv("CLOUDFRONT_DOMAIN", "")
	t.Setenv("CLOUDFRONT_KEY_PAIR_ID", "")
	t.Cleanup(cdn.ResetCDNInstance)
	for _, tc := range []struct {
		name, cdnBaseURL, buildsBucket string
		allowed                        bool
	}{
		{"updates bucket behind a public CDN", "https://cdn.example.com", "", false},
		{"own bucket behind a public CDN", "https://cdn.example.com", "builds", true},
		{"updates bucket without a public CDN", "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CDN_BASE_URL", tc.cdnBaseURL)
			t.Setenv("S3_BUCKET_BUILDS_NAME", tc.buildsBucket)
			cdn.ResetCDNInstance()
			artifactStore, err := bucket.OpenBuildArtifactStore()
			require.NoError(t, err)
			require.Equal(t, tc.allowed, buildsAllowed(artifactStore))
		})
	}
}
