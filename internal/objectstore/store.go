// Package objectstore is the raw object layer every storage feature builds on:
// a key and its bytes, with no notion of apps, branches or updates.
package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"xprem/config"
	"xprem/internal/types"
)

// Store is one bucket, container or directory. Keys are slash-separated and
// relative; a prefix argument names everything under it and ends with "/".
type Store interface {
	Exists(ctx context.Context, key string) (bool, error)
	// Get returns nil, nil when the key does not exist.
	Get(ctx context.Context, key string) (*types.BucketFile, error)
	Put(ctx context.Context, key string, body io.Reader) error
	Delete(ctx context.Context, key string) error
	DeletePrefix(ctx context.Context, prefix string) error
	Copy(ctx context.Context, from, to string) error
	// List returns every key under prefix.
	List(ctx context.Context, prefix string) ([]string, error)
	// ListPrefixes returns the bare names of the immediate children of prefix
	// that hold further keys.
	ListPrefixes(ctx context.Context, prefix string) ([]string, error)
	// PresignPut returns a request an uploader can PUT the object with.
	PresignPut(ctx context.Context, key string) (*UploadRequest, error)
}

// UploadRequest describes a PUT, including any per-file authorization headers.
type UploadRequest struct {
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers,omitempty"`
}

// requirePrefix refuses the empty prefix, which names every object of the store.
func requirePrefix(prefix string) error {
	if prefix == "" || prefix == "/" {
		return errors.New("refusing to delete every object of the store")
	}
	return nil
}

// Mode is the STORAGE_MODE value selecting the driver.
type Mode string

const (
	ModeS3    Mode = "s3"
	ModeGCS   Mode = "gcs"
	ModeAzure Mode = "azure"
	ModeLocal Mode = "local"
)

func ResolveMode() Mode {
	switch Mode(config.GetEnv("STORAGE_MODE")) {
	case ModeS3:
		return ModeS3
	case ModeGCS:
		return ModeGCS
	case ModeAzure:
		return ModeAzure
	default:
		return ModeLocal
	}
}

// Open returns the store at location: a bucket name, a container name or a
// directory, depending on the mode.
func Open(mode Mode, location string) Store {
	switch mode {
	case ModeS3:
		return &s3Store{bucket: location}
	case ModeGCS:
		return &gcsStore{bucket: location}
	case ModeAzure:
		return &azureStore{container: location}
	case ModeLocal:
		return &localStore{root: location}
	default:
		panic(fmt.Sprintf("objectstore: unknown mode %q", mode))
	}
}

// ValidateKey rejects keys that could escape a store's root on a filesystem.
func ValidateKey(key string) error {
	if key == "" {
		return fmt.Errorf("must not be empty")
	}
	if strings.ContainsRune(key, '\\') {
		return fmt.Errorf("must not contain '\\' characters")
	}
	if strings.HasPrefix(key, "/") {
		return fmt.Errorf("must not be absolute")
	}
	for _, segment := range strings.Split(key, "/") {
		if segment == ".." {
			return fmt.Errorf("must not contain '..' segments")
		}
	}
	return nil
}

var updatesLocationEnv = map[Mode]string{
	ModeS3:    "S3_BUCKET_NAME",
	ModeGCS:   "GCS_BUCKET_NAME",
	ModeAzure: "AZURE_BLOB_CONTAINER_NAME",
	ModeLocal: "LOCAL_BUCKET_BASE_PATH",
}

var locationKind = map[Mode]string{
	ModeS3:    "bucket",
	ModeGCS:   "bucket",
	ModeAzure: "container",
	ModeLocal: "directory",
}

// UpdatesLocation is the configured location of the updates store.
func UpdatesLocation(mode Mode) string {
	return config.GetEnv(updatesLocationEnv[mode])
}

// OpenDedicated opens a store a feature keeps apart from the updates store,
// reading its location from the env var registered for the active mode. On
// disk the store is private to the server's user.
func OpenDedicated(locationEnv map[Mode]string) (Store, error) {
	mode := ResolveMode()
	envVar := locationEnv[mode]
	location := config.GetEnv(envVar)
	if location == "" {
		return nil, fmt.Errorf("%s is not set", envVar)
	}
	if sameLocation(mode, location, UpdatesLocation(mode)) {
		kind := locationKind[mode]
		return nil, fmt.Errorf("%s must be a dedicated %s, not the updates %s (%s)", envVar, kind, kind, updatesLocationEnv[mode])
	}
	if mode == ModeLocal {
		return &localStore{root: location, private: true}, nil
	}
	return Open(mode, location), nil
}

func sameLocation(mode Mode, a, b string) bool {
	if mode != ModeLocal {
		return a == b
	}
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return a == b
	}
	return absA == absB
}
