package bucket

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
	"xprem/internal/providers/gcp"
	"xprem/internal/types"
)

func (b *GCSBucket) blobKey(appId, hash string) string {
	return prefixedBlobKey(b.KeyPrefix, appId, hash)
}

func (b *GCSBucket) BlobExists(ctx context.Context, appId, hash string) (bool, error) {
	return b.objectExists(ctx, b.blobKey(appId, hash))
}

func (b *GCSBucket) GetBlob(ctx context.Context, appId, hash string) (*types.BucketFile, error) {
	return b.getObject(ctx, b.blobKey(appId, hash))
}

func (b *GCSBucket) PutBlob(ctx context.Context, appId, hash string, body io.Reader) error {
	return b.putObject(ctx, b.blobKey(appId, hash), body)
}

func (b *GCSBucket) RequestBlobUploadURL(_ context.Context, appId, hash, _ string) (*UploadRequest, error) {
	if b.BucketName == "" {
		return nil, errors.New("BucketName not set")
	}
	url, err := gcp.SignedURL(b.BucketName, b.blobKey(appId, hash), "PUT", "", 15*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("error generating signed URL: %w", err)
	}
	return &UploadRequest{URL: url, Method: "PUT"}, nil
}
