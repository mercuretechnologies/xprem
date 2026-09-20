package store_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"xprem/internal/bucket"
	"xprem/internal/database"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/handlers"
	"xprem/internal/services"
	"xprem/internal/store"
	"xprem/internal/types"
	"xprem/internal/validation"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

func TestBuildCachePublicationAndReplacement(t *testing.T) {
	f := setupBuildStore(t)
	ctx := context.Background()
	repo := store.NewPostgresBuildCacheStore(&database.Engine{Queries: pgdb.New(f.pool), DB: f.pool})
	storage := &bucket.LocalBucket{BasePath: t.TempDir()}
	service := services.NewBuildCacheService(repo, storage)
	first := buildCacheArchive(t, "first")
	input := services.BuildCacheInput{Namespace: types.BuildCacheCcache, Key: "archive-v1-" + strings.Repeat("a", 64), Size: int64(len(first)), SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(first)))}
	upload, err := service.Reserve(ctx, f.app, f.identifier, input)
	require.NoError(t, err)
	_, err = service.Find(ctx, f.app, f.identifier, input.Namespace, input.Key)
	require.Error(t, err, "unverified uploads are never cache hits")
	require.NoError(t, service.UploadLocal(ctx, f.app, f.identifier, upload.Object.ID, strings.NewReader(first)))
	_, err = service.Complete(ctx, f.app, f.identifier, upload.Object.ID)
	require.NoError(t, err)
	_, err = service.Complete(ctx, f.app, f.identifier, upload.Object.ID)
	require.NoError(t, err, "completion is retryable")
	duplicate, err := service.Reserve(ctx, f.app, f.identifier, input)
	require.NoError(t, err)
	require.True(t, duplicate.Cached)
	require.Equal(t, upload.Object.ID, duplicate.Object.ID)
	require.Error(t, service.UploadLocal(ctx, f.app, f.identifier, upload.Object.ID, strings.NewReader("evil!")))

	later := buildCacheArchive(t, "later")
	input.SHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(later)))
	newUpload, err := service.Reserve(ctx, f.app, f.identifier, input)
	require.NoError(t, err)
	require.NoError(t, service.UploadLocal(ctx, f.app, f.identifier, newUpload.Object.ID, strings.NewReader(later)))
	_, err = service.Complete(ctx, f.app, f.identifier, newUpload.Object.ID)
	require.NoError(t, err)
	found, err := service.Find(ctx, f.app, f.identifier, input.Namespace, input.Key)
	require.NoError(t, err)
	require.Equal(t, newUpload.Object.ID, found.ID)
	var queued int
	require.NoError(t, f.pool.QueryRow(ctx, "SELECT count(*) FROM build_cache_cleanup WHERE id = $1", upload.Object.ID).Scan(&queued))
	require.Equal(t, 1, queued)

	invalid := strings.Repeat("not a TAR archive\n", 64)
	for _, rejected := range []struct {
		name, declared, uploaded string
		err                      error
	}{
		{"checksum mismatch", buildCacheArchive(t, "third"), buildCacheArchive(t, "wrong"), services.ErrBuildCacheIntegrity},
		{"invalid archive with matching checksum", invalid, invalid, services.ErrBuildCacheArchive},
	} {
		t.Run(rejected.name, func(t *testing.T) {
			input.Size = int64(len(rejected.declared))
			input.SHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(rejected.declared)))
			bad, err := service.Reserve(ctx, f.app, f.identifier, input)
			require.NoError(t, err)
			require.NoError(t, service.UploadLocal(ctx, f.app, f.identifier, bad.Object.ID, strings.NewReader(rejected.uploaded)))
			_, err = service.Complete(ctx, f.app, f.identifier, bad.Object.ID)
			require.ErrorIs(t, err, rejected.err)
			found, err := service.Find(ctx, f.app, f.identifier, input.Namespace, input.Key)
			require.NoError(t, err)
			require.Equal(t, newUpload.Object.ID, found.ID, "a rejected replacement must leave the published archive available")
			_, err = service.Get(ctx, f.app, f.identifier, bad.Object.ID)
			var missing *store.ErrResourceNotFound
			require.ErrorAs(t, err, &missing)
			require.NoError(t, f.pool.QueryRow(ctx, "SELECT count(*) FROM build_cache_cleanup WHERE id = $1", bad.Object.ID).Scan(&queued))
			require.Equal(t, 1, queued)
		})
	}

	// App deletion retains a bucket cleanup record after all foreign keys cascade.
	_, err = f.pool.Exec(ctx, "DELETE FROM apps WHERE id = $1", f.app)
	require.NoError(t, err)
	require.NoError(t, f.pool.QueryRow(ctx, "SELECT count(*) FROM build_cache_cleanup WHERE app_id = $1", f.app).Scan(&queued))
	require.Equal(t, 4, queued)
	_, err = f.pool.Exec(ctx, "UPDATE build_cache_cleanup SET due_at = now() WHERE app_id = $1", f.app)
	require.NoError(t, err)
	cleanup := services.NewBuildCleanup(f.pool, storage)
	_, err = cleanup.SweepCache(ctx)
	require.NoError(t, err)
	file, err := storage.GetBuildCache(ctx, bucket.BuildCacheObject{AppID: f.app, IdentifierID: f.identifier, Namespace: input.Namespace, ID: newUpload.Object.ID})
	require.NoError(t, err)
	require.Nil(t, file)
}

