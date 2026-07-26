// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package identity

import (
	"fmt"
	"log"
	"net"
	"strings"

	"github.com/oschwald/geoip2-golang"
)

// GeoResolver turns a request IP into an optional Geo enrichment. Resolvers
// must be nil-tolerant on the value they return: no resolution is a normal
// outcome (private IP, unknown range, no database configured), never an error
// worth failing an operation over.
type GeoResolver interface {
	Resolve(ip string) *Geo
}

// GeoLite2Resolver resolves against a local MaxMind GeoLite2/GeoIP2 City
// database (mmdb file). The operator downloads the database with their own
// MaxMind license key; without a configured file the feature is simply off.
// City-level accuracy: lat/lng is a city centroid, not a device position.
type GeoLite2Resolver struct {
	db *geoip2.Reader
}

func NewGeoLite2Resolver(path string) (*GeoLite2Resolver, error) {
	db, err := geoip2.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening GeoLite2 database %q: %w", path, err)
	}
	// Open succeeds on ANY valid mmdb (ASN, ISP...), after which every City()
	// lookup fails silently and the operator would see geo mysteriously never
	// resolve. Fail loud at boot instead.
	if dbType := db.Metadata().DatabaseType; !strings.Contains(dbType, "City") && !strings.Contains(dbType, "Country") {
		_ = db.Close()
		return nil, fmt.Errorf("GeoLite2 database %q has type %q; a City or Country database is required", path, dbType)
	}
	log.Printf("🌍 [IDENTITY] GeoLite2 database loaded from %s", path)
	return &GeoLite2Resolver{db: db}, nil
}

func (r *GeoLite2Resolver) Close() {
	if r != nil && r.db != nil {
		_ = r.db.Close()
	}
}

func (r *GeoLite2Resolver) Resolve(ipStr string) *Geo {
	ip := net.ParseIP(ipStr)
	if ip == nil || ip.IsPrivate() || ip.IsLoopback() || ip.IsUnspecified() {
		return nil
	}
	if r == nil || r.db == nil {
		return nil
	}
	record, err := r.db.City(ip)
	if err != nil {
		return nil
	}

	geo := &Geo{}
	resolved := false
	if code := record.Country.IsoCode; code != "" {
		geo.CountryCode = &code
		resolved = true
	}
	if city := record.City.Names["en"]; city != "" {
		geo.City = &city
		resolved = true
	}
	// 0,0 (Null Island) is what the database reports when it has no location;
	// treat it as absent rather than pinning devices in the Gulf of Guinea.
	if record.Location.Latitude != 0 || record.Location.Longitude != 0 {
		lat, lng := record.Location.Latitude, record.Location.Longitude
		geo.Lat = &lat
		geo.Lng = &lng
		resolved = true
	}
	if !resolved {
		return nil
	}
	return geo
}
