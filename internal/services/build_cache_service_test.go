package services_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"sync"
	"testing"
	"time"
	"xprem/internal/bucket"
	"xprem/internal/repository"
	"xprem/internal/services"
	"xprem/internal/types"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type cacheServiceRepository struct {
	mu      sync.Mutex
	objects map[string]types.BuildCacheObject
}

func (r *cacheServiceRepository) Reserve(_ context.Context, object types.BuildCacheObject) (*types.BuildCacheObject, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	object.CreatedAt = time.Now()
	object.ExpiresAt = object.CreatedAt.Add(24 * time.Hour)
	r.objects[object.ID] = object
	return &object, nil
}

func (r *cacheServiceRepository) Get(_ context.Context, app, identifier, id string) (*types.BuildCacheObject, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	object, exists := r.objects[id]
	if !exists || object.AppID != app || object.AppIdentifierID != identifier {
		return nil, &repository.ErrResourceNotFound{Resource: "build cache", Identifier: id}
	}
	return &object, nil
}

func (r *cacheServiceRepository) Find(_ context.Context, app, identifier string, namespace types.BuildCacheNamespace, key string) (*types.BuildCacheObject, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, object := range r.objects {
		if object.AppID == app && object.AppIdentifierID == identifier && object.Namespace == namespace && object.CacheKey == key && object.PublishedAt != nil {
			return &object, nil
		}
	}
	return nil, &repository.ErrResourceNotFound{Resource: "build cache", Identifier: key}
}

func (r *cacheServiceRepository) Publish(_ context.Context, object types.BuildCacheObject) (*types.BuildCacheObject, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	current, exists := r.objects[object.ID]
	if !exists {
		return nil, &repository.ErrResourceNotFound{Resource: "build cache", Identifier: object.ID}
	}
	if current.PublishedAt == nil {
		now := time.Now()
		current.PublishedAt = &now
		r.objects[current.ID] = current
	}
	return &current, nil
}

func (r *cacheServiceRepository) Delete(_ context.Context, object types.BuildCacheObject) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if current, exists := r.objects[object.ID]; exists && current.PublishedAt == nil {
		delete(r.objects, object.ID)
	}
	return nil
}

type cacheServiceFixture struct {
	service *services.BuildCacheService
	store   *bucket.BuildCacheStore
	repo    *cacheServiceRepository
	app     string
	id      string
	input   services.BuildCacheInput
	upload  *services.BuildCacheUpload
}

func newCacheServiceFixture(t *testing.T, content []byte) *cacheServiceFixture {
	t.Helper()
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_BUILDS_BASE_PATH", t.TempDir())
	t.Setenv("BUCKET_KEY_PREFIX", "")
	artifacts, err := bucket.OpenBuildArtifactStore()
	require.NoError(t, err)
	checksum := sha256.Sum256(content)
	repo := &cacheServiceRepository{objects: make(map[string]types.BuildCacheObject)}
	f := &cacheServiceFixture{
		service: services.NewBuildCacheService(repo, artifacts.CacheStore()),
		store:   artifacts.CacheStore(),
		repo:    repo,
		app:     uuid.NewString(),
		id:      uuid.NewString(),
		input: services.BuildCacheInput{
			Namespace: types.BuildCacheCcache,
			Key:       "archive-v1-" + hex.EncodeToString(checksum[:]),
			Size:      int64(len(content)),
			SHA256:    hex.EncodeToString(checksum[:]),
		},
	}
	f.upload, err = f.service.Reserve(t.Context(), f.app, f.id, f.input)
	require.NoError(t, err)
	return f
}

func serviceCacheArchive(t *testing.T, content string) []byte {
	t.Helper()
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	require.NoError(t, writer.WriteHeader(&tar.Header{Name: "cache-entry", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(content))}))
	_, err := io.WriteString(writer, content)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	return archive.Bytes()
}

