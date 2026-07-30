package handlers

import (
	"context"
	"encoding/json"
	"expo-open-ota/config"
	"expo-open-ota/internal/cdn"
	"expo-open-ota/internal/handlers"
	"expo-open-ota/internal/helpers"
	"expo-open-ota/internal/providers/expo"
	"expo-open-ota/internal/services"
	"expo-open-ota/internal/version"
	"net/http"
)

type SettingsHandler struct {
	appService *services.AppService
	// ssoEnabled reports whether enterprise SSO is currently active; injected
	// from the wiring so this community handler needs no ee import. Nil-safe
	// for tests that build the handler directly.
	ssoEnabled func(context.Context) bool
	// visibleApps filters the embedded app list the same way the app listing
	// endpoint does; same injection story, same nil-safety.
	visibleApps AppVisibilityFilter
}

func NewSettingsHandler(appService *services.AppService, ssoEnabled func(context.Context) bool, visibleApps AppVisibilityFilter) *SettingsHandler {
	return &SettingsHandler{
		appService:  appService,
		ssoEnabled:  ssoEnabled,
		visibleApps: visibleApps,
	}
}

type SettingsEnv struct {
	BASE_URL                   string `json:"BASE_URL"`
	SERVER_VERSION             string `json:"SERVER_VERSION"`
	CONTROL_PLANE_ENABLED      bool   `json:"CONTROL_PLANE_ENABLED"`
	CACHE_MODE                 string `json:"CACHE_MODE"`
	REDIS_HOST                 string `json:"REDIS_HOST"`
	REDIS_PORT                 string `json:"REDIS_PORT"`
	REDIS_SENTINEL_ADDRS       string `json:"REDIS_SENTINEL_ADDRS"`
	REDIS_SENTINEL_MASTER_NAME string `json:"REDIS_SENTINEL_MASTER_NAME"`
	STORAGE_MODE               string `json:"STORAGE_MODE"`
	S3_BUCKET_NAME             string `json:"S3_BUCKET_NAME"`
	// CDN_BASE_URL is the resolved generic CDN base URL, whether it was
	// configured through CDN_BASE_URL or the deprecated S3_CDN_PREFIX.
	CDN_BASE_URL                           string `json:"CDN_BASE_URL"`
	GCS_BUCKET_NAME                        string `json:"GCS_BUCKET_NAME"`
	AZURE_BLOB_CONTAINER_NAME              string `json:"AZURE_BLOB_CONTAINER_NAME"`
	AZURE_STORAGE_ACCOUNT_NAME             string `json:"AZURE_STORAGE_ACCOUNT_NAME"`
	LOCAL_BUCKET_BASE_PATH                 string `json:"LOCAL_BUCKET_BASE_PATH"`
	AWS_REGION                             string `json:"AWS_REGION"`
	AWS_BASE_ENDPOINT                      string `json:"AWS_BASE_ENDPOINT"`
	AWS_S3_FORCE_PATH_STYLE                string `json:"AWS_S3_FORCE_PATH_STYLE"`
	AWS_ACCESS_KEY_ID                      string `json:"AWS_ACCESS_KEY_ID"`
	CLOUDFRONT_DOMAIN                      string `json:"CLOUDFRONT_DOMAIN"`
	CLOUDFRONT_KEY_PAIR_ID                 string `json:"CLOUDFRONT_KEY_PAIR_ID"`
	PRIVATE_CLOUDFRONT_KEY_B64             string `json:"PRIVATE_CLOUDFRONT_KEY_B64"`
	AWSSM_CLOUDFRONT_PRIVATE_KEY_SECRET_ID string `json:"AWSSM_CLOUDFRONT_PRIVATE_KEY_SECRET_ID"`
	PRIVATE_CLOUDFRONT_KEY_PATH            string `json:"PRIVATE_CLOUDFRONT_KEY_PATH"`
	PROMETHEUS_ENABLED                     string `json:"PROMETHEUS_ENABLED"`
	// CDN_TYPE is the CDN the server actually resolved at boot ("cloudfront",
	// "gcs-direct", "azure-direct", "generic" or "" when assets are served
	// directly), so the dashboard can display the effective setup instead of
	// making the user re-derive it from raw variables.
	CDN_TYPE string `json:"CDN_TYPE"`
	// EXPO_ACCOUNT_USERNAME is the Expo account behind the configured access
	// token. Only resolved in stateless mode (single app, single token) and
	// best-effort: empty when the token is missing or invalid.
	EXPO_ACCOUNT_USERNAME string `json:"EXPO_ACCOUNT_USERNAME"`
	// SSO_ENABLED reports whether enterprise SSO is active right now
	// (configured, enabled and licensed), so the dashboard can adapt the
	// account-management UI. Not an env var: the config lives in the database.
	SSO_ENABLED bool `json:"SSO_ENABLED"`
	// Apps lists the configured apps, the single flat-env app in stateless
	// mode, or every app in the database in control-plane mode. Each entry
	// carries just the id and optional display name, tokens and keys are
	// never surfaced here because this endpoint is read by the dashboard UI.
	Apps []config.AppDescriptor `json:"APPS"`
}

