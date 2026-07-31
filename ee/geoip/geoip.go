// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package geoip

import (
	"context"
	"net/http"

	"expo-open-ota/internal/helpers"
)

// Location is an optional, per-field-optional enrichment resolved from a
// request. A nil field never overwrites a previously known value.
type Location struct {
	CountryCode *string
	City        *string
	Lat         *float64
	Lng         *float64
}

// resolver turns a request into an optional Location. A nil result is a
// normal outcome, not an error.
type resolver interface {
	Resolve(r *http.Request) *Location
}

// clientIPString renders the request's client address the same way the
// identity registry keys on it, "" when it cannot be trusted or parsed.
func clientIPString(r *http.Request) string {
	if ip := helpers.ClientIP(r); ip.IsValid() {
		return ip.String()
	}
	return ""
}

type contextKey struct{}

// NewContext stamps a resolved Location on the context.
func NewContext(ctx context.Context, location *Location) context.Context {
	return context.WithValue(ctx, contextKey{}, location)
}

// FromContext reads back a stamped Location, nil when there is none.
func FromContext(ctx context.Context) *Location {
	location, _ := ctx.Value(contextKey{}).(*Location)
	return location
}