func (f *cacheServiceFixture) downloaded(t *testing.T) []byte {
	t.Helper()
	object, err := f.service.Find(t.Context(), f.app, f.id, f.input.Namespace, f.input.Key)
	require.NoError(t, err)
	file, err := f.service.Download(t.Context(), *object)
	require.NoError(t, err)
	require.NotNil(t, file)
	defer file.Reader.Close()
	content, err := io.ReadAll(file.Reader)
	require.NoError(t, err)
	checksum := sha256.Sum256(content)
	require.Equal(t, object.Size, int64(len(content)))
	require.Equal(t, object.SHA256, hex.EncodeToString(checksum[:]), "published metadata must describe the immutable downloaded bytes")
	return content
}

type delayedCacheUpload struct {
	reader  *bytes.Reader
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *delayedCacheUpload) Read(buffer []byte) (int, error) {
	r.once.Do(func() {
		close(r.entered)
		<-r.release
	})
	return r.reader.Read(buffer)
}

func (f *cacheServiceFixture) delayedUpload(t *testing.T, content []byte) (func(), <-chan error) {
	t.Helper()
	body := &delayedCacheUpload{reader: bytes.NewReader(content), entered: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	unblock := func() { release.Do(func() { close(body.release) }) }
	t.Cleanup(unblock)
	finished := make(chan error, 1)
	go func() {
		finished <- f.service.UploadLocal(t.Context(), f.app, f.id, f.upload.Object.ID, body)
	}()
	select {
	case <-body.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the admitted upload did not reach its body")
	}
	return unblock, finished
}

func (f *cacheServiceFixture) ref() bucket.BuildCacheObject {
	return bucket.BuildCacheObject{AppID: f.app, IdentifierID: f.id, Namespace: f.input.Namespace, ID: f.upload.Object.ID}
}

func TestBuildCacheAdmittedUploadCleansStagingAfterLifecycleChange(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		published bool
		changed   bool
	}{
		{name: "published", published: true},
		{name: "published with different bytes", published: true, changed: true},
		{name: "deleted"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			original := serviceCacheArchive(t, "verified contents")
			f := newCacheServiceFixture(t, original)
			require.NoError(t, f.service.UploadLocal(t.Context(), f.app, f.id, f.upload.Object.ID, bytes.NewReader(original)))
			content := original
			if scenario.changed {
				content = serviceCacheArchive(t, "replaced contents")
			}
			unblock, finished := f.delayedUpload(t, content)
			if scenario.published {
				_, err := f.service.Complete(t.Context(), f.app, f.id, f.upload.Object.ID)
				require.NoError(t, err)
			} else {
				require.NoError(t, f.repo.Delete(t.Context(), *f.upload.Object))
			}
			unblock()
			select {
			case err := <-finished:
				if scenario.published {
					require.ErrorIs(t, err, bucket.ErrCacheObjectExists)
				} else {
					var missing *repository.ErrResourceNotFound
					require.ErrorAs(t, err, &missing)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the admitted upload did not finish")
			}
			staged, err := f.store.GetUpload(t.Context(), f.ref())
			require.NoError(t, err)
			require.Nil(t, staged, "a late admitted upload must not leave bytes without a pending reservation")
			if scenario.published {
				require.Equal(t, original, f.downloaded(t))
			}
		})
	}
}

func TestBuildCacheCompleteRejectsSizeAndChecksumMismatch(t *testing.T) {
	original := serviceCacheArchive(t, "verified contents")
	for _, rejected := range []struct {
		name    string
		content []byte
	}{
		{name: "checksum", content: serviceCacheArchive(t, "replaced contents")},
		{name: "truncated", content: original[:len(original)-1]},
		{name: "extra byte", content: append(bytes.Clone(original), 0)},
	} {
		t.Run(rejected.name, func(t *testing.T) {
			f := newCacheServiceFixture(t, original)
			require.NoError(t, f.service.UploadLocal(t.Context(), f.app, f.id, f.upload.Object.ID, bytes.NewReader(rejected.content)))
			_, err := f.service.Complete(t.Context(), f.app, f.id, f.upload.Object.ID)
			require.ErrorIs(t, err, services.ErrBuildCacheIntegrity)
			_, err = f.service.Find(t.Context(), f.app, f.id, f.input.Namespace, f.input.Key)
			var missing *repository.ErrResourceNotFound
			require.ErrorAs(t, err, &missing)
		})
	}
}

