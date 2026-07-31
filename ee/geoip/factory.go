// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package geoip

import (
	"net/http"
	"sync"
)

var (
	resolverInstance resolver
	resolverOnce     sync.Once
)

// getResolver picks the geo backend the environment selects: the visitor
// headers of a trusted proxy (TRUST_GEOIP_HEADERS), a managed MaxMind
// download (MAXMIND_ACCOUNT_ID and MAXMIND_LICENSE_KEY), or nil when devices
// stay unlocated.
func getResolver() resolver {
	resolverOnce.Do(func() {
		if resolver := newHeaderResolverFromEnv(); resolver != nil {
			resolverInstance = resolver
			return
		}
		if resolver := newMaxMindResolverFromEnv(); resolver != nil {
			resolverInstance = resolver
		}
	})
	return resolverInstance
}

// GetMiddleware stamps each request's resolved Geo on its context, where
// every identity write reads it back; nil when no resolver is configured.
func GetMiddleware() func(http.Handler) http.Handler {
	resolver := getResolver()
	if resolver == nil {
		return nil
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if location := resolver.Resolve(r); location != nil {
				r = r.WithContext(NewContext(r.Context(), location))
			}
			next.ServeHTTP(w, r)
		})
	}
}

// CloseResolver releases the selected resolver; safe when nothing was
// selected.
func CloseResolver() {
	if resolver, ok := resolverInstance.(*maxMindResolver); ok {
		resolver.Close()
	}
}

func ResetResolverInstance() {
	CloseResolver()
	resolverInstance = nil
	resolverOnce = sync.Once{}
}
