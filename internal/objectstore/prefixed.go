package objectstore

import (
	"context"
	"io"
	"strings"
	"xprem/internal/types"
)

// WithPrefix returns a view of store where every key lives under prefix.
func WithPrefix(store Store, prefix string) Store {
	if prefix == "" {
		return store
	}
	return &prefixed{inner: store, prefix: prefix}
}

type prefixed struct {
	inner  Store
	prefix string
}

func (p *prefixed) Exists(ctx context.Context, key string) (bool, error) {
	return p.inner.Exists(ctx, p.prefix+key)
}

func (p *prefixed) Get(ctx context.Context, key string) (*types.BucketFile, error) {
	return p.inner.Get(ctx, p.prefix+key)
}

func (p *prefixed) Put(ctx context.Context, key string, body io.Reader) error {
	return p.inner.Put(ctx, p.prefix+key, body)
}

func (p *prefixed) Delete(ctx context.Context, key string) error {
	return p.inner.Delete(ctx, p.prefix+key)
}

func (p *prefixed) DeletePrefix(ctx context.Context, prefix string) error {
	if err := requirePrefix(prefix); err != nil {
		return err
	}
	return p.inner.DeletePrefix(ctx, p.prefix+prefix)
}

func (p *prefixed) Copy(ctx context.Context, from, to string) error {
	return p.inner.Copy(ctx, p.prefix+from, p.prefix+to)
}

func (p *prefixed) List(ctx context.Context, prefix string) ([]string, error) {
	keys, err := p.inner.List(ctx, p.prefix+prefix)
	if err != nil {
		return nil, err
	}
	for i, key := range keys {
		keys[i] = strings.TrimPrefix(key, p.prefix)
	}
	return keys, nil
}

func (p *prefixed) ListPrefixes(ctx context.Context, prefix string) ([]string, error) {
	return p.inner.ListPrefixes(ctx, p.prefix+prefix)
}

func (p *prefixed) PresignPut(ctx context.Context, key string) (*UploadRequest, error) {
	return p.inner.PresignPut(ctx, p.prefix+key)
}