func TestBuildCacheRejectsInvalidInput(t *testing.T) {
	ctx := context.Background()
	// Invalid input must fail before either storage dependency is used.
	service := services.NewBuildCacheService(store.NewPostgresBuildCacheStore(nil), nil)
	for _, entry := range []struct {
		namespace types.BuildCacheNamespace
		key       string
	}{
		{types.BuildCacheGradle, strings.Repeat("a", 32)},
		{types.BuildCacheCcache, strings.Repeat("b", 40) + "R"},
		{"unknown", "archive-v1-" + strings.Repeat("a", 64)},
	} {
		t.Run(string(entry.namespace), func(t *testing.T) {
			_, err := service.Find(ctx, "app", "identifier", entry.namespace, entry.key)
			require.True(t, validation.IsValidationError(err))
			_, err = service.Reserve(ctx, "app", "identifier", services.BuildCacheInput{Namespace: entry.namespace, Key: entry.key, Size: 1024, SHA256: strings.Repeat("a", 64)})
			require.True(t, validation.IsValidationError(err))
		})
	}
	for _, namespace := range []types.BuildCacheNamespace{types.BuildCacheGradle, types.BuildCacheCcache} {
		t.Run(string(namespace)+"/undersized archive", func(t *testing.T) {
			_, err := service.Reserve(ctx, "app", "identifier", services.BuildCacheInput{Namespace: namespace, Key: "archive-v1-" + strings.Repeat("a", 64), Size: 1023, SHA256: strings.Repeat("a", 64)})
			require.True(t, validation.IsValidationError(err))
		})
	}
}

func TestBuildCacheConcurrentArchivePublication(t *testing.T) {
	f := setupBuildStore(t)
	ctx := context.Background()
	repo := store.NewPostgresBuildCacheStore(&database.Engine{Queries: pgdb.New(f.pool), DB: f.pool})
	service := services.NewBuildCacheService(repo, &bucket.LocalBucket{BasePath: t.TempDir()})
	key := "archive-v1-" + strings.Repeat("a", 64)
	ids := make([]string, 0, 2)
	for _, content := range []string{"first", "later"} {
		content = buildCacheArchive(t, content)
		upload, err := service.Reserve(ctx, f.app, f.identifier, services.BuildCacheInput{Namespace: types.BuildCacheGradle, Key: key, Size: int64(len(content)), SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(content)))})
		require.NoError(t, err)
		require.NoError(t, service.UploadLocal(ctx, f.app, f.identifier, upload.Object.ID, strings.NewReader(content)))
		ids = append(ids, upload.Object.ID)
	}
	ready := make(chan struct{})
	errors := make(chan error, len(ids))
	for _, id := range ids {
		go func() {
			<-ready
			_, err := service.Complete(ctx, f.app, f.identifier, id)
			errors <- err
		}()
	}
	close(ready)
	for range ids {
		require.NoError(t, <-errors)
	}
	found, err := service.Find(ctx, f.app, f.identifier, types.BuildCacheGradle, key)
	require.NoError(t, err)
	require.Contains(t, ids, found.ID)
	var published, queued int
	require.NoError(t, f.pool.QueryRow(ctx, "SELECT count(*) FROM build_cache_objects WHERE app_id = $1 AND published_at IS NOT NULL", f.app).Scan(&published))
	require.Equal(t, 1, published)
	require.NoError(t, f.pool.QueryRow(ctx, "SELECT count(*) FROM build_cache_cleanup WHERE app_id = $1", f.app).Scan(&queued))
	require.Equal(t, 1, queued)
}

