package bucket

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
	"xprem/internal/types"
)

func (b *LocalBucket) cachePath(ref BuildCacheObject) string {
	return filepath.Join(b.rootPath(), filepath.FromSlash(ref.Key()))
}

func (b *LocalBucket) GetBuildCache(_ context.Context, ref BuildCacheObject) (*types.BucketFile, error) {
	file := b.cachePath(ref)
	return b.openFile(file)
}

func (b *LocalBucket) PutBuildCache(ctx context.Context, ref BuildCacheObject, body io.Reader) error {
	target := b.cachePath(ref)
	if b.BasePath == "" {
		return os.ErrInvalid
	}
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	// The hard link publishes complete bytes atomically without replacing an
	// existing object. No reader can see a partially written local upload.
	f, err := os.OpenFile(target+".upload", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		if os.IsExist(err) {
			return ErrCacheObjectExists
		}
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := io.Copy(f, body); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Link(f.Name(), target); err != nil {
		if os.IsExist(err) {
			return ErrCacheObjectExists
		}
		return err
	}
	return nil
}

func (b *LocalBucket) DeleteBuildCache(_ context.Context, ref BuildCacheObject) error {
	file := b.cachePath(ref)
	var failures []error
	for _, name := range []string{file, file + ".upload"} {
		if err := os.Remove(name); err != nil && !os.IsNotExist(err) {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
func (b *LocalBucket) RequestBuildCacheUploadURL(context.Context, BuildCacheObject) (*UploadRequest, error) {
	return nil, nil
}
func (b *LocalBucket) RequestBuildCacheDownloadURL(context.Context, BuildCacheObject, time.Time) (string, error) {
	return "", nil
}
