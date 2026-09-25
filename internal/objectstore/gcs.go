package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"time"
	"xprem/internal/providers/gcp"
	"xprem/internal/types"

	"cloud.google.com/go/storage"
	"golang.org/x/sync/errgroup"
	"google.golang.org/api/iterator"
)

type gcsStore struct {
	bucket string
}

func (s *gcsStore) handle() (*storage.BucketHandle, error) {
	if s.bucket == "" {
		return nil, errors.New("gcs store: bucket name not set")
	}
	client, err := gcp.GetClient()
	if err != nil {
		return nil, err
	}
	return client.Bucket(s.bucket), nil
}

func (s *gcsStore) Exists(ctx context.Context, key string) (bool, error) {
	bucketHandle, err := s.handle()
	if err != nil {
		return false, err
	}
	if _, err := bucketHandle.Object(key).Attrs(ctx); err != nil {
		if errors.Is(err, storage.ErrObjectNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("Attrs error: %w", err)
	}
	return true, nil
}

func (s *gcsStore) Get(ctx context.Context, key string) (*types.BucketFile, error) {
	bucketHandle, err := s.handle()
	if err != nil {
		return nil, err
	}
	obj := bucketHandle.Object(key)
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

func (s *gcsStore) Put(ctx context.Context, key string, body io.Reader) error {
	bucketHandle, err := s.handle()
	if err != nil {
		return err
	}
	w := bucketHandle.Object(key).NewWriter(ctx)
	if _, err := io.Copy(w, body); err != nil {
		_ = w.Close()
		return err
	}
	return w.Close()
}

func (s *gcsStore) Delete(ctx context.Context, key string) error {
	bucketHandle, err := s.handle()
	if err != nil {
		return err
	}
	if err := bucketHandle.Object(key).Delete(ctx); err != nil && !errors.Is(err, storage.ErrObjectNotExist) {
		return fmt.Errorf("delete %s: %w", key, err)
	}
	return nil
}

func (s *gcsStore) DeletePrefix(ctx context.Context, prefix string) error {
	if err := requirePrefix(prefix); err != nil {
		return err
	}
	keys, err := s.List(ctx, prefix)
	if err != nil {
		return err
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.NumCPU())
	for _, key := range keys {
		g.Go(func() error { return s.Delete(gctx, key) })
	}
	return g.Wait()
}

func (s *gcsStore) Copy(ctx context.Context, from, to string) error {
	bucketHandle, err := s.handle()
	if err != nil {
		return err
	}
	if _, err := bucketHandle.Object(to).CopierFrom(bucketHandle.Object(from)).Run(ctx); err != nil {
		return fmt.Errorf("copy %s -> %s: %w", from, to, err)
	}
	return nil
}

func (s *gcsStore) List(ctx context.Context, prefix string) ([]string, error) {
	bucketHandle, err := s.handle()
	if err != nil {
		return nil, err
	}
	var keys []string
	it := bucketHandle.Objects(ctx, &storage.Query{Prefix: prefix})
	for {
		attrs, err := it.Next()
		if err == iterator.Done {
			return keys, nil
		}
		if err != nil {
			return nil, fmt.Errorf("list objects: %w", err)
		}
		if attrs.Name != "" {
			keys = append(keys, attrs.Name)
		}
	}
}

func (s *gcsStore) ListPrefixes(ctx context.Context, prefix string) ([]string, error) {
	bucketHandle, err := s.handle()
	if err != nil {
		return nil, err
	}
	var names []string
	it := bucketHandle.Objects(ctx, &storage.Query{Prefix: prefix, Delimiter: "/"})
	for {
		attrs, err := it.Next()
		if err == iterator.Done {
			return names, nil
		}
		if err != nil {
			return nil, fmt.Errorf("list prefixes: %w", err)
		}
		if attrs.Prefix != "" {
			names = append(names, strings.TrimSuffix(strings.TrimPrefix(attrs.Prefix, prefix), "/"))
		}
	}
}

func (s *gcsStore) PresignPut(_ context.Context, key string) (*UploadRequest, error) {
	if s.bucket == "" {
		return nil, errors.New("gcs store: bucket name not set")
	}
	url, err := gcp.SignedURL(s.bucket, key, "PUT", "", 15*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("error generating signed URL: %w", err)
	}
	return &UploadRequest{URL: url, Method: "PUT"}, nil
}
