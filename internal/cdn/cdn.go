package cdn

import "sync"

type CDN interface {
	isCDNAvailable() bool
	// ComputeRedirectionURLForAsset mirrors the v2 bucket layout:
	// {keyPrefix}{appId}/{branch}/{runtimeVersion}/{updateId}/{asset}.
	// Missing the appId segment produces a 404 against S3/CloudFront and
	// GCS-direct (objects are not at the top level anymore).
	ComputeRedirectionURLForAsset(appId, branch, runtimeVersion, updateId, asset string) (string, error)
}

var (
	cdnInstance CDN
	once        sync.Once
)

func GetCDN() CDN {
	once.Do(func() {
		cloudfrontCDN := CloudfrontCDN{}
		if (&cloudfrontCDN).isCDNAvailable() {
			cdnInstance = &cloudfrontCDN
			return
		}
		// An explicitly configured CDN base URL wins over the implicit
		// direct-to-storage modes: gcs-direct is available on any GCS
		// deployment with signing credentials, so it would otherwise
		// always shadow a deliberate CDN_BASE_URL configuration.
		genericCDN := GenericCDN{}
		if (&genericCDN).isCDNAvailable() {
			cdnInstance = &genericCDN
			return
		}
		gcsCDN := GCSDirectCDN{}
		if (&gcsCDN).isCDNAvailable() {
			cdnInstance = &gcsCDN
			return
		}
		s3CDN := S3DirectCDN{}
		if (&s3CDN).isCDNAvailable() {
			cdnInstance = &s3CDN
			return
		}
		azureCDN := AzureBlobDirectCDN{}
		if (&azureCDN).isCDNAvailable() {
			cdnInstance = &azureCDN
		}
	})
	return cdnInstance
}

func ResetCDNInstance() {
	cdnInstance = nil
	once = sync.Once{}
}

// ResolvedType names the CDN the server actually resolved at boot:
// "cloudfront", "gcs-direct", "s3-direct", "azure-direct", "generic", or ""
// when assets are served directly.
func ResolvedType() string {
	switch GetCDN().(type) {
	case *CloudfrontCDN:
		return "cloudfront"
	case *GCSDirectCDN:
		return "gcs-direct"
	case *S3DirectCDN:
		return "s3-direct"
	case *AzureBlobDirectCDN:
		return "azure-direct"
	case *GenericCDN:
		return "generic"
	default:
		return ""
	}
}
