package bucket

import (
	"context"
	"io"
	"xprem/internal/types"
)

func (b *S3Bucket) DeleteBSDiffs(ctx context.Context, appId, branch string) error {
	return b.deletePrefix(ctx, b.prefixedKey(BSDiffBranchPrefix(appId, branch)))
}

func (b *S3Bucket) bsDiffKey(appId, branch, targetUpdateUUID, sourceUpdateUUID string) string {
	return b.prefixedKey(BSDiffObjectKey(appId, branch, targetUpdateUUID, sourceUpdateUUID))
}

func (b *S3Bucket) BSDiffExists(ctx context.Context, appId, branch, targetUpdateUUID, sourceUpdateUUID string) (bool, error) {
	return b.objectExists(ctx, b.bsDiffKey(appId, branch, targetUpdateUUID, sourceUpdateUUID))
}

func (b *S3Bucket) GetBSDiff(ctx context.Context, appId, branch, targetUpdateUUID, sourceUpdateUUID string) (*types.BucketFile, error) {
	return b.getObject(ctx, b.bsDiffKey(appId, branch, targetUpdateUUID, sourceUpdateUUID))
}

func (b *S3Bucket) PutBSDiff(ctx context.Context, appId, branch, targetUpdateUUID, sourceUpdateUUID string, body io.Reader) error {
	return b.putObject(ctx, b.bsDiffKey(appId, branch, targetUpdateUUID, sourceUpdateUUID), body)
}
