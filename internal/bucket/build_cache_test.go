package bucket

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"xprem/internal/types"

	"github.com/stretchr/testify/require"
)

func testCacheObject() BuildCacheObject {
	return BuildCacheObject{AppID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", IdentifierID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", Namespace: types.BuildCacheGradle, ID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc"}
}

func TestLocalBuildCacheRoundTripsNextToTheArtifacts(t *testing.T) {
	artifactStore, dir := buildArtifactEnv(t, "tenant/")
	store := artifactStore.CacheStore()
	ctx := context.Background()
	ref := testCacheObject()
	upload, err := store.PresignPut(ctx, ref)
	require.NoError(t, err)
	require.Nil(t, upload, "local archives go through the server's cache route")
	require.NoError(t, store.Put(ctx, ref, strings.NewReader("archive")))
	require.FileExists(t, filepath.Join(dir, "tenant", filepath.FromSlash(ref.Key())))
	file, err := store.Get(ctx, ref)
	require.NoError(t, err)
	content, err := io.ReadAll(file.Reader)
	file.Reader.Close()
	require.NoError(t, err)
	require.Equal(t, "archive", string(content))
	require.NoError(t, store.Delete(ctx, ref))
	require.NoError(t, store.Delete(ctx, ref))
	file, err = store.Get(ctx, ref)
	require.NoError(t, err)
	require.Nil(t, file)
}

func TestCloudBuildCacheRefusesServerUploads(t *testing.T) {
	store := &BuildCacheStore{objectStore: unreachableObjectStore{}}
	require.ErrorIs(t, store.Put(context.Background(), testCacheObject(), strings.NewReader("archive")), ErrCacheDirectUploadRequired)
}

func TestBuildCacheValidationRejectsInvalidPaths(t *testing.T) {
	ctx := context.Background()
	for _, field := range []string{"appId", "identifierId", "namespace", "unknown namespace", "uploadId"} {
		t.Run(field, func(t *testing.T) {
			ref := testCacheObject()
			switch field {
			case "appId":
				ref.AppID = "../ota"
			case "identifierId":
				ref.IdentifierID = "../ota"
			case "namespace":
				ref.Namespace = "../ota"
			case "unknown namespace":
				ref.Namespace = "unknown"
			case "uploadId":
				ref.ID = "../ota"
			}
			store := &BuildCacheStore{objectStore: unreachableObjectStore{}, localUploads: true}
			_, err := store.Get(ctx, ref)
			require.Error(t, err)
			require.Error(t, store.Delete(ctx, ref))
			_, err = store.PresignPut(ctx, ref)
			require.Error(t, err)
			_, err = store.PresignGet(ctx, ref, time.Now().Add(time.Minute))
			require.Error(t, err)
			require.ErrorContains(t, store.Put(ctx, ref, strings.NewReader("cache")), "invalid")
		})
	}
}
