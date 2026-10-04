package services

import (
	"context"
	"io"
	"time"
	"xprem/internal/bucket"
	"xprem/internal/objectstore"
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

// SourcemapStore holds the source maps of published bundles.
type SourcemapStore interface {
	Exists(ctx context.Context, appId, hash string) (bool, error)
	Put(ctx context.Context, appId, hash string, body io.Reader) error
	PresignPut(ctx context.Context, appId, hash, branch string) (*objectstore.UploadRequest, error)
}

type UpdateStore interface {
	Delete(ctx context.Context, appId, branch, runtimeVersion, updateId string) error
	CreateFrom(ctx context.Context, previousUpdate *types.Update, newUpdateId string) (*types.Update, error)
}

// BuildArtifactStore holds the artifacts of native builds.
type BuildArtifactStore interface {
	Get(ctx context.Context, ref bucket.BuildArtifact, staging bool) (*types.BucketFile, error)
	Put(ctx context.Context, ref bucket.BuildArtifact, staging bool, body io.Reader) error
	Delete(ctx context.Context, ref bucket.BuildArtifact, staging bool) error
	PresignPut(ctx context.Context, appID string, ref bucket.BuildArtifact) (*objectstore.UploadRequest, error)
	PresignGet(ctx context.Context, ref bucket.BuildArtifact, expiresAt time.Time) (string, error)
}
