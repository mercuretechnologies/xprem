// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package geoip

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func requestWithHeaders(pairs map[string]string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/manifest", nil)
	for name, value := range pairs {
		req.Header.Set(name, value)
	}
	return req
}

func TestHeaderResolverRecognizesProviders(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
		country string
		city    string
		lat     float64
	}{
		{
			name: "cloudflare",
			headers: map[string]string{
				"CF-IPCountry":   "FR",
				"CF-IPCity":      "Paris",
				"CF-IPLatitude":  "48.8566",
				"CF-IPLongitude": "2.3522",
			},
			country: "FR", city: "Paris", lat: 48.8566,
		},
		{
			name: "cloudfront",
			headers: map[string]string{
				"CloudFront-Viewer-Country":   "DE",
				"CloudFront-Viewer-City":      "Berlin",
				"CloudFront-Viewer-Latitude":  "52.52",
				"CloudFront-Viewer-Longitude": "13.40",
			},
			country: "DE", city: "Berlin", lat: 52.52,
		},
		{
			name: "vercel with encoded city",
			headers: map[string]string{
				"X-Vercel-IP-Country": "BR",
				"X-Vercel-IP-City":    "S%C3%A3o%20Paulo",
			},
			country: "BR", city: "São Paulo",
		},
		{
			name: "generic set",
			headers: map[string]string{
				"X-Geo-Country": "jp",
				"X-Geo-City":    "Tokyo",
			},
			country: "JP", city: "Tokyo",
		},
		{
			name: "akamai edgescape",
			headers: map[string]string{
				"X-Akamai-Edgescape": "georegion=250,country_code=ES,city=MADRID,lat=40.41,long=-3.70",
			},
			country: "ES", city: "MADRID", lat: 40.41,
		},
	}
	resolver := &headerResolver{}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			location := resolver.Resolve(requestWithHeaders(testCase.headers))
			if location == nil {
				t.Fatal("expected a resolved location")
			}
			if testCase.country != "" && (location.CountryCode == nil || *location.CountryCode != testCase.country) {
				t.Fatalf("country = %v, want %q", location.CountryCode, testCase.country)
			}
			if testCase.city != "" && (location.City == nil || *location.City != testCase.city) {
				t.Fatalf("city = %v, want %q", location.City, testCase.city)
			}
			if testCase.lat != 0 && (location.Lat == nil || *location.Lat != testCase.lat) {
				t.Fatalf("lat = %v, want %v", location.Lat, testCase.lat)
			}
		})
	}
}

func TestHeaderResolverFiltersUnknownMarkersAndNullIsland(t *testing.T) {
	resolver := &headerResolver{}
	if location := resolver.Resolve(requestWithHeaders(map[string]string{"CF-IPCountry": "XX"})); location != nil {
		t.Fatalf("XX is Cloudflare's unknown marker, got %#v", location)
	}
	if location := resolver.Resolve(requestWithHeaders(map[string]string{
		"X-Geo-Latitude":  "0",
		"X-Geo-Longitude": "0",
	})); location != nil {
		t.Fatalf("0,0 means no location, got %#v", location)
	}
	if location := resolver.Resolve(requestWithHeaders(map[string]string{"X-Geo-Latitude": "48.8"})); location != nil {
		t.Fatalf("a latitude without a longitude is not a location, got %#v", location)
	}
}

// Forged or corrupt header values must never resolve: NaN/Inf coordinates
// break JSON encoding and invalid UTF-8 cities break the Postgres writes.
func TestHeaderResolverRejectsGarbageValues(t *testing.T) {
	resolver := &headerResolver{}
	cases := []map[string]string{
		{"X-Geo-Latitude": "NaN", "X-Geo-Longitude": "2.35"},
		{"X-Geo-Latitude": "48.8", "X-Geo-Longitude": "Inf"},
		{"X-Geo-Latitude": "91", "X-Geo-Longitude": "2.35"},
		{"X-Geo-Latitude": "48.8", "X-Geo-Longitude": "-181"},
		{"X-Geo-City": "%FF"},
		{"X-Geo-City": strings.Repeat("a", 300)},
	}
	for _, headers := range cases {
		if location := resolver.Resolve(requestWithHeaders(headers)); location != nil {
			t.Fatalf("headers %v must not resolve, got %#v", headers, location)
		}
	}
}

// Explicit GEOIP_HEADER_* names replace the whole catalog: a Cloudflare
// header must be ignored once overrides are configured.
func TestHeaderResolverOverridesReplaceTheCatalog(t *testing.T) {
	resolver := &headerResolver{overrides: headerNames{country: "X-My-Country"}}
	location := resolver.Resolve(requestWithHeaders(map[string]string{
		"CF-IPCountry": "FR",
		"X-My-Country": "IT",
	}))
	if location == nil || location.CountryCode == nil || *location.CountryCode != "IT" {
		t.Fatalf("expected the override header to win, got %#v", location)
	}
}

func TestGetResolverSelectsFromEnv(t *testing.T) {
	ResetResolverInstance()
	t.Cleanup(ResetResolverInstance)

	if resolver := getResolver(); resolver != nil {
		t.Fatalf("expected no resolver without any geo configuration, got %T", resolver)
	}
	if middleware := GetMiddleware(); middleware != nil {
		t.Fatal("expected no middleware without any geo configuration")
	}

	ResetResolverInstance()
	t.Setenv("TRUST_GEOIP_HEADERS", "true")
	if _, ok := getResolver().(*headerResolver); !ok {
		t.Fatalf("expected the header resolver, got %T", getResolver())
	}
	if middleware := GetMiddleware(); middleware == nil {
		t.Fatal("expected a middleware once a resolver is selected")
	}
}

// The middleware stamps the resolved location on the context, where the
// identity writes read it back.
func TestGetMiddlewareStampsTheContext(t *testing.T) {
	ResetResolverInstance()
	t.Cleanup(ResetResolverInstance)
	t.Setenv("TRUST_GEOIP_HEADERS", "true")

	var seen *Location
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = FromContext(r.Context())
	})
	req := requestWithHeaders(map[string]string{"CF-IPCountry": "CH"})
	GetMiddleware()(next).ServeHTTP(httptest.NewRecorder(), req)

	if seen == nil || seen.CountryCode == nil || *seen.CountryCode != "CH" {
		t.Fatalf("expected the stamped location, got %#v", seen)
	}
}

func TestNewHeaderResolverFromEnv(t *testing.T) {
	if resolver := newHeaderResolverFromEnv(); resolver != nil {
		t.Fatal("expected nil without TRUST_GEOIP_HEADERS")
	}
	t.Setenv("TRUST_GEOIP_HEADERS", "true")
	t.Setenv("GEOIP_HEADER_COUNTRY", "X-My-Country")
	resolver := newHeaderResolverFromEnv()
	if resolver == nil {
		t.Fatal("expected a resolver with TRUST_GEOIP_HEADERS=true")
	}
	if resolver.overrides.country != "X-My-Country" {
		t.Fatalf("expected the override to be read, got %#v", resolver.overrides)
	}
}