func TestBuildCacheConcurrentReservationsRespectQuota(t *testing.T) {
	f := setupBuildStore(t)
	ctx := context.Background()
	repo := store.NewPostgresBuildCacheStore(&database.Engine{Queries: pgdb.New(f.pool), DB: f.pool})
	var wg sync.WaitGroup
	errors := make(chan error, 24)
	for range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := repo.Reserve(ctx, types.BuildCacheObject{ID: uuid.NewString(), AppID: f.app, AppIdentifierID: f.identifier, Namespace: types.BuildCacheGradle, CacheKey: "archive-v1-" + strings.Repeat("a", 64), Size: types.MaxBuildCacheObjectBytes, SHA256: strings.Repeat("b", 64)})
			errors <- err
		}()
	}
	wg.Wait()
	close(errors)
	accepted := 0
	for err := range errors {
		if err == nil {
			accepted++
		} else {
			require.ErrorIs(t, err, store.ErrBuildCacheFull)
		}
	}
	require.Equal(t, int(types.MaxBuildCacheBytes/types.MaxBuildCacheObjectBytes), accepted)
}

func TestBuildCacheRejectsUnknownNamespace(t *testing.T) {
	f := setupBuildStore(t)
	ctx := context.Background()
	repo := store.NewPostgresBuildCacheStore(&database.Engine{Queries: pgdb.New(f.pool), DB: f.pool})
	_, err := repo.Reserve(ctx, types.BuildCacheObject{ID: uuid.NewString(), AppID: f.app, AppIdentifierID: f.identifier, Namespace: "unknown", CacheKey: "key", Size: 1, SHA256: strings.Repeat("a", 64)})
	var constraint *pgconn.PgError
	require.ErrorAs(t, err, &constraint)
	require.Equal(t, "23514", constraint.Code)
	_, err = f.pool.Exec(ctx, "INSERT INTO build_cache_cleanup (id, app_id, app_identifier_id, namespace, size) VALUES ($1, $2, $3, 'unknown', 1)", uuid.NewString(), f.app, f.identifier)
	require.ErrorAs(t, err, &constraint)
	require.Equal(t, "23514", constraint.Code)
}

