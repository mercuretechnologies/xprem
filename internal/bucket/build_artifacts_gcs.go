package bucket

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
	"xprem/config"
	"xprem/internal/providers/gcp"
	"xprem/internal/types"
)

func (b *GCSBucket) buildArtifactKey(ref BuildArtifact, staging bool) string {
	return b.prefixedKey(ref.Key(staging))
}

func (b *GCSBucket) GetBuildArtifact(ctx context.Context, ref BuildArtifact, staging bool) (*types.BucketFile, error) {
	key := b.buildArtifactKey(ref, staging)
	return b.getObject(ctx, key)
}

func (b *GCSBucket) PutBuildArtifact(ctx context.Context, ref BuildArtifact, staging bool, body io.Reader) error {
	key := b.buildArtifactKey(ref, staging)
	return b.putObject(ctx, key, body)
}

func (b *GCSBucket) DeleteBuildArtifact(ctx context.Context, ref BuildArtifact, staging bool) error {
	key := b.buildArtifactKey(ref, staging)
	return b.deleteObject(ctx, key)
}

func (b *GCSBucket) RequestBuildArtifactUploadURL(_ context.Context, _ string, ref BuildArtifact) (*UploadRequest, error) {
	key := b.buildArtifactKey(ref, true)
	if b.BucketName == "" {
		return nil, errors.New("BucketName not set")
	}
	url, err := gcp.SignedURL(b.BucketName, key, "PUT", "", buildUploadExpiry)
	if err != nil {
		return nil, fmt.Errorf("error generating signed URL: %w", err)
	}
	return &UploadRequest{URL: url, Method: "PUT"}, nil
}

func (b *GCSBucket) RequestBuildArtifactDownloadURL(_ context.Context, ref BuildArtifact, expiresAt time.Time) (string, error) {
	if config.GetEnv("GOOGLE_APPLICATION_CREDENTIALS_B64") == "" {
		return "", nil
	}
	key := b.buildArtifactKey(ref, false)
	if b.BucketName == "" {
		return "", errors.New("BucketName not set")
	}
	return gcp.SignedDownloadURL(b.BucketName, key, ref.downloadDisposition(), ref.downloadContentType(), expiresAt)
}
