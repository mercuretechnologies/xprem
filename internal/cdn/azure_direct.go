package cdn

import (
	"fmt"
	"time"
	"xprem/config"
	"xprem/internal/bucket"
	"xprem/internal/providers/azure"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/sas"
)

// AzureBlobDirectCDN redirects asset requests to SAS URLs served directly by
// Azure Blob Storage, the Azure counterpart of GCSDirectCDN: the container
// stays private and the storage validates each URL. Azure has no native CDN
// with per-URL signed access, so edge caching requires the generic CDN in
// front of a public container instead.
type AzureBlobDirectCDN struct{}

func (c *AzureBlobDirectCDN) isCDNAvailable() bool {
	return config.GetEnv("STORAGE_MODE") == "azure" &&
		config.GetEnv("AZURE_BLOB_CONTAINER_NAME") != "" &&
		config.GetEnv("AZURE_STORAGE_ACCOUNT_NAME") != "" &&
		config.GetEnv("AZURE_STORAGE_ACCOUNT_KEY") != ""
}

func (c *AzureBlobDirectCDN) ComputeRedirectionURLForAsset(appId, branch, runtimeVersion, updateId, asset string) (string, error) {
	containerName := config.GetEnv("AZURE_BLOB_CONTAINER_NAME")
	// Must match the full blob key that the bucket backend writes:
	// {BUCKET_KEY_PREFIX}{appId}/{branch}/{rv}/{updateId}/{asset}. Dropping
	// the prefix here points the SAS URL at a non-existent blob.
	key := bucket.ResolveKeyPrefix() + fmt.Sprintf("%s/%s/%s/%s/%s", appId, branch, runtimeVersion, updateId, asset)
	return azure.SignBlobSAS(containerName, key, sas.BlobPermissions{Read: true}, 15*time.Minute)
}

func (c *AzureBlobDirectCDN) ComputeRedirectionURLForPatch(string, string, string, string) (string, error) {
	return "", ErrPatchRedirectUnsupported
}

func (c *AzureBlobDirectCDN) ComputeRedirectionURLForBlob(appId, hash string) (string, error) {
	containerName := config.GetEnv("AZURE_BLOB_CONTAINER_NAME")
	key := bucket.ResolveKeyPrefix() + bucket.BlobObjectKey(appId, hash)
	return azure.SignBlobSAS(containerName, key, sas.BlobPermissions{Read: true}, 15*time.Minute)
}
