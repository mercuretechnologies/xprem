package bucket

import (
	"context"
	"io"
	"xprem/internal/objectstore"
	"xprem/internal/types"
)

// PatchStore is the bsdiff store: {appId}/bsdiff/{branch}/{target}/{source}.
type PatchStore struct {
	objectStore objectstore.Store
}

func (p *PatchStore) key(appId, branch, targetUpdateUUID, sourceUpdateUUID string) (string, error) {
	if err := validateSegment("appId", appId); err != nil {
		return "", err
	}
	if err := validateBranch(branch); err != nil {
		return "", err
	}
	if err := validateUpdateUUID("targetUpdateUUID", targetUpdateUUID); err != nil {
		return "", err
	}
	if err := validateUpdateUUID("sourceUpdateUUID", sourceUpdateUUID); err != nil {
		return "", err
	}
	return BSDiffObjectKey(appId, branch, targetUpdateUUID, sourceUpdateUUID), nil
}

func (p *PatchStore) Exists(ctx context.Context, appId, branch, targetUpdateUUID, sourceUpdateUUID string) (bool, error) {
	key, err := p.key(appId, branch, targetUpdateUUID, sourceUpdateUUID)
	if err != nil {
		return false, err
	}
	return p.objectStore.Exists(ctx, key)
}

func (p *PatchStore) Get(ctx context.Context, appId, branch, targetUpdateUUID, sourceUpdateUUID string) (*types.BucketFile, error) {
	key, err := p.key(appId, branch, targetUpdateUUID, sourceUpdateUUID)
	if err != nil {
		return nil, err
	}
	return p.objectStore.Get(ctx, key)
}

func (p *PatchStore) Put(ctx context.Context, appId, branch, targetUpdateUUID, sourceUpdateUUID string, body io.Reader) error {
	key, err := p.key(appId, branch, targetUpdateUUID, sourceUpdateUUID)
	if err != nil {
		return err
	}
	return p.objectStore.Put(ctx, key, body)
}

func (p *PatchStore) DeleteBranch(ctx context.Context, appId, branch string) error {
	if err := validateSegment("appId", appId); err != nil {
		return err
	}
	if err := validateBranch(branch); err != nil {
		return err
	}
	return p.objectStore.DeletePrefix(ctx, BSDiffBranchPrefix(appId, branch))
}
