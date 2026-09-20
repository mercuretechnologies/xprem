package bucket

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"xprem/internal/types"
)

func (b *LocalBucket) blobPath(appId, hash string) string {
	return filepath.Join(b.rootPath(), appId, casDir, hash)
}

func (b *LocalBucket) BlobExists(_ context.Context, appId, hash string) (bool, error) {
	return b.fileExists(b.blobPath(appId, hash))
}

func (b *LocalBucket) GetBlob(_ context.Context, appId, hash string) (*types.BucketFile, error) {
	return b.openFile(b.blobPath(appId, hash))
}

func (b *LocalBucket) PutBlob(_ context.Context, appId, hash string, body io.Reader) error {
	if b.BasePath == "" {
		return errors.New("BasePath not set")
	}
	return writeFileAtomically(b.blobPath(appId, hash), body, hash)
}

// ErrBlobHashMismatch reports a blob whose bytes do not hash to its name.
var ErrBlobHashMismatch = errors.New("uploaded blob does not match its hash")

func (b *LocalBucket) RequestBlobUploadURL(_ context.Context, appId, hash, branch string) (*UploadRequest, error) {
	if b.BasePath == "" {
		return nil, errors.New("BasePath not set")
	}
	dirPath := filepath.Join(b.rootPath(), appId, casDir)
	if err := os.MkdirAll(dirPath, 0o700); err != nil {
		return nil, err
	}
	return b.localUploadRequest(appId, branch, filepath.Join(dirPath, hash))
}
