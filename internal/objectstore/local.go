package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"xprem/internal/types"
)

type localStore struct {
	root string
	// private keeps the directories 0700 and the files 0600, for destinations
	// holding personal data. Otherwise files are 0644, readable by a web server.
	private bool
}

func (s *localStore) path(key string) (string, error) {
	if s.root == "" {
		return "", errors.New("local store: directory not set")
	}
	if key == "" {
		return s.root, nil
	}
	if err := ValidateKey(key); err != nil {
		return "", fmt.Errorf("invalid object key: %w", err)
	}
	return filepath.Join(s.root, filepath.FromSlash(key)), nil
}

func (s *localStore) Exists(_ context.Context, key string) (bool, error) {
	path, err := s.path(key)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *localStore) Get(_ context.Context, key string) (*types.BucketFile, error) {
	path, err := s.path(key)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	return &types.BucketFile{Reader: file, CreatedAt: info.ModTime()}, nil
}

// Put streams body into a temporary file next to the target and renames it
// into place, so an interrupted write leaves nothing at the key.
func (s *localStore) Put(_ context.Context, key string, body io.Reader) error {
	target, err := s.path(key)
	if err != nil {
		return err
	}
	dirMode := os.ModePerm
	if s.private {
		dirMode = 0o700
	}
	if err := os.MkdirAll(filepath.Dir(target), dirMode); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".upload-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if !s.private {
		if err := tmp.Chmod(0o644); err != nil {
			tmp.Close()
			return err
		}
	}
	_, copyErr := io.Copy(tmp, body)
	syncErr := tmp.Sync()
	closeErr := tmp.Close()
	if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), target)
}

// Delete also removes the directories the key leaves empty, since a
// directory is not an object and must not be listed as a prefix.
func (s *localStore) Delete(_ context.Context, key string) error {
	path, err := s.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	root := filepath.Clean(s.root)
	for dir := filepath.Dir(path); strings.HasPrefix(dir, root+string(os.PathSeparator)); dir = filepath.Dir(dir) {
		if err := os.Remove(dir); err != nil {
			return nil
		}
	}
	return nil
}

func (s *localStore) DeletePrefix(_ context.Context, prefix string) error {
	if err := requirePrefix(prefix); err != nil {
		return err
	}
	path, err := s.path(prefix)
	if err != nil {
		return err
	}
	return os.RemoveAll(path)
}

func (s *localStore) Copy(ctx context.Context, from, to string) error {
	source, err := s.path(from)
	if err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	return s.Put(ctx, to, in)
}

func (s *localStore) List(_ context.Context, prefix string) ([]string, error) {
	dir, err := s.path(prefix)
	if err != nil {
		return nil, err
	}
	var keys []string
	err = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && path == dir {
				return nil
			}
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(s.root, path)
		if err != nil {
			return err
		}
		keys = append(keys, filepath.ToSlash(rel))
		return nil
	})
	return keys, err
}

func (s *localStore) ListPrefixes(_ context.Context, prefix string) ([]string, error) {
	dir, err := s.path(prefix)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}

// PresignPut has no answer on a filesystem: local uploads go through the
// server's own upload route.
func (s *localStore) PresignPut(context.Context, string) (*UploadRequest, error) {
	return nil, errors.New("local store: uploads go through the server's upload route")
}
