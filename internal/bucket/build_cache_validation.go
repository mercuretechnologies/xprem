package bucket

import (
	"context"
	"errors"
	"io"
	"time"
	"xprem/internal/types"
)

func (v *validatingBucket) GetBuildCache(ctx context.Context, ref BuildCacheObject) (*types.BucketFile, error) {
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	return v.Inner.GetBuildCache(ctx, ref)
}

func (v *validatingBucket) DeleteBuildCache(ctx context.Context, ref BuildCacheObject) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	return v.Inner.DeleteBuildCache(ctx, ref)
}

func (v *validatingBucket) RequestBuildCacheUploadURL(ctx context.Context, ref BuildCacheObject) (*UploadRequest, error) {
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	return v.Inner.RequestBuildCacheUploadURL(ctx, ref)
}

func (v *validatingBucket) RequestBuildCacheDownloadURL(ctx context.Context, ref BuildCacheObject, expiry time.Time) (string, error) {
	if err := ref.Validate(); err != nil {
		return "", err
	}
	return v.Inner.RequestBuildCacheDownloadURL(ctx, ref, expiry)
}

func (v *validatingBucket) PutBuildCache(ctx context.Context, ref BuildCacheObject, body io.Reader) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	local, ok := v.Inner.(LocalBuildCacheStorage)
	if !ok {
		return ErrCacheDirectUploadRequired
	}
	return local.PutBuildCache(ctx, ref, body)
}

func (r BuildCacheObject) Validate() error {
	if err := validateUUID("appId", r.AppID); err != nil {
		return err
	}
	if err := validateUUID("identifierId", r.IdentifierID); err != nil {
		return err
	}
	if err := validateUUID("uploadId", r.ID); err != nil {
		return err
	}
	if r.Namespace != types.BuildCacheGradle && r.Namespace != types.BuildCacheCcache {
		return errors.New("invalid cache namespace")
	}
	return nil
}
