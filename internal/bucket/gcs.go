package bucket

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"time"
	"xprem/internal/providers/gcp"
	"xprem/internal/types"

	"cloud.google.com/go/storage"
	"golang.org/x/sync/errgroup"
	"google.golang.org/api/iterator"
)

type GCSBucket struct {
	BucketName string
	KeyPrefix  string
}

func (b *GCSBucket) prefixedKey(key string) string {
	return b.KeyPrefix + key
}

func (b *GCSBucket) bucketHandle(ctx context.Context) (*storage.BucketHandle, error) {
	if b.BucketName == "" {
		return nil, errors.New("BucketName not set")
	}
	client, err := gcp.GetClient()
	if err != nil {
		return nil, err
	}
	return client.Bucket(b.BucketName), nil
}

// deletePrefix removes every object under prefix.
func (b *GCSBucket) deletePrefix(ctx context.Context, prefix string) error {
	bh, err := b.bucketHandle(ctx)
	if err != nil {
		return err
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.NumCPU())
	it := bh.Objects(gctx, &storage.Query{Prefix: prefix})
	var listErr error
	for {
		attrs, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			listErr = fmt.Errorf("failed to list objects: %w", err)
			break
		}
		if attrs.Name == "" { // prefix entry
			continue
		}
		name := attrs.Name
		g.Go(func() error {
			if err := bh.Object(name).Delete(gctx); err != nil {
				return fmt.Errorf("failed to delete object %s: %w", name, err)
			}
			return nil
		})
	}
	// A worker error cancels gctx, which also aborts the listing above; the
	// worker error is the interesting one, so report it first.
	if err := g.Wait(); err != nil {
		return err
	}
	if listErr != nil {
		return listErr
	}
	return nil
}

func (b *GCSBucket) objectExists(ctx context.Context, key string) (bool, error) {
	bh, err := b.bucketHandle(ctx)
	if err != nil {
		return false, err
	}
	_, err = bh.Object(key).Attrs(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("Attrs error: %w", err)
	}
	return true, nil
}

// getObject returns nil, nil when the key does not exist.
func (b *GCSBucket) getObject(ctx context.Context, key string) (*types.BucketFile, error) {
	bh, err := b.bucketHandle(ctx)
	if err != nil {
		return nil, err
	}
	obj := bh.Object(key)
	r, err := obj.NewReader(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("GetObject error: %w", err)
	}
	attrs, _ := obj.Attrs(ctx)
	var created time.Time
	if attrs != nil {
		created = attrs.Updated
	}
	return &types.BucketFile{Reader: r, CreatedAt: created}, nil
}

func (b *GCSBucket) putObject(ctx context.Context, key string, body io.Reader) error {
	bh, err := b.bucketHandle(ctx)
	if err != nil {
		return err
	}
	w := bh.Object(key).NewWriter(ctx)
	if _, err := io.Copy(w, body); err != nil {
		_ = w.Close()
		return err
	}
	return w.Close()
}

// PutObject implements the audit archive's object write.
func (b *GCSBucket) PutObject(ctx context.Context, key string, body []byte) error {
	bh, err := b.bucketHandle(ctx)
	if err != nil {
		return err
	}
	w := bh.Object(b.prefixedKey(key)).NewWriter(ctx)
	if _, err := w.Write(body); err != nil {
		_ = w.Close()
		return err
	}
	return w.Close()
}

// deleteObject is a no-op when the key does not exist.
func (b *GCSBucket) deleteObject(ctx context.Context, key string) error {
	bh, err := b.bucketHandle(ctx)
	if err != nil {
		return err
	}
	err = bh.Object(key).Delete(ctx)
	if errors.Is(err, storage.ErrObjectNotExist) {
		return nil
	}
	return err
}
