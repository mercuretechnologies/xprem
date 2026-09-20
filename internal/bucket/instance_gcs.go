package bucket

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"

	"cloud.google.com/go/storage"
)

func (b *GCSBucket) GetInstanceID(ctx context.Context) (string, error) {
	bh, err := b.bucketHandle(ctx)
	if err != nil {
		return "", err
	}
	r, err := bh.Object(b.prefixedKey(".instanceid")).NewReader(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotExist) {
			return "", nil
		}
		return "", err
	}
	defer r.Close()
	buf := new(bytes.Buffer)
	if _, err := io.Copy(buf, r); err != nil {
		return "", err
	}
	return strings.TrimSpace(buf.String()), nil
}

func (b *GCSBucket) PersistInstanceID(ctx context.Context, id string) error {
	bh, err := b.bucketHandle(ctx)
	if err != nil {
		return err
	}
	w := bh.Object(b.prefixedKey(".instanceid")).NewWriter(ctx)
	if _, err := w.Write([]byte(id + "\n")); err != nil {
		_ = w.Close()
		return err
	}
	return w.Close()
}
