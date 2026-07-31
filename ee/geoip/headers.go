// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package geoip

import (
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
	"xprem/config"
)

// headerNames maps one provider's visitor-location headers.
type headerNames struct {
	country string
	city    string
	lat     string
	lng     string
}

func (n headerNames) isZero() bool {
	return n.country == "" && n.city == "" && n.lat == "" && n.lng == ""
}

// providerHeaderSets are the documented visitor-location headers, tried in
// order. The X-Geo-* set is the generic convention for proxies that let you
// name the headers yourself (nginx, Google Cloud LB, custom setups).
var providerHeaderSets = []headerNames{
	{country: "Cf-Ipcountry", city: "Cf-Ipcity", lat: "Cf-Iplatitude", lng: "Cf-Iplongitude"},
	{country: "Cloudfront-Viewer-Country", city: "Cloudfront-Viewer-City", lat: "Cloudfront-Viewer-Latitude", lng: "Cloudfront-Viewer-Longitude"},
	{country: "X-Vercel-Ip-Country", city: "X-Vercel-Ip-City", lat: "X-Vercel-Ip-Latitude", lng: "X-Vercel-Ip-Longitude"},
	{country: "X-Geo-Country", city: "X-Geo-City", lat: "X-Geo-Latitude", lng: "X-Geo-Longitude"},
}

// headerResolver reads the visitor-location headers a trusted proxy or CDN
// stamps on each request, so devices get located without any local GeoIP
// database. Known provider headers are recognized automatically; the
// GEOIP_HEADER_* variables replace the whole catalog when set.
type headerResolver struct {
	overrides headerNames
}

// newHeaderResolverFromEnv builds the resolver when
// TRUST_GEOIP_HEADERS=true, nil otherwise. Only enable it when the server is
// reachable exclusively through the proxy that adds the headers, since a
// direct client could otherwise forge its own location.
func newHeaderResolverFromEnv() *headerResolver {
	if config.GetEnv("TRUST_GEOIP_HEADERS") != "true" {
		return nil
	}
	return &headerResolver{
		overrides: headerNames{
			country: config.GetEnv("GEOIP_HEADER_COUNTRY"),
			city:    config.GetEnv("GEOIP_HEADER_CITY"),
			lat:     config.GetEnv("GEOIP_HEADER_LATITUDE"),
			lng:     config.GetEnv("GEOIP_HEADER_LONGITUDE"),
		},
	}
}

func (r *headerResolver) Resolve(req *http.Request) *Location {
	if r == nil || req == nil {
		return nil
	}
	if !r.overrides.isZero() {
		return geoFromHeaderSet(req.Header, r.overrides)
	}
	for _, set := range providerHeaderSets {
		if geo := geoFromHeaderSet(req.Header, set); geo != nil {
			return geo
		}
	}
	return geoFromAkamaiEdgescape(req.Header)
}

func geoFromHeaderSet(header http.Header, names headerNames) *Location {
	geo := &Location{}
	resolved := false
	if names.country != "" {
		if country := normalizeCountry(header.Get(names.country)); country != "" {
			geo.CountryCode = &country
			resolved = true
		}
	}
	if names.city != "" {
		if city := normalizeCity(header.Get(names.city)); city != "" {
			geo.City = &city
			resolved = true
		}
	}
	if names.lat != "" && names.lng != "" {
		if lat, lng, ok := parseLatLng(header.Get(names.lat), header.Get(names.lng)); ok {
			geo.Lat = &lat
			geo.Lng = &lng
			resolved = true
		}
	}
	if !resolved {
		return nil
	}
	return geo
}

// geoFromAkamaiEdgescape parses the single comma-separated key=value header
// Akamai stamps (country_code=FR,city=PARIS,lat=48.86,long=2.34,...).
func geoFromAkamaiEdgescape(header http.Header) *Location {
	raw := header.Get("X-Akamai-Edgescape")
	if raw == "" {
		return nil
	}
	fields := map[string]string{}
	for _, pair := range strings.Split(raw, ",") {
		key, value, found := strings.Cut(strings.TrimSpace(pair), "=")
		if found {
			fields[strings.ToLower(key)] = value
		}
	}
	geo := &Location{}
	resolved := false
	if country := normalizeCountry(fields["country_code"]); country != "" {
		geo.CountryCode = &country
		resolved = true
	}
	if city := normalizeCity(fields["city"]); city != "" {
		geo.City = &city
		resolved = true
	}
	if lat, lng, ok := parseLatLng(fields["lat"], fields["long"]); ok {
		geo.Lat = &lat
		geo.Lng = &lng
		resolved = true
	}
	if !resolved {
		return nil
	}
	return geo
}

// normalizeCountry keeps two-letter ISO codes, dropping the "unknown" markers
// providers use (Cloudflare's XX/T1, Vercel's ZZ).
func normalizeCountry(raw string) string {
	code := strings.ToUpper(strings.TrimSpace(raw))
	if len(code) != 2 || code == "XX" || code == "T1" || code == "ZZ" {
		return ""
	}
	return code
}

// normalizeCity undoes the percent-encoding some providers apply to city
// names (Vercel, CloudFront).
func normalizeCity(raw string) string {
	city := strings.TrimSpace(raw)
	if city == "" {
		return ""
	}
	if decoded, err := url.QueryUnescape(city); err == nil {
		city = strings.TrimSpace(decoded)
	}
	// QueryUnescape can emit invalid UTF-8, which Postgres rejects mid-transaction.
	if len(city) > 128 || !utf8.ValidString(city) {
		return ""
	}
	return city
}

func parseLatLng(rawLat, rawLng string) (float64, float64, bool) {
	lat, latErr := strconv.ParseFloat(strings.TrimSpace(rawLat), 64)
	lng, lngErr := strconv.ParseFloat(strings.TrimSpace(rawLng), 64)
	if latErr != nil || lngErr != nil {
		return 0, 0, false
	}
	// ParseFloat accepts NaN, which passes every range check and breaks JSON encoding downstream.
	if math.IsNaN(lat) || math.IsNaN(lng) || lat < -90 || lat > 90 || lng < -180 || lng > 180 {
		return 0, 0, false
	}
	// 0,0 (Null Island) means the provider has no location; treat it as absent.
	if lat == 0 && lng == 0 {
		return 0, 0, false
	}
	return lat, lng, true
}