// These requests start after authentication. The router tests cover build:create;
// here the real SQL must reject an upload ID outside that authenticated scope.
func TestBuildCacheHTTPRejectsForeignAndExpiredUploads(t *testing.T) {
	owner := setupBuildStore(t)
	otherApp := setupBuildStore(t)
	otherIdentifier := insertIdentifier(t, owner.identifiers, owner.app, types.PlatformAndroid, "com.example.other")
	ctx := context.Background()
	repo := store.NewPostgresBuildCacheStore(&database.Engine{Queries: pgdb.New(owner.pool), DB: owner.pool})
	storage := &bucket.LocalBucket{BasePath: t.TempDir()}
	service := services.NewBuildCacheService(repo, storage)
	handler := handlers.NewBuildCacheHandler(service)
	content := buildCacheArchive(t, "private compiled bytes")

	for _, scope := range []struct {
		name, app, identifier string
		expired               bool
	}{
		{"other app", otherApp.app, otherApp.identifier, false},
		{"other identifier", owner.app, otherIdentifier, false},
		{"expired", owner.app, owner.identifier, true},
	} {
		for _, published := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/published=%t", scope.name, published), func(t *testing.T) {
				input := services.BuildCacheInput{Namespace: types.BuildCacheCcache, Key: fmt.Sprintf("archive-v1-%x", sha256.Sum256([]byte(uuid.NewString()))), Size: int64(len(content)), SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(content)))}
				upload, err := service.Reserve(ctx, owner.app, owner.identifier, input)
				require.NoError(t, err)
				id := upload.Object.ID
				require.NoError(t, service.UploadLocal(ctx, owner.app, owner.identifier, id, strings.NewReader(content)))
				if published {
					_, err = service.Complete(ctx, owner.app, owner.identifier, id)
					require.NoError(t, err)
				}
				serve := func(ctx context.Context, method string, handle http.HandlerFunc, app, identifier string, body *strings.Reader) *httptest.ResponseRecorder {
					req := httptest.NewRequestWithContext(ctx, method, "/", body)
					req = mux.SetURLVars(req, map[string]string{"APP_ID": app, "UPLOAD_ID": id, "NAMESPACE": string(input.Namespace), "CACHE_KEY": input.Key})
					req = req.WithContext(services.WithBuildIdentifier(req.Context(), identifier))
					response := httptest.NewRecorder()
					handle(response, req)
					return response
				}
				// The rightful owner can read published bytes, but not a pending upload.
				response := serve(ctx, http.MethodGet, handler.Download, owner.app, owner.identifier, strings.NewReader(""))
				if published {
					require.Equal(t, http.StatusOK, response.Code, response.Body.String())
					require.Equal(t, content, response.Body.String())
				} else {
					require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
					require.NotContains(t, response.Body.String(), content)
				}
				if scope.expired {
					_, err = owner.pool.Exec(ctx, "UPDATE build_cache_objects SET expires_at = now() - interval '1 second' WHERE id = $1", id)
					require.NoError(t, err)
				}
				var before string
				require.NoError(t, owner.pool.QueryRow(ctx, "SELECT to_jsonb(c)::text FROM build_cache_objects c WHERE id = $1", id).Scan(&before))
				ref := bucket.BuildCacheObject{AppID: owner.app, IdentifierID: owner.identifier, Namespace: input.Namespace, ID: id}
				for _, operation := range []struct {
					name, method string
					handle       http.HandlerFunc
				}{
					{"upload", http.MethodPut, handler.UploadLocal},
					{"complete", http.MethodPost, handler.Complete},
					{"download", http.MethodGet, handler.Download},
					{"find", http.MethodGet, handler.Find},
				} {
					t.Run(operation.name, func(t *testing.T) {
						body := strings.NewReader("replacement bytes")
						response := serve(ctx, operation.method, operation.handle, scope.app, scope.identifier, body)
						require.Equal(t, http.StatusNotFound, response.Code, response.Body.String())
						require.NotContains(t, response.Body.String(), content)
						require.NotContains(t, response.Body.String(), input.SHA256)
						require.Equal(t, len("replacement bytes"), body.Len())
						var after string
						require.NoError(t, owner.pool.QueryRow(ctx, "SELECT to_jsonb(c)::text FROM build_cache_objects c WHERE id = $1", id).Scan(&after))
						require.Equal(t, before, after, "refused requests must not mutate the owner's cache entry")
						stored, err := os.ReadFile(filepath.Join(storage.BasePath, ref.Key()))
						require.NoError(t, err)
						require.Equal(t, content, string(stored))
						var queued int
						require.NoError(t, owner.pool.QueryRow(ctx, "SELECT count(*) FROM build_cache_cleanup WHERE id = $1", id).Scan(&queued))
						require.Zero(t, queued, "refused requests must not schedule deletion")
					})
				}
			})
		}
	}
}

func buildCacheArchive(t *testing.T, content string) string {
	t.Helper()
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	require.NoError(t, writer.WriteHeader(&tar.Header{Name: "./", Typeflag: tar.TypeDir, Mode: 0o755}))
	require.NoError(t, writer.WriteHeader(&tar.Header{Name: "./result", Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(content))}))
	_, err := writer.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	return archive.String()
}
