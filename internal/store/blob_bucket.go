package store

import (
	"context"
	"xprem/internal/bucket"
)

type BucketBlobStore struct {
	bucket bucket.Bucket
}

func NewBucketBlobStore(b bucket.Bucket) *BucketBlobStore {
	return &BucketBlobStore{bucket: b}
}

func (s *BucketBlobStore) FilterExistingHashes(ctx context.Context, appId string, hashes []string) ([]string, error) {
	var existing []string
	for _, hash := range hashes {
		ok, err := s.bucket.BlobExists(ctx, appId, hash)
		if err != nil {
			return nil, err
		}
		if ok {
			existing = append(existing, hash)
		}
	}
	return existing, nil
}
