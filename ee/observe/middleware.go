// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"net/http"
	"xprem/config"
	"xprem/internal/cache"
	"xprem/internal/services"

	"github.com/gorilla/mux"
)

// The observe subrouter runs CachedAppResolverMiddleware before the handler.
// (Request rate limiting is intentionally not implemented yet: per-IP would
// punish the shared-NAT / carrier-CGNAT case that dominates mobile, and the
// right shape is operator-configurable, so it is deferred. Hard limits belong
// at the edge in the meantime; the store's own per-device bounds still apply.)

// appExistenceTTLSeconds bounds how long a known/unknown app id is trusted
// from cache. Short enough that a freshly created or deleted app converges
// quickly, long enough that a device fleet backgrounding in unison collapses
// to one database read per app per window instead of one per request.
const appExistenceTTLSeconds = 60

const (
	appKnownCacheValue   = "1"
	appUnknownCacheValue = "0"
)

// CachedAppResolverMiddleware validates {APP_ID} against the registry like the
// generic AppResolverMiddleware, but memoizes the lookup so telemetry (which
// fires on every app-background of every device) does not issue an uncached
// primary-key query per request. Both outcomes are cached: a valid id skips
// the query for the window, an invalid id is short-circuited to 404 without
// re-hitting the database under a flood of the same bad id.
func CachedAppResolverMiddleware(appRepo services.AppRepository) func(http.Handler) http.Handler {
	c := cache.GetCache()
	ttl := appExistenceTTLSeconds
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			appID := mux.Vars(r)["APP_ID"]
			if !isValidAppID(appID) {
				// 404, not 400: for the SDK both are permanent (batch dropped),
				// and 404 matches the manifest/asset edge for unknown ids.
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

// isValidAppID uses the same syntactic guard as the generic
// AppResolverMiddleware (internal/middleware delegates to this too): the
// observe {APP_ID} is the same internal app id and must obey the same rules.
// We call config directly rather than import internal/middleware, which would
// pull the whole handler graph into ee/observe.
func isValidAppID(id string) bool {
	return config.ValidateAppId(id, "appId") == nil
}
