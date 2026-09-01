package handlers

import (
	"errors"
	"log"
	"net/http"
	"xprem/internal/compression"
	"xprem/internal/services"
	"xprem/internal/types"

	"github.com/google/uuid"
)

func (h *ExpoProtocolHandler) HandleAssets(w http.ResponseWriter, r *http.Request) {
	requestID := uuid.New().String()

	appId := resolveAppID(r)
	if appId == "" {
		log.Printf("[RequestID: %s] No app id provided", requestID)
		http.Error(w, "No app id provided", http.StatusBadRequest)
		return
	}

	channelName := r.Header.Get("expo-channel-name")
	platform, err := types.ParsePlatform(r.URL.Query().Get("platform"))
	if err != nil {
		log.Printf("[RequestID: %s] Invalid platform: %s", requestID, r.URL.Query().Get("platform"))
		http.Error(w, "Invalid platform", http.StatusBadRequest)
		return
	}

	params := services.AssetResolutionParams{
		RequestID:         requestID,
		AppID:             appId,
		ChannelName:       channelName,
		AssetName:         r.URL.Query().Get("asset"),
		RuntimeVersion:    r.URL.Query().Get("runtimeVersion"),
		Platform:          platform,
		ClientID:          r.Header.Get("EAS-Client-ID"),
		Branch:            r.URL.Query().Get("branch"),
		UpdateID:          r.URL.Query().Get("updateId"),
		RequestedUpdateID: r.Header.Get("Expo-Requested-Update-ID"),
		Hash:              r.URL.Query().Get("h"),
		Extension:         r.URL.Query().Get("ext"),
	}

	result, err := h.protocolService.ResolveAsset(r.Context(), params)
	if err != nil {
		if assetErr := (*services.ExpoAssetError)(nil); errors.As(err, &assetErr) {
			http.Error(w, assetErr.Message, assetErr.StatusCode)
			return
		}
		if protoErr := (*services.ExpoProtocolError)(nil); errors.As(err, &protoErr) {
			http.Error(w, protoErr.Message, protoErr.StatusCode)
			return
		}
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	if result.RedirectToURL != "" {
		http.Redirect(w, r, result.RedirectToURL, http.StatusFound)
		return
	}

	for key, value := range result.Headers {
		w.Header().Set(key, value)
	}

	if result.StatusCode != http.StatusOK {
		http.Error(w, string(result.Body), result.StatusCode)
		return
	}

	compression.ServeCompressedAsset(w, r, result.Body, result.ContentType, params.RequestID)
}
