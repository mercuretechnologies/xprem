package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xprem/config"
	cache2 "xprem/internal/cache"
	"xprem/internal/dashboard"
	"xprem/internal/services"
	"xprem/internal/store"

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
func (f *fakeAppRepo) UpdateAppGitURLByID(_ context.Context, _ string, _ string) error {
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

type mutableAppRepo struct {
	app config.AppConfig
}

func (f *mutableAppRepo) InsertApp(context.Context, store.InsertAppParameters) (string, error) {
	panic("unused")
}
func (f *mutableAppRepo) DeleteAppByID(context.Context, string) error { panic("unused") }
func (f *mutableAppRepo) GetApps(context.Context) ([]config.AppDescriptor, error) {
	return nil, nil
}
func (f *mutableAppRepo) UpdateAppNameByID(_ context.Context, _ string, name string) error {
	f.app.Name = name
	return nil
}
func (f *mutableAppRepo) UpdateAppGitURLByID(_ context.Context, _ string, gitURL string) error {
	f.app.GitURL = gitURL
	return nil
}
func (f *mutableAppRepo) GetAppByID(context.Context, string) (config.AppConfig, error) {
	return f.app, nil
}

func updateAppGitURL(t *testing.T, handler *AppHandler, app config.AppConfig, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPatch, "/api/apps/"+app.Id+"/git-url", strings.NewReader(body))
	request = request.WithContext(services.WithApp(request.Context(), app))
	handler.UpdateAppGitURLHandler(recorder, request)
	return recorder
}

func TestUpdateAppGitURLHandlerSavesOrClearsGitURL(t *testing.T) {
	repo := &mutableAppRepo{app: config.AppConfig{Id: "app-1", Name: "Mobile"}}
	handler := NewAppHandler(services.NewAppService(repo), nil)

	recorder := updateAppGitURL(t, handler, repo.app, `{"gitUrl":"https://github.com/acme/mobile.git/"}`)
	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())
	require.Equal(t, "https://github.com/acme/mobile", repo.app.GitURL)

	recorder = updateAppGitURL(t, handler, repo.app, `{"gitUrl":""}`)
	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())
	require.Empty(t, repo.app.GitURL)
}

func TestUpdateAppGitURLHandlerRejectsInvalidOrMissingGitURL(t *testing.T) {
	repo := &mutableAppRepo{app: config.AppConfig{Id: "app-1", Name: "Mobile"}}
	handler := NewAppHandler(services.NewAppService(repo), nil)

	for name, body := range map[string]string{
		"credentials": `{"gitUrl":"https://user:token@github.com/acme/mobile"}`,
		"missing":     `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			recorder := updateAppGitURL(t, handler, repo.app, body)
			require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
		})
	}
	require.Equal(t, "Mobile", repo.app.Name)
	require.Empty(t, repo.app.GitURL)
}
