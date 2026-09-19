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

func (b *AzureBucket) buildArtifactKey(ref BuildArtifact, staging bool) string {
	return b.prefixedKey(ref.Key(staging))
}

func (b *AzureBucket) GetBuildArtifact(ctx context.Context, ref BuildArtifact, staging bool) (*types.BucketFile, error) {
	key := b.buildArtifactKey(ref, staging)
	return b.getObject(ctx, key)
}

func (b *AzureBucket) PutBuildArtifact(ctx context.Context, ref BuildArtifact, staging bool, body io.Reader) error {
	key := b.buildArtifactKey(ref, staging)
	return b.putObject(ctx, key, body)
}

func (b *AzureBucket) DeleteBuildArtifact(ctx context.Context, ref BuildArtifact, staging bool) error {
	key := b.buildArtifactKey(ref, staging)
	return b.deleteObject(ctx, key)
}

func (b *AzureBucket) RequestBuildArtifactUploadURL(_ context.Context, _ string, ref BuildArtifact) (*UploadRequest, error) {
	key := b.buildArtifactKey(ref, true)
	if b.ContainerName == "" {
		return nil, errors.New("ContainerName not set")
	}
	url, err := azure.SignBlobSAS(b.ContainerName, key, sas.BlobPermissions{Create: true, Write: true}, buildUploadExpiry)
	if err != nil {
		return nil, fmt.Errorf("error generating SAS URL: %w", err)
	}
	return &UploadRequest{URL: url, Method: "PUT", Headers: b.uploadHeaders()}, nil
}

func (b *AzureBucket) RequestBuildArtifactDownloadURL(_ context.Context, ref BuildArtifact, expiresAt time.Time) (string, error) {
	key := b.buildArtifactKey(ref, false)
	if b.ContainerName == "" {
		return "", errors.New("ContainerName not set")
	}
	return azure.SignBlobDownloadURL(b.ContainerName, key, ref.downloadDisposition(), ref.downloadContentType(), expiresAt)
}
