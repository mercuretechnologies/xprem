package bucket

import (
	"context"
	"io"
	"strings"
	"xprem/internal/objectstore"
)

const instanceIDKey = ".instanceid"

// Instance holds the deployment id at the root of the bucket.
type InstanceStore struct {
	objectStore objectstore.Store
}

// ID is empty until Persist has run.
func (i *InstanceStore) ID(ctx context.Context) (string, error) {
	file, err := i.objectStore.Get(ctx, instanceIDKey)
	if err != nil || file == nil {
		return "", err
	}
	defer file.Reader.Close()
	content, err := io.ReadAll(file.Reader)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(content)), nil
}

func (i *InstanceStore) Persist(ctx context.Context, id string) error {
	return i.objectStore.Put(ctx, instanceIDKey, strings.NewReader(id+"\n"))
}
