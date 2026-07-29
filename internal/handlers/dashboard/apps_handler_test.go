package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"expo-open-ota/config"
	cache2 "expo-open-ota/internal/cache"
	"expo-open-ota/internal/dashboard"
	"expo-open-ota/internal/services"
	"expo-open-ota/internal/store"

	"github.com/stretchr/testify/require"
)

// fakeAppRepo serves a fixed app list; only GetApps matters here.
type fakeAppRepo struct {
	apps []config.AppDescriptor
}

func (f *fakeAppRepo) InsertApp(_ context.Context, _ store.InsertAppParameters) (string, error) {
	panic("unused")
}
func (f *fakeAppRepo) DeleteAppByID(_ context.Context, _ string) error { panic("unused") }
func (f *fakeAppRepo) GetApps(_ context.Context) ([]config.AppDescriptor, error) {
	return f.apps, nil
}
func (f *fakeAppRepo) UpdateAppNameByID(_ context.Context, _ string, _ string) error {
	panic("unused")
}
func (f *fakeAppRepo) GetAppByID(_ context.Context, _ string) (config.AppConfig, error) {
	panic("unused")
}

func getApps(t *testing.T, handler *AppHandler) (int, []config.AppDescriptor) {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.GetAppsHandler(recorder, httptest.NewRequest(http.MethodGet, "/api/apps", nil))
	var apps []config.AppDescriptor
	if recorder.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &apps))
	}
	return recorder.Code, apps
}

// The cache stores the unfiltered list shared by every account, so the
// visibility filter must apply on the cache-hit path too, a member's request
// right after an admin warmed the cache is the regression this guards.
func TestGetAppsHandlerFiltersAfterCacheRead(t *testing.T) {
	cache2.GetCache().Delete(dashboard.ComputeGetAppsCacheKey())
	t.Cleanup(func() { cache2.GetCache().Delete(dashboard.ComputeGetAppsCacheKey()) })

	appService := services.NewAppService(&fakeAppRepo{apps: []config.AppDescriptor{
		{Id: "app-1", Name: "One"},
		{Id: "app-2", Name: "Two"},
	}})

	// First request, unrestricted (admin view): warms the cache with both apps.
	unrestricted := NewAppHandler(appService, func(context.Context, *services.DashboardPrincipal) (bool, map[string]bool, error) {
		return false, nil, nil
	})
	status, apps := getApps(t, unrestricted)
	require.Equal(t, http.StatusOK, status)
	require.Len(t, apps, 2)

	// Second request, restricted member: served from the warm cache, and
	// still only their app.
	restricted := NewAppHandler(appService, func(context.Context, *services.DashboardPrincipal) (bool, map[string]bool, error) {
		return true, map[string]bool{"app-2": true}, nil
	})
	status, apps = getApps(t, restricted)
	require.Equal(t, http.StatusOK, status)
	require.Len(t, apps, 1)
	require.Equal(t, "app-2", apps[0].Id)

	// A member with no grants gets an empty list, not an error.
	invisible := NewAppHandler(appService, func(context.Context, *services.DashboardPrincipal) (bool, map[string]bool, error) {
		return true, map[string]bool{}, nil
	})
	status, apps = getApps(t, invisible)
	require.Equal(t, http.StatusOK, status)
	require.Empty(t, apps)

	// No filter injected (community handler construction): everything shows.
	community := NewAppHandler(appService, nil)
	status, apps = getApps(t, community)
	require.Equal(t, http.StatusOK, status)
	require.Len(t, apps, 2)
}
