// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"xprem/config"
	"xprem/internal/cache"
	"xprem/internal/services"
	"xprem/internal/store"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
)

func resetObserveCache(t *testing.T) {
	t.Helper()
	require.NoError(t, cache.GetCache().Clear())
}

// countingAppRepo counts GetAppByID calls so the cache's effect is observable.
type countingAppRepo struct {
	calls   int64
	unknown bool
}

func (r *countingAppRepo) GetAppByID(_ context.Context, _ string) (config.AppConfig, error) {
	atomic.AddInt64(&r.calls, 1)
	if r.unknown {
		return config.AppConfig{}, fmt.Errorf("app not found")
	}
	return config.AppConfig{}, nil
}
func (r *countingAppRepo) InsertApp(context.Context, store.InsertAppParameters) (string, error) {
	return "", nil
}
func (r *countingAppRepo) DeleteAppByID(context.Context, string) error             { return nil }
func (r *countingAppRepo) GetApps(context.Context) ([]config.AppDescriptor, error) { return nil, nil }
func (r *countingAppRepo) UpdateAppNameByID(context.Context, string, string) error { return nil }

// Just here to verify correct compilation of the mocked appRepo
var _ services.AppRepository = (*countingAppRepo)(nil)

func chain(handler *IngestHandler, repo services.AppRepository) http.Handler {
	router := mux.NewRouter()
	sub := router.PathPrefix("/observe/{APP_ID}").Subrouter()
	sub.Use(CachedAppResolverMiddleware(repo))
	sub.HandleFunc("/{PROJECT_ID}/v1/logs", handler.HandleLogs).Methods(http.MethodPost)
	return router
}

func post(h http.Handler, path, remote string) *httptest.ResponseRecorder {
	// A valid empty batch: these tests exercise the middleware chain, not the
	// decoder, so the handler must reach its 204 rather than 400 on the body.
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(`{}`)))
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestCachedAppResolverMemoizesLookup(t *testing.T) {
	resetObserveCache(t)
	repo := &countingAppRepo{}
	h := chain(NewIngestHandler(nil, nil, nil, nil), repo)

	for i := 0; i < 5; i++ {
		rec := post(h, "/observe/known-app/proj/v1/logs", "203.0.113.20:1")
		require.Equal(t, http.StatusNoContent, rec.Code)
	}
	// The five requests share one database read.
	require.Equal(t, int64(1), atomic.LoadInt64(&repo.calls))
}

func TestCachedAppResolverCachesUnknown(t *testing.T) {
	resetObserveCache(t)
	repo := &countingAppRepo{unknown: true}
	h := chain(NewIngestHandler(nil, nil, nil, nil), repo)

	for i := 0; i < 5; i++ {
		rec := post(h, "/observe/ghost-app/proj/v1/logs", "203.0.113.21:1")
		require.Equal(t, http.StatusNotFound, rec.Code)
	}
	// A flood of the same unknown id does not re-hit the database each time.
	require.Equal(t, int64(1), atomic.LoadInt64(&repo.calls))
}

func TestCachedAppResolverRejectsMalformedID(t *testing.T) {
	resetObserveCache(t)
	repo := &countingAppRepo{}
	h := chain(NewIngestHandler(nil, nil, nil, nil), repo)
	// A control character in the id fails the syntactic guard before any read.
	rec := post(h, "/observe/%00bad/proj/v1/logs", "203.0.113.22:1")
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Equal(t, int64(0), atomic.LoadInt64(&repo.calls))
}
