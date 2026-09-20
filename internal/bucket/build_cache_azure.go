package bucket

import (
	"context"
	"time"
	"xprem/internal/providers/azure"
	"xprem/internal/types"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/sas"
)

func (b *AzureBucket) GetBuildCache(ctx context.Context, ref BuildCacheObject) (*types.BucketFile, error) {
	key := ref.Key()
	return b.getObject(ctx, b.prefixedKey(key))
}
func (b *AzureBucket) DeleteBuildCache(ctx context.Context, ref BuildCacheObject) error {
	key := ref.Key()
	return b.deleteObject(ctx, b.prefixedKey(key))
}
func (b *AzureBucket) RequestBuildCacheUploadURL(_ context.Context, ref BuildCacheObject) (*UploadRequest, error) {
	key := ref.Key()
	url, err := azure.SignBlobSAS(b.ContainerName, b.prefixedKey(key), sas.BlobPermissions{Create: true, Write: true}, buildUploadExpiry)
	if err != nil {
		return nil, err
	}
	return &UploadRequest{URL: url, Method: "PUT", Headers: b.uploadHeaders()}, nil
}
func (b *AzureBucket) RequestBuildCacheDownloadURL(_ context.Context, ref BuildCacheObject, expiry time.Time) (string, error) {
	key := ref.Key()
	return azure.SignBlobDownloadURL(b.ContainerName, b.prefixedKey(key), "attachment", "application/octet-stream", expiry)
}
