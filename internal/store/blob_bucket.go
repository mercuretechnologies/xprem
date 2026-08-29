package store

import (
	"context"
	"xprem/internal/bucket"
)

type BucketBlobStore struct {
	bucket bucket.Bucket
}

func NewBucketBlobStore(bucket bucket.Bucket) *BucketBlobStore {
	return &BucketBlobStore{
		bucket: bucket,
	}
}

func (s* BucketBlobStore) FilterExistingHashes(ctx context.Context, appId string, hashes []string) ([]string, error) {
	return hashes, nil
}