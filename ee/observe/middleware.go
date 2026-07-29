// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"expo-open-ota/config"
	"expo-open-ota/internal/cache"
	"expo-open-ota/internal/services"
	"net/http"

	"github.com/gorilla/mux"
)

// appExistenceTTLSeconds bounds how long a known/unknown app id is trusted from cache.
const appExistenceTTLSeconds = 60

const (
	appKnownCacheValue   = "1"
	appUnknownCacheValue = "0"
)

// CachedAppResolverMiddleware validates {APP_ID} against the registry like
// AppResolverMiddleware, but memoizes both outcomes so a flood of requests
// does not issue an uncached query per request.
func CachedAppResolverMiddleware(appRepo services.AppRepository) func(http.Handler) http.Handler {
	c := cache.GetCache()
	ttl := appExistenceTTLSeconds
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			appID := mux.Vars(r)["APP_ID"]
			if !isValidAppID(appID) {
				// 404, not 400: matches the manifest/asset edge for unknown ids.
				w.WriteHeader(http.StatusNotFound)
				return
			}

			cacheKey := "observe:app_exists:" + appID
			switch c.Get(cacheKey) {
			case appKnownCacheValue:
				next.ServeHTTP(w, r)
				return
			case appUnknownCacheValue:
				w.WriteHeader(http.StatusNotFound)
				return
			}

			if _, err := appRepo.GetAppByID(r.Context(), appID); err != nil {
				_ = c.Set(cacheKey, appUnknownCacheValue, &ttl)
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = c.Set(cacheKey, appKnownCacheValue, &ttl)
			next.ServeHTTP(w, r)
		})
	}
}

// isValidAppID applies the same syntactic guard as AppResolverMiddleware.
func isValidAppID(id string) bool {
	return config.ValidateAppId(id, "appId") == nil
}
