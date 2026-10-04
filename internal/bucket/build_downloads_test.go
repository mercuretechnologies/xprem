package bucket

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/url"
	"strconv"
	"testing"
	"time"
	"xprem/internal/objectstore"

	"github.com/stretchr/testify/require"
)

func TestBuildArtifactDownloadURLs(t *testing.T) {
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "build-test-access")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "build-test-secret")
	t.Setenv("AWS_BASE_ENDPOINT", "")
	t.Setenv("AWS_S3_FORCE_PATH_STYLE", "false")
	t.Setenv("DISABLE_S3_DIRECT_CDN", "false")
	t.Setenv("AZURE_STORAGE_ACCOUNT_NAME", "buildtest")
	t.Setenv("AZURE_STORAGE_ACCOUNT_KEY", base64.StdEncoding.EncodeToString([]byte("build-download-test-key")))
	t.Setenv("AZURE_BLOB_ENDPOINT", "")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	credentials, err := json.Marshal(map[string]string{
		"client_email": "build-test@example.iam.gserviceaccount.com",
		"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})),
	})
	require.NoError(t, err)
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS_B64", base64.StdEncoding.EncodeToString(credentials))

	for _, tc := range []struct {
		name                        string
		mode                        objectstore.Mode
		host, pathPrefix, signature string
	}{
		{"s3", objectstore.ModeS3, "artifacts.s3.us-east-1.amazonaws.com", "/prefix/", "X-Amz-Signature"},
		{"gcs", objectstore.ModeGCS, "storage.googleapis.com", "/artifacts/prefix/", "X-Goog-Signature"},
		{"azure", objectstore.ModeAzure, "buildtest.blob.core.windows.net", "/artifacts/prefix/", "sig"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &BuildArtifactStore{objectStore: objectstore.WithPrefix(objectstore.Open(tc.mode, "artifacts"), "prefix/")}
			deadline := time.Now().Add(30 * time.Second).UTC().Truncate(time.Second)
			signed, err := store.PresignGet(context.Background(), testArtifact(), deadline)
			require.NoError(t, err)
			parsed, err := url.Parse(signed)
			require.NoError(t, err)
			require.Equal(t, "https", parsed.Scheme)
			require.Equal(t, tc.host, parsed.Host)
			require.Equal(t, tc.pathPrefix+"builds/android/"+testIdentifierID+"/"+testBuildID+".apk", parsed.Path)
			query := parsed.Query()
			require.NotEmpty(t, query.Get(tc.signature))
			contentType, disposition := query.Get("response-content-type"), query.Get("response-content-disposition")
			var expiresAt time.Time
			if tc.name == "azure" {
				require.Equal(t, "r", query.Get("sp"))
				contentType, disposition = query.Get("rsct"), query.Get("rscd")
				expiresAt, err = time.Parse(time.RFC3339, query.Get("se"))
				require.NoError(t, err)
			} else {
				prefix := "X-Amz-"
				if tc.name == "gcs" {
					prefix = "X-Goog-"
				}
				require.Equal(t, "host", query.Get(prefix+"SignedHeaders"), "a browser GET must not require custom headers")
				issuedAt, err := time.Parse("20060102T150405Z", query.Get(prefix+"Date"))
				require.NoError(t, err)
				seconds, err := strconv.Atoi(query.Get(prefix + "Expires"))
				require.NoError(t, err)
				require.Positive(t, seconds)
				expiresAt = issuedAt.Add(time.Duration(seconds) * time.Second)
			}
			require.False(t, expiresAt.After(deadline), "the signed URL cannot outlive the share")
			require.WithinDuration(t, deadline, expiresAt, 2*time.Second)
			require.Equal(t, "application/vnd.android.package-archive", contentType)
			require.Equal(t, `attachment; filename="`+testBuildID+`.apk"`, disposition)
			require.Empty(t, query.Get("token"), "the share token is never sent to the bucket")
		})
	}
}

func TestBuildArtifactDownloadFallbacks(t *testing.T) {
	t.Setenv("DISABLE_S3_DIRECT_CDN", "true")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS_B64", "")
	for _, mode := range []objectstore.Mode{objectstore.ModeLocal, objectstore.ModeS3, objectstore.ModeGCS} {
		store := &BuildArtifactStore{objectStore: objectstore.Open(mode, t.TempDir())}
		url, err := store.PresignGet(context.Background(), testArtifact(), time.Now().Add(time.Minute))
		require.NoError(t, err)
		require.Empty(t, url, mode)
	}
}

func TestBuildArtifactDownloadRejectsInvalidInputBeforeSigning(t *testing.T) {
	store := &BuildArtifactStore{objectStore: unreachableObjectStore{}}
	_, err := store.PresignGet(context.Background(), BuildArtifact{}, time.Now().Add(time.Minute))
	require.Error(t, err)
	_, err = store.PresignGet(context.Background(), testArtifact(), time.Now().Add(-time.Second))
	require.ErrorIs(t, err, objectstore.ErrDownloadExpired)
}
