package bucket

import (
	"context"
	"io"
	"xprem/internal/objectstore"
	"xprem/internal/types"
)

// BlobStore is the content-addressed store: {appId}/cas/{hash}.
type BlobStore struct {
	objectStore  objectstore.Store
	localUploads bool
}

func (b *BlobStore) key(appId, hash string) (string, error) {
	if err := validateSegment("appId", appId); err != nil {
		return "", err
	}
	if err := ValidateBlobHash(hash); err != nil {
		return "", err
	}
	return BlobObjectKey(appId, hash), nil
}

func (b *BlobStore) Exists(ctx context.Context, appId, hash string) (bool, error) {
	key, err := b.key(appId, hash)
	if err != nil {
		return false, err
	}
	return b.objectStore.Exists(ctx, key)
}

func (b *BlobStore) Get(ctx context.Context, appId, hash string) (*types.BucketFile, error) {
	key, err := b.key(appId, hash)
	if err != nil {
		return nil, err
	}
	return b.objectStore.Get(ctx, key)
}

func (b *BlobStore) Put(ctx context.Context, appId, hash string, body io.Reader) error {
	key, err := b.key(appId, hash)
	if err != nil {
		return err
	}
	return b.objectStore.Put(ctx, key, body)
}

// PresignPut takes the branch the blob is published under, which is what
// scoped API keys are judged against on the upload route.
func (b *BlobStore) PresignPut(ctx context.Context, appId, hash, branch string) (*objectstore.UploadRequest, error) {
	key, err := b.key(appId, hash)
	if err != nil {
		return nil, err
	}
	if err := validateBranch(branch); err != nil {
		return nil, err
	}
	return presignUpload(ctx, b.objectStore, b.localUploads, appId, branch, key)
}
