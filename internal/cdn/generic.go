package cdn

import (
	"log"
	"net/url"
	"strings"
	"sync"
	"xprem/config"
	"xprem/internal/bucket"
)

var s3CDNPrefixDeprecationOnce sync.Once

// ResolveCDNBaseURL returns the base URL of the CDN fronting the storage
// bucket. It reads CDN_BASE_URL first and falls back to the legacy
// S3_CDN_PREFIX env var, which predates non-S3 storage backends.
func ResolveCDNBaseURL() string {
	baseURL := config.GetEnv("CDN_BASE_URL")
	if baseURL == "" {
		// TODO: remove S3_CDN_PREFIX backward-compat once users migrated to CDN_BASE_URL
		baseURL = config.GetEnv("S3_CDN_PREFIX")
		if baseURL != "" {
			s3CDNPrefixDeprecationOnce.Do(func() {
				log.Println("WARNING: S3_CDN_PREFIX is deprecated and will be removed in a future release; use CDN_BASE_URL instead")
			})
		}
	}
	return baseURL
}

type GenericCDN struct{}

// A generic CDN serves the bucket's objects from a base URL, so it needs a
// storage backend whose objects are reachable over HTTP behind that URL.
// Cloud backends qualify; local storage has no object URLs a CDN could use
// as origin.
func (c *GenericCDN) isCDNAvailable() bool {
	if ResolveCDNBaseURL() == "" {
		return false
	}
	switch config.GetEnv("STORAGE_MODE") {
	case "s3":
		return config.GetEnv("S3_BUCKET_NAME") != ""
	case "gcs":
		return config.GetEnv("GCS_BUCKET_NAME") != ""
	case "azure":
		return config.GetEnv("AZURE_BLOB_CONTAINER_NAME") != ""
	default:
		return false
	}
}

func (c *GenericCDN) ComputeRedirectionURLForAsset(appId, branch, runtimeVersion, updateId, asset string) (string, error) {
	baseURL := ResolveCDNBaseURL()
	keyPrefix := strings.TrimSuffix(bucket.ResolveKeyPrefix(), "/")
	elems := []string{appId, branch, runtimeVersion, updateId, asset}
	if keyPrefix != "" {
		elems = append([]string{keyPrefix}, elems...)
	}
	cdnUrl, err := url.JoinPath(baseURL, elems...)
	if err != nil {
		return "", err
	}
	return cdnUrl, nil
}
