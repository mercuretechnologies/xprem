package bucket

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"xprem/internal/objectstore"
	"xprem/internal/types"

	"github.com/stretchr/testify/require"
)

func cacheFileContents(t *testing.T, file *types.BucketFile, err error) string {
	t.Helper()
	require.NoError(t, err)
	require.NotNil(t, file)
	content, err := io.ReadAll(file.Reader)
	require.NoError(t, file.Reader.Close())
	require.NoError(t, err)
	return string(content)
}

func TestLocalBuildCacheKeepsPublishedObjectSeparateFromStaging(t *testing.T) {
	artifactStore, _ := buildArtifactEnv(t, "tenant/")
	store := artifactStore.CacheStore()
	ctx, ref := context.Background(), testCacheObject()
	require.NoError(t, store.Put(ctx, ref, strings.NewReader("validated archive")))
	file, err := store.Get(ctx, ref)
	require.NoError(t, err)
	require.Nil(t, file)
	file, err = store.GetUpload(ctx, ref)
	require.Equal(t, "validated archive", cacheFileContents(t, file, err))
	require.NoError(t, store.Publish(ctx, ref))
	require.NoError(t, store.Put(ctx, ref, strings.NewReader("late upload")))
	file, err = store.GetUpload(ctx, ref)
	require.Equal(t, "late upload", cacheFileContents(t, file, err))
	file, err = store.Get(ctx, ref)
	require.Equal(t, "validated archive", cacheFileContents(t, file, err))
	require.NoError(t, store.Delete(ctx, ref))
	file, err = store.Get(ctx, ref)
	require.NoError(t, err)
	require.Nil(t, file)
	file, err = store.GetUpload(ctx, ref)
	require.NoError(t, err)
	require.Nil(t, file)
}

type stagingCloudObjectStore struct {
	objectstore.Store
	objects      map[string]string
	signedKey    string
	signedUpload objectstore.Upload
}

func (s *stagingCloudObjectStore) Put(_ context.Context, key string, body io.Reader) error {
	content, err := io.ReadAll(body)
	if err == nil {
		s.objects[key] = string(content)
	}
	return err
}

func (s *stagingCloudObjectStore) Get(_ context.Context, key string) (*types.BucketFile, error) {
	content, exists := s.objects[key]
	if !exists {
		return nil, nil
	}
	return &types.BucketFile{Reader: io.NopCloser(strings.NewReader(content))}, nil
}

func (s *stagingCloudObjectStore) Copy(_ context.Context, from, to string) error {
	content, exists := s.objects[from]
	if !exists {
		return errors.New("staged cache object absent")
	}
	s.objects[to] = content
	return nil
}

func (s *stagingCloudObjectStore) Delete(_ context.Context, key string) error {
	delete(s.objects, key)
	return nil
}

func (s *stagingCloudObjectStore) PresignPut(_ context.Context, key string, upload objectstore.Upload) (*objectstore.UploadRequest, error) {
	s.signedKey, s.signedUpload = key, upload
	return &objectstore.UploadRequest{URL: "https://storage.test/staging", Method: "PUT"}, nil
}

func TestCloudBuildCacheSignsOnlyStagingObject(t *testing.T) {
	backend := &stagingCloudObjectStore{objects: map[string]string{}}
	store := (&BuildArtifactStore{objectStore: objectstore.WithPrefix(backend, "tenant/")}).CacheStore()
	ctx, ref := context.Background(), testCacheObject()
	constraints := objectstore.Upload{Size: 2048, SHA256: strings.Repeat("a", 64)}
	upload, err := store.PresignPut(ctx, ref, constraints)
	require.NoError(t, err)
	require.NotNil(t, upload)
	require.Equal(t, "PUT", upload.Method)
	require.Equal(t, "tenant/"+ref.Key()+".upload", backend.signedKey)
	require.Equal(t, constraints, backend.signedUpload)
	require.NoError(t, backend.Put(ctx, backend.signedKey, strings.NewReader("validated archive")))
	require.NoError(t, store.Publish(ctx, ref))
	require.NoError(t, backend.Put(ctx, backend.signedKey, strings.NewReader("late direct PUT")))
	file, err := store.Get(ctx, ref)
	require.Equal(t, "validated archive", cacheFileContents(t, file, err))
	require.NoError(t, store.Delete(ctx, ref))
	require.Empty(t, backend.objects)
}
