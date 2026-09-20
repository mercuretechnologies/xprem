package bucket

import (
	"context"
	"time"
	"xprem/config"
	"xprem/internal/providers/gcp"
	"xprem/internal/types"
)

func (b *GCSBucket) GetBuildCache(ctx context.Context, ref BuildCacheObject) (*types.BucketFile, error) {
	key := ref.Key()
	return b.getObject(ctx, b.prefixedKey(key))
}
func (b *GCSBucket) DeleteBuildCache(ctx context.Context, ref BuildCacheObject) error {
	key := ref.Key()
	return b.deleteObject(ctx, b.prefixedKey(key))
}
func (b *GCSBucket) RequestBuildCacheUploadURL(_ context.Context, ref BuildCacheObject) (*UploadRequest, error) {
	key := ref.Key()
	url, err := gcp.SignedURL(b.BucketName, b.prefixedKey(key), "PUT", "", buildUploadExpiry)
	if err != nil {
		return nil, err
	}
	return &UploadRequest{URL: url, Method: "PUT"}, nil
}
func (b *GCSBucket) RequestBuildCacheDownloadURL(_ context.Context, ref BuildCacheObject, expiry time.Time) (string, error) {
	key := ref.Key()
	if config.GetEnv("GOOGLE_APPLICATION_CREDENTIALS_B64") == "" {
		return "", nil
	}
	return gcp.SignedDownloadURL(b.BucketName, b.prefixedKey(key), "attachment", "application/octet-stream", expiry)
}
