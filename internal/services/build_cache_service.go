package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"time"
	"xprem/internal/bucket"
	"xprem/internal/objectstore"
	"xprem/internal/repository"
	"xprem/internal/types"
	"xprem/internal/validation"

	"github.com/google/uuid"
)

var ErrBuildCacheIntegrity = errors.New("cache object size or SHA-256 does not match")
var ErrBuildCachePending = errors.New("cache object is not published")

type BuildCacheRepository interface {
	Reserve(context.Context, types.BuildCacheObject) (*types.BuildCacheObject, error)
	Get(context.Context, string, string, string) (*types.BuildCacheObject, error)
	Find(context.Context, string, string, types.BuildCacheNamespace, string) (*types.BuildCacheObject, error)
	Publish(context.Context, types.BuildCacheObject) (*types.BuildCacheObject, error)
	Delete(context.Context, types.BuildCacheObject) error
}

type BuildCacheService struct {
	repo       BuildCacheRepository
	cacheStore BuildCacheStore
}

// NewBuildCacheService takes a nil cacheStore when builds are turned off.
func NewBuildCacheService(repo BuildCacheRepository, cacheStore BuildCacheStore) *BuildCacheService {
	return &BuildCacheService{repo: repo, cacheStore: cacheStore}
}

type BuildCacheInput struct {
	Namespace types.BuildCacheNamespace `json:"namespace"`
	Key       string                    `json:"key"`
	Size      int64                     `json:"size"`
	SHA256    string                    `json:"sha256"`
}

type BuildCacheUpload struct {
	Object *types.BuildCacheObject    `json:"object"`
	Upload *objectstore.UploadRequest `json:"upload,omitempty"`
	Cached bool                       `json:"cached"`
}

func cacheRef(object types.BuildCacheObject) bucket.BuildCacheObject {
	return bucket.BuildCacheObject{AppID: object.AppID, IdentifierID: object.AppIdentifierID, Namespace: object.Namespace, ID: object.ID}
}

func (s *BuildCacheService) Reserve(ctx context.Context, appID, identifierID string, input BuildCacheInput) (*BuildCacheUpload, error) {
	if s.repo == nil {
		return nil, repository.ErrNotSupportedInStatelessMode
	}
	if input.Size <= 0 || input.Size > types.MaxBuildCacheObjectBytes || !buildHash.MatchString(input.SHA256) {
		return nil, validation.Errorf("cache", "invalid size or SHA-256")
	}
	if err := validateCacheUpload(input); err != nil {
		return nil, err
	}
	if s.cacheStore == nil {
		return nil, ErrBuildStorageUnavailable
	}
	previous, err := s.repo.Find(ctx, appID, identifierID, input.Namespace, input.Key)
	var missing *repository.ErrResourceNotFound
	if err != nil && !errors.As(err, &missing) {
		return nil, err
	}
	if err == nil && previous.Size == input.Size && previous.SHA256 == input.SHA256 {
		return &BuildCacheUpload{Object: previous, Cached: true}, nil
	}
	object, err := s.repo.Reserve(ctx, types.BuildCacheObject{ID: uuid.NewString(), AppID: appID, AppIdentifierID: identifierID, Namespace: input.Namespace, CacheKey: input.Key, Size: input.Size, SHA256: input.SHA256})
	if err != nil {
		return nil, err
	}
	upload, err := s.cacheStore.PresignPut(ctx, cacheRef(*object))
	if err != nil {
		return nil, errors.Join(err, s.repo.Delete(ctx, *object))
	}
	return &BuildCacheUpload{Object: object, Upload: upload}, nil
}

func (s *BuildCacheService) Get(ctx context.Context, appID, identifierID, id string) (*types.BuildCacheObject, error) {
	if s.repo == nil {
		return nil, repository.ErrNotSupportedInStatelessMode
	}
	if parsed, err := uuid.Parse(id); err != nil || parsed.String() != id {
		return nil, validation.Errorf("uploadId", "expected a canonical UUID")
	}
	if s.cacheStore == nil {
		return nil, ErrBuildStorageUnavailable
	}
	return s.repo.Get(ctx, appID, identifierID, id)
}

func (s *BuildCacheService) Find(ctx context.Context, appID, identifierID string, namespace types.BuildCacheNamespace, key string) (*types.BuildCacheObject, error) {
	if s.repo == nil {
		return nil, repository.ErrNotSupportedInStatelessMode
	}
	if err := validateCacheKey(namespace, key); err != nil {
		return nil, err
	}
	if s.cacheStore == nil {
		return nil, ErrBuildStorageUnavailable
	}
	return s.repo.Find(ctx, appID, identifierID, namespace, key)
}

func (s *BuildCacheService) Complete(ctx context.Context, appID, identifierID, id string) (*types.BuildCacheObject, error) {
	object, err := s.Get(ctx, appID, identifierID, id)
	if err != nil || object.PublishedAt != nil {
		return object, err
	}
	file, err := s.cacheStore.Get(ctx, cacheRef(*object))
	if err != nil {
		return nil, err
	}
	if file == nil {
		return nil, ErrBuildCacheIntegrity
	}
	defer file.Reader.Close()
	hash := sha256.New()
	limited := &io.LimitedReader{R: file.Reader, N: object.Size + 1}
	reader := io.TeeReader(limited, hash)
	contentErr := validateCacheContent(object.Namespace, reader)
	_, err = io.Copy(io.Discard, reader)
	if err != nil {
		return nil, err
	}
	if limited.N != 1 || hex.EncodeToString(hash.Sum(nil)) != object.SHA256 {
		contentErr = ErrBuildCacheIntegrity
	}
	if contentErr != nil {
		// The outbox waits for the signed URL to expire before deleting bytes.
		if err := s.repo.Delete(ctx, *object); err != nil {
			return nil, err
		}
		return nil, contentErr
	}
	return s.repo.Publish(ctx, *object)
}

func (s *BuildCacheService) UploadLocal(ctx context.Context, appID, identifierID, id string, body io.Reader) error {
	object, err := s.Get(ctx, appID, identifierID, id)
	if err != nil {
		return err
	}
	if object.PublishedAt != nil {
		return bucket.ErrCacheObjectExists
	}
	return s.cacheStore.Put(ctx, cacheRef(*object), io.LimitReader(body, object.Size+1))
}

// DownloadURL signs an object returned by Find, which only returns published entries.
func (s *BuildCacheService) DownloadURL(ctx context.Context, object types.BuildCacheObject) (string, error) {
	return s.cacheStore.PresignGet(ctx, cacheRef(object), time.Now().Add(5*time.Minute))
}
func (s *BuildCacheService) Download(ctx context.Context, object types.BuildCacheObject) (*types.BucketFile, error) {
	if object.PublishedAt == nil {
		return nil, ErrBuildCachePending
	}
	return s.cacheStore.Get(ctx, cacheRef(object))
}
