package bucket

import (
	"context"
	"errors"
	"io"
	"time"
	"xprem/internal/objectstore"
	"xprem/internal/types"
)

var ErrCacheObjectExists = errors.New("cache object already exists")
var ErrCacheDirectUploadRequired = errors.New("cache uploads require the signed bucket URL")

type BuildCacheObject struct {
	AppID        string
	IdentifierID string
	Namespace    types.BuildCacheNamespace
	ID           string
}

func (r BuildCacheObject) Validate() error {
	if err := validateUUID("appId", r.AppID); err != nil {
		return err
	}
	if err := validateUUID("identifierId", r.IdentifierID); err != nil {
		return err
	}
	if err := validateUUID("uploadId", r.ID); err != nil {
		return err
	}
	if r.Namespace != types.BuildCacheGradle && r.Namespace != types.BuildCacheCcache {
		return errors.New("invalid cache namespace")
	}
	return nil
}

// Key is builds/cache/{appId}/{identifierId}/{namespace}/{id}.
func (r BuildCacheObject) Key() string {
	return BuildsPrefix + "/cache/" + r.AppID + "/" + r.IdentifierID + "/" + string(r.Namespace) + "/" + r.ID
}

// BuildCacheStore holds the cache archives of native builds, in the location
// of their artifacts.
type BuildCacheStore struct {
	objectStore  objectstore.Store
	localUploads bool
}

// CacheStore is the cache archive store sharing the artifacts' location.
func (s *BuildArtifactStore) CacheStore() *BuildCacheStore {
	return &BuildCacheStore{objectStore: s.objectStore, localUploads: s.localUploads}
}

func (s *BuildCacheStore) key(ref BuildCacheObject) (string, error) {
	if err := ref.Validate(); err != nil {
		return "", err
	}
	return ref.Key(), nil
}

// Get returns nil, nil when the archive is absent.
func (s *BuildCacheStore) Get(ctx context.Context, ref BuildCacheObject) (*types.BucketFile, error) {
	key, err := s.key(ref)
	if err != nil {
		return nil, err
	}
	return s.objectStore.Get(ctx, key)
}

// Put receives local uploads only; cloud uploads go straight to a signed URL.
func (s *BuildCacheStore) Put(ctx context.Context, ref BuildCacheObject, body io.Reader) error {
	key, err := s.key(ref)
	if err != nil {
		return err
	}
	if !s.localUploads {
		return ErrCacheDirectUploadRequired
	}
	return s.objectStore.Put(ctx, key, body)
}

// Delete succeeds when the archive is already absent.
func (s *BuildCacheStore) Delete(ctx context.Context, ref BuildCacheObject) error {
	key, err := s.key(ref)
	if err != nil {
		return err
	}
	return s.objectStore.Delete(ctx, key)
}

// PresignPut returns nil, nil on disk: the CLI then uploads through the
// server's authenticated cache route.
func (s *BuildCacheStore) PresignPut(ctx context.Context, ref BuildCacheObject) (*objectstore.UploadRequest, error) {
	key, err := s.key(ref)
	if err != nil {
		return nil, err
	}
	if s.localUploads {
		return nil, nil
	}
	return s.objectStore.PresignPut(ctx, key)
}

// PresignGet returns "" when the server must stream the archive itself.
func (s *BuildCacheStore) PresignGet(ctx context.Context, ref BuildCacheObject, expiresAt time.Time) (string, error) {
	key, err := s.key(ref)
	if err != nil {
		return "", err
	}
	return s.objectStore.PresignGet(ctx, key, objectstore.Download{
		ExpiresAt:          expiresAt,
		ContentDisposition: "attachment",
		ContentType:        "application/octet-stream",
	})
}
