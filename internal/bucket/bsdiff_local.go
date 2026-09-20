package bucket

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"xprem/internal/types"
)

func (b *LocalBucket) bsDiffPath(appId, branch, targetUpdateUUID, sourceUpdateUUID string) string {
	return filepath.Join(b.rootPath(), appId, bsDiffDir, branch, targetUpdateUUID, sourceUpdateUUID)
}

func (b *LocalBucket) BSDiffExists(_ context.Context, appId, branch, targetUpdateUUID, sourceUpdateUUID string) (bool, error) {
	return b.fileExists(b.bsDiffPath(appId, branch, targetUpdateUUID, sourceUpdateUUID))
}

func (b *LocalBucket) GetBSDiff(_ context.Context, appId, branch, targetUpdateUUID, sourceUpdateUUID string) (*types.BucketFile, error) {
	return b.openFile(b.bsDiffPath(appId, branch, targetUpdateUUID, sourceUpdateUUID))
}

func (b *LocalBucket) PutBSDiff(_ context.Context, appId, branch, targetUpdateUUID, sourceUpdateUUID string, body io.Reader) error {
	return b.writeFile(b.bsDiffPath(appId, branch, targetUpdateUUID, sourceUpdateUUID), body)
}

func (b *LocalBucket) DeleteBSDiffs(_ context.Context, appId, branch string) error {
	if b.BasePath == "" {
		return errors.New("BasePath not set")
	}
	return os.RemoveAll(filepath.Join(b.rootPath(), appId, bsDiffDir, branch))
}
