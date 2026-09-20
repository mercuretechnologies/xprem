package bucket

import (
	"context"
	"io"
	"time"
	"xprem/internal/types"
)

func (v *validatingBucket) GetBuildArtifact(ctx context.Context, ref BuildArtifact, staging bool) (*types.BucketFile, error) {
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	return v.Inner.GetBuildArtifact(ctx, ref, staging)
}

func (v *validatingBucket) PutBuildArtifact(ctx context.Context, ref BuildArtifact, staging bool, body io.Reader) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	return v.Inner.PutBuildArtifact(ctx, ref, staging, body)
}

func (v *validatingBucket) DeleteBuildArtifact(ctx context.Context, ref BuildArtifact, staging bool) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	return v.Inner.DeleteBuildArtifact(ctx, ref, staging)
}

func (v *validatingBucket) RequestBuildArtifactUploadURL(ctx context.Context, appID string, ref BuildArtifact) (*UploadRequest, error) {
	if err := validateSegment("appId", appID); err != nil {
		return nil, err
	}
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	return v.Inner.RequestBuildArtifactUploadURL(ctx, appID, ref)
}

func (v *validatingBucket) RequestBuildArtifactDownloadURL(ctx context.Context, ref BuildArtifact, expiresAt time.Time) (string, error) {
	if err := ref.Validate(); err != nil {
		return "", err
	}
	if !expiresAt.After(time.Now()) {
		return "", ErrBuildDownloadExpired
	}
	return v.Inner.RequestBuildArtifactDownloadURL(ctx, ref, expiresAt)
}

func (r BuildArtifact) Validate() error {
	if _, err := r.Type.Platform(); err != nil {
		return err
	}
	if err := validateUUID("identifierId", r.IdentifierID); err != nil {
		return err
	}
	return validateUUID("buildId", r.BuildID)
}