func TestBuildCachePublishedArchiveSurvivesStagingReplay(t *testing.T) {
	original := serviceCacheArchive(t, "verified contents")
	f := newCacheServiceFixture(t, original)
	require.NoError(t, f.service.UploadLocal(t.Context(), f.app, f.id, f.upload.Object.ID, bytes.NewReader(original)))
	_, err := f.service.Complete(t.Context(), f.app, f.id, f.upload.Object.ID)
	require.NoError(t, err)
	require.NoError(t, f.store.Put(t.Context(), f.ref(), bytes.NewReader(serviceCacheArchive(t, "replaced contents"))))
	require.Equal(t, original, f.downloaded(t))
	_, err = f.service.Complete(t.Context(), f.app, f.id, f.upload.Object.ID)
	require.NoError(t, err)
	require.Equal(t, original, f.downloaded(t))
}

func TestBuildCacheCompletePublishesExistingFinalArchive(t *testing.T) {
	original := serviceCacheArchive(t, "verified contents")
	f := newCacheServiceFixture(t, original)
	require.NoError(t, f.service.UploadLocal(t.Context(), f.app, f.id, f.upload.Object.ID, bytes.NewReader(original)))
	require.NoError(t, f.store.Publish(t.Context(), f.ref()))
	require.NoError(t, f.store.DeleteUpload(t.Context(), f.ref()))
	_, err := f.service.Complete(t.Context(), f.app, f.id, f.upload.Object.ID)
	require.NoError(t, err)
	require.Equal(t, original, f.downloaded(t))
}

type pauseCompletion struct{}

// pausedCacheRepository holds a Complete marked with pauseCompletion right after it read its object.
type pausedCacheRepository struct {
	services.BuildCacheRepository
	admitted chan struct{}
	release  chan struct{}
	once     sync.Once
}

func (r *pausedCacheRepository) Get(ctx context.Context, app, identifier, id string) (*types.BuildCacheObject, error) {
	object, err := r.BuildCacheRepository.Get(ctx, app, identifier, id)
	if ctx.Value(pauseCompletion{}) != nil {
		r.once.Do(func() {
			close(r.admitted)
			<-r.release
		})
	}
	return object, err
}

func TestBuildCacheConcurrentCompleteKeepsPublishedBytes(t *testing.T) {
	original := serviceCacheArchive(t, "verified contents")
	f := newCacheServiceFixture(t, original)
	require.NoError(t, f.service.UploadLocal(t.Context(), f.app, f.id, f.upload.Object.ID, bytes.NewReader(original)))
	paused := &pausedCacheRepository{BuildCacheRepository: f.repo, admitted: make(chan struct{}), release: make(chan struct{})}
	f.service = services.NewBuildCacheService(paused, f.store)
	late := make(chan error, 1)
	go func() {
		_, err := f.service.Complete(context.WithValue(t.Context(), pauseCompletion{}, true), f.app, f.id, f.upload.Object.ID)
		late <- err
	}()
	select {
	case <-paused.admitted:
	case <-time.After(5 * time.Second):
		t.Fatal("the late completion was not admitted")
	}
	_, err := f.service.Complete(t.Context(), f.app, f.id, f.upload.Object.ID)
	require.NoError(t, err)
	require.NoError(t, f.store.Put(t.Context(), f.ref(), bytes.NewReader(serviceCacheArchive(t, "replaced contents"))))
	close(paused.release)
	require.NoError(t, <-late)
	require.Equal(t, original, f.downloaded(t))
}
