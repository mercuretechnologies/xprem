package bucket

import (
	"context"
	"io"
	"xprem/internal/types"
)

const bsDiffDir = "bsdiff"

// BSDiffBranchPrefix is {appId}/bsdiff/{branch}/, under which every patch of
// the branch lives. Update ids are only unique within a branch.
func BSDiffBranchPrefix(appId, branch string) string {
	return appId + "/" + bsDiffDir + "/" + branch + "/"
}

// BSDiffObjectKey is {appId}/bsdiff/{branch}/{targetUpdateUUID}/{sourceUpdateUUID}:
// the patch that turns the source update's bundle into the target's. The
// source UUID is the last segment so a CDN edge can echo it as the
// expo-base-update-id header.
func BSDiffObjectKey(appId, branch, targetUpdateUUID, sourceUpdateUUID string) string {
	return BSDiffBranchPrefix(appId, branch) + targetUpdateUUID + "/" + sourceUpdateUUID
}

type BSDiffStorage interface {
	BSDiffExists(ctx context.Context, appId, branch, targetUpdateUUID, sourceUpdateUUID string) (bool, error)
	GetBSDiff(ctx context.Context, appId, branch, targetUpdateUUID, sourceUpdateUUID string) (*types.BucketFile, error)
	PutBSDiff(ctx context.Context, appId, branch, targetUpdateUUID, sourceUpdateUUID string, body io.Reader) error
	DeleteBSDiffs(ctx context.Context, appId, branch string) error
}
