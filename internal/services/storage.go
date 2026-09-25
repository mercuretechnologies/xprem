package services

import (
	"context"
	"io"
	"xprem/internal/types"
)

// BlobStore, PatchStore and UpdateStore are the stores of the updates bucket the
// services use, as bucket.Bucket lays them out.
type BlobStore interface {
	Exists(ctx context.Context, appId, hash string) (bool, error)
	Get(ctx context.Context, appId, hash string) (*types.BucketFile, error)
}

type PatchStore interface {
	Exists(ctx context.Context, appId, branch, targetUpdateUUID, sourceUpdateUUID string) (bool, error)
	Get(ctx context.Context, appId, branch, targetUpdateUUID, sourceUpdateUUID string) (*types.BucketFile, error)
	Put(ctx context.Context, appId, branch, targetUpdateUUID, sourceUpdateUUID string, body io.Reader) error
	DeleteBranch(ctx context.Context, appId, branch string) error
}

type UpdateStore interface {
	Delete(ctx context.Context, appId, branch, runtimeVersion, updateId string) error
	CreateFrom(ctx context.Context, previousUpdate *types.Update, newUpdateId string) (*types.Update, error)
}