func (h *SettingsHandler) GetSettingsHandler(w http.ResponseWriter, r *http.Request) {
	apps, err := h.appService.GetApps(r.Context())
	if err != nil {
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred while fetching app settings")
		return
	}
	expoAccountUsername := ""
	if !config.IsDBMode() && len(apps) > 0 {
		expoAccountUsername = expo.FetchSelfUsername(apps[0].Id)
	}
	// Filter after the stateless expo lookup above: that one needs any app id
	// and only runs without a control plane, where nothing is ever filtered.
	if h.visibleApps != nil {
		restricted, visible, err := h.visibleApps(r.Context(), services.PrincipalFromContext(r.Context()))
		if err != nil {
			handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred while fetching app settings")
			return
		}
		if restricted {
			filtered := make([]config.AppDescriptor, 0, len(apps))
			for _, app := range apps {
				if visible[app.Id] {
					filtered = append(filtered, app)
				}
			}
			apps = filtered
		}
	}
	marshaledResponse, _ := json.Marshal(SettingsEnv{
		BASE_URL:                               config.GetEnv("BASE_URL"),
		SERVER_VERSION:                         version.Version,
		CONTROL_PLANE_ENABLED:                  config.IsDBMode(),
		CACHE_MODE:                             config.GetEnv("CACHE_MODE"),
		REDIS_HOST:                             config.GetEnv("REDIS_HOST"),
		REDIS_PORT:                             config.GetEnv("REDIS_PORT"),
		REDIS_SENTINEL_ADDRS:                   config.GetEnv("REDIS_SENTINEL_ADDRS"),
		REDIS_SENTINEL_MASTER_NAME:             config.GetEnv("REDIS_SENTINEL_MASTER_NAME"),
		STORAGE_MODE:                           config.GetEnv("STORAGE_MODE"),
		S3_BUCKET_NAME:                         config.GetEnv("S3_BUCKET_NAME"),
		CDN_BASE_URL:                           cdn.ResolveCDNBaseURL(),
		GCS_BUCKET_NAME:                        config.GetEnv("GCS_BUCKET_NAME"),
		AZURE_BLOB_CONTAINER_NAME:              config.GetEnv("AZURE_BLOB_CONTAINER_NAME"),
		AZURE_STORAGE_ACCOUNT_NAME:             config.GetEnv("AZURE_STORAGE_ACCOUNT_NAME"),
		LOCAL_BUCKET_BASE_PATH:                 config.GetEnv("LOCAL_BUCKET_BASE_PATH"),
		AWS_REGION:                             config.GetEnv("AWS_REGION"),
		AWS_BASE_ENDPOINT:                      config.GetEnv("AWS_BASE_ENDPOINT"),
		AWS_S3_FORCE_PATH_STYLE:                config.GetEnv("AWS_S3_FORCE_PATH_STYLE"),
		AWS_ACCESS_KEY_ID:                      helpers.MaskSecret(config.GetEnv("AWS_ACCESS_KEY_ID")),
		CLOUDFRONT_DOMAIN:                      config.GetEnv("CLOUDFRONT_DOMAIN"),
		CLOUDFRONT_KEY_PAIR_ID:                 helpers.MaskSecret(config.GetEnv("CLOUDFRONT_KEY_PAIR_ID")),
		PRIVATE_CLOUDFRONT_KEY_B64:             helpers.MaskSecret(config.GetEnv("PRIVATE_CLOUDFRONT_KEY_B64")),
		AWSSM_CLOUDFRONT_PRIVATE_KEY_SECRET_ID: config.GetEnv("AWSSM_CLOUDFRONT_PRIVATE_KEY_SECRET_ID"),
		PRIVATE_CLOUDFRONT_KEY_PATH:            config.GetEnv("PRIVATE_CLOUDFRONT_KEY_PATH"),
		PROMETHEUS_ENABLED:                     config.GetEnv("PROMETHEUS_ENABLED"),
		CDN_TYPE:                               cdn.ResolvedType(),
		EXPO_ACCOUNT_USERNAME:                  expoAccountUsername,
		SSO_ENABLED:                            h.ssoEnabled != nil && h.ssoEnabled(r.Context()),
		Apps:                                   apps,
	})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(marshaledResponse)

}
