// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"time"
	"xprem/config"
	"xprem/internal/cache"
	"xprem/internal/handlers"
	"xprem/internal/helpers"
	"xprem/internal/services"

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

// ingestLimitWindow is the fixed window both ingestion budgets are counted over.
const ingestLimitWindow = time.Minute

// IngestLimitMiddleware answers 429 once an address, or the whole app, has sent
// its budget of batches in the current minute. The SDK keeps a refused batch
// and sends it again after Retry-After. A limit of 0 disables that budget.
func IngestLimitMiddleware(perIP, perApp int) func(http.Handler) http.Handler {
	c := cache.GetCache()
	secret := []byte(config.GetEnv("JWT_SECRET"))
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			appID := mux.Vars(r)["APP_ID"]
			// The address is counted first: a throttled address never spends the app's budget.
			if ip := helpers.ClientIP(r); ip.IsValid() && overBudget(c, ingestIPKey(secret, appID, ip.String()), perIP) {
				refuseOverBudget(w)
				return
			}
			if overBudget(c, "observe:ingest_app:"+appID, perApp) {
				refuseOverBudget(w)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// overBudget counts one request and fails open when the cache is unreachable.
func overBudget(c cache.Cache, key string, limit int) bool {
	if limit <= 0 {
		return false
	}
	count, err := c.Incr(key, int(ingestLimitWindow.Seconds()))
	return err == nil && count > int64(limit)
}

// ingestIPKey hashes the address so the cache holds no IP.
func ingestIPKey(secret []byte, appID, ip string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(ip))
	return "observe:ingest_ip:" + appID + ":" + hex.EncodeToString(mac.Sum(nil)[:16])
}

func refuseOverBudget(w http.ResponseWriter) {
	observeBatch(resultThrottled)
	handlers.RenderThrottled(w, ingestLimitWindow)
}
