package bucket

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"
	"xprem/internal/types"

	"github.com/stretchr/testify/require"
)

func TestLocalBuildCacheCannotOverwritePublishedBytes(t *testing.T) {
	b := &LocalBucket{BasePath: t.TempDir(), KeyPrefix: "tenant/"}
	ref := BuildCacheObject{AppID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", IdentifierID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", Namespace: types.BuildCacheGradle, ID: "cccccccc-cccc-cccc-cccc-cccccccccccc"}
	require.NoError(t, b.PutBuildCache(context.Background(), ref, strings.NewReader("original")))
	require.ErrorIs(t, b.PutBuildCache(context.Background(), ref, strings.NewReader("replacement")), ErrCacheObjectExists)
	file, err := b.GetBuildCache(context.Background(), ref)
	require.NoError(t, err)
	defer file.Reader.Close()
	content, err := io.ReadAll(file.Reader)
	require.NoError(t, err)
	require.Equal(t, "original", string(content))
	localPath := b.cachePath(ref)
	// A process crash can leave an unfinished upload alongside the object.
	require.NoError(t, os.WriteFile(localPath+".upload", []byte("unfinished"), 0600))
	require.NoError(t, b.DeleteBuildCache(context.Background(), ref))
	require.NoFileExists(t, localPath)
	require.NoFileExists(t, localPath+".upload")
}

func TestBuildCacheValidationRejectsInvalidPaths(t *testing.T) {
	ctx := context.Background()
	valid := BuildCacheObject{AppID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", IdentifierID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", Namespace: types.BuildCacheGradle, ID: "cccccccc-cccc-cccc-cccc-cccccccccccc"}
	for _, field := range []string{"appId", "identifierId", "namespace", "unknown namespace", "uploadId"} {
		t.Run(field, func(t *testing.T) {
			ref := valid
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
			// Any delegation would panic: invalid paths must stop at the wrapper.
			b := &validatingBucket{}
			_, err := b.GetBuildCache(ctx, ref)
			require.Error(t, err)
			require.Error(t, b.DeleteBuildCache(ctx, ref))
			_, err = b.RequestBuildCacheUploadURL(ctx, ref)
			require.Error(t, err)
			_, err = b.RequestBuildCacheDownloadURL(ctx, ref, time.Now().Add(time.Minute))
			require.Error(t, err)
			require.ErrorContains(t, b.PutBuildCache(ctx, ref, strings.NewReader("cache")), "invalid")
		})
	}
}
