package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"xprem/config"
	cache2 "xprem/internal/cache"
	"xprem/internal/dashboard"
	"xprem/internal/handlers"
	"xprem/internal/services"
	"xprem/internal/store"
	"xprem/internal/validation"
)

// AppVisibilityFilter narrows the dashboard app list to what the requesting
// account may see. Injected from the wiring (ee/rbac) so this community
// handler needs no ee import; nil-safe for tests that build the handler
// directly. restricted=false means nothing is filtered (admin, community
// fallback); otherwise only the ids in visible are.
type AppVisibilityFilter func(ctx context.Context, principal *services.DashboardPrincipal) (restricted bool, visible map[string]bool, err error)

type AppHandler struct {
	appService *services.AppService
	// visibleApps filters the responses of the app listing; the cache keeps
	// the unfiltered list (keyed per app set, not per user), so filtering
	// always happens after the cache read.
	visibleApps AppVisibilityFilter
}

func NewAppHandler(appService *services.AppService, visibleApps AppVisibilityFilter) *AppHandler {
	return &AppHandler{
		appService:  appService,
		visibleApps: visibleApps,
	}
}

// filterVisibleApps applies the visibility filter for this request. The
// returned error means the filter itself failed and the caller must 500
// rather than fall back to the unfiltered list.
func (h *AppHandler) filterVisibleApps(r *http.Request, apps []config.AppDescriptor) ([]config.AppDescriptor, error) {
	if h.visibleApps == nil {
		return apps, nil
	}
	restricted, visible, err := h.visibleApps(r.Context(), services.PrincipalFromContext(r.Context()))
	if err != nil {
		return nil, err
	}
	if !restricted {
		return apps, nil
	}
	filtered := make([]config.AppDescriptor, 0, len(apps))
	for _, app := range apps {
		if visible[app.Id] {
			filtered = append(filtered, app)
		}
	}
	return filtered, nil
}

func (h *AppHandler) CreateAppHandler(w http.ResponseWriter, r *http.Request) {
	var requestBody struct {
		Name       string            `json:"name"`
		KeysConfig config.KeysConfig `json:"keysConfig"`
	}
	if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	if requestBody.Name == "" {
		handlers.RenderError(w, http.StatusBadRequest, "Display name is empty")
		return
	}
	appId, err := h.appService.CreateApp(r.Context(), requestBody.Name, requestBody.KeysConfig)
	if err != nil {
		var valErr *validation.Error
		if errors.As(err, &valErr) {
			handlers.RenderError(w, http.StatusBadRequest, valErr.Error())
			return
		}
		if alreadyExistsErr := (*store.ErrResourceAlreadyExists)(nil); errors.As(err, &alreadyExistsErr) {
			handlers.RenderError(w, http.StatusConflict, alreadyExistsErr.Error())
			return
		}
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred while creating the app.")
		return
	}

	marshaledResponse, _ := json.Marshal(map[string]interface{}{
		"appId": appId,
	})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	w.Write(marshaledResponse)

	cache := cache2.GetCache()
	appsCacheKey := dashboard.ComputeGetAppsCacheKey()
	cache.Delete(appsCacheKey)
}

func (h *AppHandler) GetAppHandler(w http.ResponseWriter, r *http.Request) {
	app := services.AppFromContext(r.Context())
	if app == nil {
		handlers.RenderError(w, http.StatusInternalServerError, "app not resolved for this route")
		return
	}
	cache := cache2.GetCache()
	cacheKey := dashboard.ComputeGetAppCacheKey(app.Id)
	if cacheValue := cache.Get(cacheKey); cacheValue != "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(cacheValue))
		return
	}

	marshaledResponse, _ := json.Marshal(h.appService.PresentApp(r.Context(), *app))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(marshaledResponse)

	ttl := 3600
	cache.Set(cacheKey, string(marshaledResponse), &ttl)
}

func (h *AppHandler) DeleteAppHandler(w http.ResponseWriter, r *http.Request) {
	app := services.AppFromContext(r.Context())
	if app == nil {
		handlers.RenderError(w, http.StatusInternalServerError, "app not resolved for this route")
		return
	}
	if err := h.appService.DeleteApp(r.Context(), *app); err != nil {
		notFoundErr := (*store.ErrResourceNotFound)(nil)
		if !errors.As(err, &notFoundErr) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred while deleting the app.")
		return
	}
	w.WriteHeader(http.StatusNoContent)

	cache := cache2.GetCache()
	appsCacheKey := dashboard.ComputeGetAppsCacheKey()
	appCacheKey := dashboard.ComputeGetAppCacheKey(app.Id)
	cache.Delete(appCacheKey)
	cache.Delete(appsCacheKey)
}

func (h *AppHandler) GetAppsHandler(w http.ResponseWriter, r *http.Request) {
	cache := cache2.GetCache()
	cacheKey := dashboard.ComputeGetAppsCacheKey()
	// The cache holds the unfiltered list: entries are shared across
	// accounts, so per-user visibility is applied after the read, never
	// baked into the key or the cached value.
	var apps []config.AppDescriptor
	cachedValue := cache.Get(cacheKey)
	if cachedValue == "" || json.Unmarshal([]byte(cachedValue), &apps) != nil {
		var err error
		apps, err = h.appService.GetApps(r.Context())
		if err != nil {
			handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred while fetching apps.")
			return
		}
		marshaledFullList, _ := json.Marshal(apps)
		ttl := 3600
		cache.Set(cacheKey, string(marshaledFullList), &ttl)
	}

	visibleAppsList, err := h.filterVisibleApps(r, apps)
	if err != nil {
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred while fetching apps.")
		return
	}
	marshaledResponse, _ := json.Marshal(visibleAppsList)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(marshaledResponse)
}

func (h *AppHandler) UpdateAppHandler(w http.ResponseWriter, r *http.Request) {
	app := services.AppFromContext(r.Context())
	if app == nil {
		handlers.RenderError(w, http.StatusInternalServerError, "app not resolved for this route")
		return
	}

	var requestBody struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	if requestBody.Name == "" {
		handlers.RenderError(w, http.StatusBadRequest, "Display name cannot be empty")
		return
	}

	err := h.appService.UpdateApp(r.Context(), *app, requestBody.Name)
	if err != nil {
		var valErr *validation.Error
		if errors.As(err, &valErr) {
			handlers.RenderError(w, http.StatusBadRequest, valErr.Error())
			return
		}
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred while updating the app.")
		return
	}

	cache := cache2.GetCache()
	appsCacheKey := dashboard.ComputeGetAppsCacheKey()
	appCacheKey := dashboard.ComputeGetAppCacheKey(app.Id)
	cache.Delete(appsCacheKey)
	cache.Delete(appCacheKey)

	w.WriteHeader(http.StatusNoContent)
}

func (h *AppHandler) DownloadAppCertificateHandler(w http.ResponseWriter, r *http.Request) {
	app := services.AppFromContext(r.Context())
	if app == nil {
		handlers.RenderError(w, http.StatusInternalServerError, "app not resolved for this route")
		return
	}

	pemCertificateString, err := h.appService.CertificateFor(r.Context(), *app)
	if err != nil {
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred while downloading the app certificate.")
		return
	}

	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Content-Disposition", `attachment; filename="certificate.pem"`)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(pemCertificateString)))
	w.Header().Set("Cache-Control", "private, no-cache, no-store")

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(pemCertificateString))
}
