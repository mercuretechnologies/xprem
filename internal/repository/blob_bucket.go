package repository

import (
	"context"
	"xprem/internal/bucket"
)

type BucketBlobRepository struct {
	blobStore *bucket.BlobStore
}

func NewBucketBlobRepository(blobStore *bucket.BlobStore) *BucketBlobRepository {
	return &BucketBlobRepository{blobStore: blobStore}
}

func (s *BucketBlobRepository) FilterExistingHashes(ctx context.Context, appId string, hashes []string) ([]string, error) {
	var existing []string
	for _, hash := range hashes {
		ok, err := s.blobStore.Exists(ctx, appId, hash)
		if err != nil {
			return nil, err
		}
		if ok {
			existing = append(existing, hash)
		}
	}
	return existing, nil
}
