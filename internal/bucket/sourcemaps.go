package bucket

import (
	"context"
	"io"
	"log"
	"xprem/config"
	"xprem/internal/objectstore"
)

var sourcemapsLocationEnv = map[objectstore.Mode]string{
	objectstore.ModeS3:    "S3_BUCKET_SOURCEMAPS_NAME",
	objectstore.ModeGCS:   "GCS_BUCKET_SOURCEMAPS_NAME",
	objectstore.ModeAzure: "AZURE_BLOB_SOURCEMAPS_CONTAINER_NAME",
	objectstore.ModeLocal: "LOCAL_SOURCEMAPS_BASE_PATH",
}

// SourcemapStore holds the source maps of published bundles at
// {appId}/sourcemaps/{hash}, under the bucket key prefix. Its location may be
// the updates one: the directory is reserved there.
type SourcemapStore struct {
	objectStore   objectstore.Store
	localUploads  bool
	sharesUpdates bool
}

// SharesUpdatesLocation reports whether the maps live in the updates bucket.
func (s *SourcemapStore) SharesUpdatesLocation() bool {
	return s.sharesUpdates
}

// OpenSourcemapStore opens the store UPLOAD_SOURCEMAPS points at; nil when the
// feature is off.
func OpenSourcemapStore() (*SourcemapStore, error) {
	if !config.IsSourcemapUploadEnabled() {
		return nil, nil
	}
	mode := objectstore.ResolveMode()
	objectStore, sharesUpdates, err := objectstore.OpenFeatureStore(sourcemapsLocationEnv, true)
	if err != nil {
		return nil, err
	}
	if sharesUpdates {
		kind := objectstore.LocationKind(mode)
		log.Printf("WARNING: source maps share the updates %s (%s). They embed the app's source code: this %s must not be publicly readable", kind, sourcemapsLocationEnv[mode], kind)
	}
	return &SourcemapStore{
		objectStore:   objectstore.WithPrefix(objectStore, ResolveKeyPrefix()),
		localUploads:  mode == objectstore.ModeLocal,
		sharesUpdates: sharesUpdates,
	}, nil
}

func (s *SourcemapStore) key(appId, hash string) (string, error) {
	if err := validateSegment("appId", appId); err != nil {
		return "", err
	}
	if err := ValidateBlobHash(hash); err != nil {
		return "", err
	}
	return SourcemapObjectKey(appId, hash), nil
}

func (s *SourcemapStore) Exists(ctx context.Context, appId, hash string) (bool, error) {
	key, err := s.key(appId, hash)
	if err != nil {
		return false, err
	}
	return s.objectStore.Exists(ctx, key)
}

// Put stores a source map, refusing bytes that do not hash to hash.
func (s *SourcemapStore) Put(ctx context.Context, appId, hash string, body io.Reader) error {
	key, err := s.key(appId, hash)
	if err != nil {
		return err
	}
	return s.objectStore.Put(ctx, key, newHashCheckedReader(body, hash))
}

// PresignPut takes the branch the bundle is published under, which is what
// scoped API keys are judged against on the upload route.
func (s *SourcemapStore) PresignPut(ctx context.Context, appId, hash, branch string) (*objectstore.UploadRequest, error) {
	key, err := s.key(appId, hash)
	if err != nil {
		return nil, err
	}
	if err := validateBranch(branch); err != nil {
		return nil, err
	}
	return presignUpload(ctx, s.objectStore, s.localUploads, appId, branch, key)
}
