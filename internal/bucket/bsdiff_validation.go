package bucket

import (
	"context"
	"io"
	"xprem/internal/types"
)

func (v *validatingBucket) BSDiffExists(ctx context.Context, appId, branch, targetUpdateUUID, sourceUpdateUUID string) (bool, error) {
	if err := validateBSDiffKey(appId, branch, targetUpdateUUID, sourceUpdateUUID); err != nil {
		return false, err
	}
	return v.Inner.BSDiffExists(ctx, appId, branch, targetUpdateUUID, sourceUpdateUUID)
}

func (v *validatingBucket) GetBSDiff(ctx context.Context, appId, branch, targetUpdateUUID, sourceUpdateUUID string) (*types.BucketFile, error) {
	if err := validateBSDiffKey(appId, branch, targetUpdateUUID, sourceUpdateUUID); err != nil {
		return nil, err
	}
	return v.Inner.GetBSDiff(ctx, appId, branch, targetUpdateUUID, sourceUpdateUUID)
}

func (v *validatingBucket) PutBSDiff(ctx context.Context, appId, branch, targetUpdateUUID, sourceUpdateUUID string, body io.Reader) error {
	if err := validateBSDiffKey(appId, branch, targetUpdateUUID, sourceUpdateUUID); err != nil {
		return err
	}
	return v.Inner.PutBSDiff(ctx, appId, branch, targetUpdateUUID, sourceUpdateUUID, body)
}

func (v *validatingBucket) DeleteBSDiffs(ctx context.Context, appId, branch string) error {
	if err := validateSegment("appId", appId); err != nil {
		return err
	}
	if err := validateBranch(branch); err != nil {
		return err
	}
	return v.Inner.DeleteBSDiffs(ctx, appId, branch)
}

func validateBSDiffKey(appId, branch, targetUpdateUUID, sourceUpdateUUID string) error {
	if err := validateSegment("appId", appId); err != nil {
		return err
	}
	if err := validateBranch(branch); err != nil {
		return err
	}
	if err := validateUUID("targetUpdateUUID", targetUpdateUUID); err != nil {
		return err
	}
	return validateUUID("sourceUpdateUUID", sourceUpdateUUID)
}
