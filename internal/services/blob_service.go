package services

import "context"

type BlobRepository interface {
	FilterExistingHashes(ctx context.Context, appId string, hashes []string) ([]string, error)
}

type BlobService struct {
	blobRepo BlobRepository
}

func NewBlobService(blobRepo BlobRepository) *BlobService {
	return &BlobService{blobRepo: blobRepo}
}
