package store_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"xprem/internal/bucket"
	"xprem/internal/database"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/services"
	"xprem/internal/store"
	"xprem/internal/types"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestBuildCacheRetiredBytesRemainChargedUntilCleanup(t *testing.T) {
	for name, replace := range map[string]bool{"deleted uploads": false, "replaced archives": true} {
		t.Run(name, func(t *testing.T) {
			f := setupBuildStore(t)
			ctx := context.Background()
			repo := store.NewPostgresBuildCacheStore(&database.Engine{Queries: pgdb.New(f.pool), DB: f.pool})
			object := types.BuildCacheObject{AppID: f.app, AppIdentifierID: f.identifier, Namespace: types.BuildCacheGradle, CacheKey: "archive-v1-" + strings.Repeat("a", 64), Size: types.MaxBuildCacheObjectBytes, SHA256: strings.Repeat("b", 64)}
			for range types.MaxBuildCacheBytes / object.Size {
				object.ID = uuid.NewString()
				saved, err := repo.Reserve(ctx, object)
				require.NoError(t, err)
				if replace {
					_, err = repo.Publish(ctx, *saved)
				} else {
					err = repo.Delete(ctx, *saved)
				}
				require.NoError(t, err)
			}
			object.ID = uuid.NewString()
			_, err := repo.Reserve(ctx, object)
			require.ErrorIs(t, err, store.ErrBuildCacheFull, "retiring an upload must not release bytes still in the bucket")

			_, err = f.pool.Exec(ctx, "UPDATE build_cache_cleanup SET due_at = now() WHERE app_id = $1", f.app)
			require.NoError(t, err)
			cleanup := services.NewBuildCleanup(f.pool, &bucket.LocalBucket{BasePath: t.TempDir()})
			_, err = cleanup.SweepCache(ctx)
			require.NoError(t, err)
			_, err = repo.Reserve(ctx, object)
			require.NoError(t, err, "successful bucket cleanup releases the reservation")
		})
	}
}

func TestBuildCacheObjectLimitIncludesPendingPublishedAndRetired(t *testing.T) {
	f := setupBuildStore(t)
	ctx := context.Background()
	repo := store.NewPostgresBuildCacheStore(&database.Engine{Queries: pgdb.New(f.pool), DB: f.pool})
	object := types.BuildCacheObject{AppID: f.app, AppIdentifierID: f.identifier, Namespace: types.BuildCacheGradle, Size: 2048, SHA256: strings.Repeat("b", 64)}
	for i := range types.MaxBuildCacheObjects {
		object.ID = uuid.NewString()
		object.CacheKey = fmt.Sprintf("archive-v1-%064x", i)
		saved, err := repo.Reserve(ctx, object)
		require.NoError(t, err)
		switch i % 3 {
		case 0:
			_, err = repo.Publish(ctx, *saved)
		case 1:
			err = repo.Delete(ctx, *saved)
		}
		require.NoError(t, err)
	}
	object.ID = uuid.NewString()
	_, err := repo.Reserve(ctx, object)
	require.ErrorIs(t, err, store.ErrBuildCacheFull, "small upload reservations also need a finite bound")

	object.AppIdentifierID = insertIdentifier(t, f.identifiers, f.app, types.PlatformAndroid, "com.example.other")
	_, err = repo.Reserve(ctx, object)
	require.NoError(t, err, "the limit is scoped to the identifier")
}
