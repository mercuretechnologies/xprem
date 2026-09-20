package bucket

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
	"xprem/internal/providers/azure"
	"xprem/internal/types"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/sas"
)

func (b *AzureBucket) blobKey(appId, hash string) string {
	return prefixedBlobKey(b.KeyPrefix, appId, hash)
}

func (b *AzureBucket) BlobExists(ctx context.Context, appId, hash string) (bool, error) {
	return b.objectExists(ctx, b.blobKey(appId, hash))
}

func (b *AzureBucket) GetBlob(ctx context.Context, appId, hash string) (*types.BucketFile, error) {
	return b.getObject(ctx, b.blobKey(appId, hash))
}

func (b *AzureBucket) PutBlob(ctx context.Context, appId, hash string, body io.Reader) error {
	return b.putObject(ctx, b.blobKey(appId, hash), body)
}

func (b *AzureBucket) RequestBlobUploadURL(_ context.Context, appId, hash, _ string) (*UploadRequest, error) {
	if b.ContainerName == "" {
		return nil, errors.New("ContainerName not set")
	}
	url, err := azure.SignBlobSAS(b.ContainerName, b.blobKey(appId, hash), sas.BlobPermissions{Create: true, Write: true}, 15*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("error generating SAS URL: %w", err)
	}
	return &UploadRequest{URL: url, Method: "PUT", Headers: b.uploadHeaders()}, nil
}
