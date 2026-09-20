package bucket

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func (b *LocalBucket) GetInstanceID(_ context.Context) (string, error) {
	if b.BasePath == "" {
		return "", errors.New("BasePath not set")
	}
	content, err := os.ReadFile(filepath.Join(b.rootPath(), ".instanceid"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(content)), nil
}

func (b *LocalBucket) PersistInstanceID(_ context.Context, id string) error {
	if b.BasePath == "" {
		return errors.New("BasePath not set")
	}
	if err := os.MkdirAll(b.rootPath(), 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(b.rootPath(), ".instanceid"), []byte(id+"\n"), 0644)
}
