// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package geoip

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"expo-open-ota/config"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/oschwald/geoip2-golang"
)

const (
	maxmindEdition = "GeoLite2-City"
	maxmindBaseURL = "https://download.maxmind.com"
	// GeoLite2 is rebuilt twice a week.
	maxmindRefreshInterval = 72 * time.Hour
	// A failed attempt retries much sooner while no database is loaded yet.
	maxmindRetryInterval = 15 * time.Minute
	// The City database is ~60MB; anything past this is not a database.
	maxmindMaxArchiveSize = 512 << 20
)

// maxMindResolver keeps a GeoLite2 City database downloaded and fresh using
// MaxMind account credentials, so deployments do not have to mount an .mmdb
// file. Resolve answers nil until the first database is available; a failed
// download never blocks the server.
type maxMindResolver struct {
	accountID       string
	licenseKey      string
	dbPath          string
	baseURL         string
	client          *http.Client
	refreshInterval time.Duration
	retryInterval   time.Duration

	mu       sync.RWMutex
	resolver *mmdbReader

	stop     chan struct{}
	stopOnce sync.Once
	done     chan struct{}
}

// newMaxMindResolverFromEnv builds the resolver from MAXMIND_ACCOUNT_ID,
// MAXMIND_LICENSE_KEY and the optional GEOIP_CACHE_DIR. It returns nil when
// no credentials are configured; a half-set pair is a fatal misconfiguration.
func newMaxMindResolverFromEnv() *maxMindResolver {
	accountID, licenseKey := config.GetEnv("MAXMIND_ACCOUNT_ID"), config.GetEnv("MAXMIND_LICENSE_KEY")
	if accountID == "" && licenseKey == "" {
		return nil
	}
	if accountID == "" || licenseKey == "" {
		log.Fatalf("🚨 [IDENTITY] MAXMIND_ACCOUNT_ID and MAXMIND_LICENSE_KEY must be set together")
	}
	r := newMaxMindResolver(accountID, licenseKey, config.GetEnv("GEOIP_CACHE_DIR"))
	go r.run()
	return r
}

// newMaxMindResolver builds the resolver without starting the refresh loop,
// for tests that drive refresh directly.
func newMaxMindResolver(accountID, licenseKey, cacheDir string) *maxMindResolver {
	return &maxMindResolver{
		accountID:       accountID,
		licenseKey:      licenseKey,
		dbPath:          filepath.Join(resolveGeoipCacheDir(cacheDir), maxmindEdition+".mmdb"),
		baseURL:         maxmindBaseURL,
		client:          &http.Client{Timeout: 5 * time.Minute},
		refreshInterval: maxmindRefreshInterval,
		retryInterval:   maxmindRetryInterval,
		stop:            make(chan struct{}),
		done:            make(chan struct{}),
	}
}

func resolveGeoipCacheDir(configured string) string {
	if configured != "" {
		return configured
	}
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(dir, "expo-open-ota", "geoip")
	}
	return filepath.Join(os.TempDir(), "expo-open-ota-geoip")
}

func (r *maxMindResolver) loadFromDisk() {
	resolver, err := openMMDBReader(r.dbPath)
	if err != nil {
		return
	}
	r.swap(resolver)
}

func (r *maxMindResolver) run() {
	defer close(r.done)
	// A database cached by a previous run serves immediately, before any
	// network call.
	r.loadFromDisk()
	for {
		delay := r.refreshInterval
		if err := r.refresh(); err != nil {
			log.Printf("⚠️ [IDENTITY] GeoLite2 refresh failed: %v", err)
			if !r.loaded() {
				delay = r.retryInterval
			}
		}
		select {
		case <-r.stop:
			return
		case <-time.After(delay):
		}
	}
}

func (r *maxMindResolver) loaded() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.resolver != nil
}

func (r *maxMindResolver) swap(next *mmdbReader) {
	r.mu.Lock()
	old := r.resolver
	r.resolver = next
	// In-flight Resolve calls hold the read lock, so closing here cannot
	// race them.
	old.close()
	r.mu.Unlock()
}

// refresh downloads the database when MaxMind has a newer build than the
// cached file, verifies its checksum, and swaps it in.
func (r *maxMindResolver) refresh() error {
	req, err := r.downloadRequest("tar.gz")
	if err != nil {
		return err
	}
	if info, statErr := os.Stat(r.dbPath); statErr == nil {
		req.Header.Set("If-Modified-Since", info.ModTime().UTC().Format(http.TimeFormat))
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusNotModified:
		if !r.loaded() {
			r.loadFromDisk()
		}
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("MaxMind refused the credentials (%s); check MAXMIND_ACCOUNT_ID and MAXMIND_LICENSE_KEY", resp.Status)
	case http.StatusOK:
	default:
		return fmt.Errorf("MaxMind download failed: %s", resp.Status)
	}
	archive, err := io.ReadAll(io.LimitReader(resp.Body, maxmindMaxArchiveSize+1))
	if err != nil {
		return err
	}
	if len(archive) > maxmindMaxArchiveSize {
		return errors.New("MaxMind archive exceeds the size guard")
	}
	if err := r.verifyChecksum(archive); err != nil {
		return err
	}
	mmdb, err := extractMmdb(archive)
	if err != nil {
		return err
	}
	if err := r.writeAtomically(mmdb, resp.Header.Get("Last-Modified")); err != nil {
		return err
	}
	resolver, err := openMMDBReader(r.dbPath)
	if err != nil {
		return fmt.Errorf("downloaded GeoLite2 database is not usable: %w", err)
	}
	r.swap(resolver)
	return nil
}

func (r *maxMindResolver) downloadRequest(suffix string) (*http.Request, error) {
	url := fmt.Sprintf("%s/geoip/databases/%s/download?suffix=%s", r.baseURL, maxmindEdition, suffix)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(r.accountID, r.licenseKey)
	return req, nil
}

func (r *maxMindResolver) verifyChecksum(archive []byte) error {
	req, err := r.downloadRequest("tar.gz.sha256")
	if err != nil {
		return err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("MaxMind checksum download failed: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if err != nil {
		return err
	}
	// The file reads "<sha256>  <archive name>".
	fields := strings.Fields(string(body))
	if len(fields) == 0 {
		return errors.New("MaxMind checksum file is empty")
	}
	sum := sha256.Sum256(archive)
	if !strings.EqualFold(fields[0], hex.EncodeToString(sum[:])) {
		return errors.New("MaxMind archive does not match its published checksum")
	}
	return nil
}

// extractMmdb returns the .mmdb entry of the tar.gz archive MaxMind ships
// (the database sits inside a dated directory).
func extractMmdb(archive []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("reading MaxMind archive: %w", err)
	}
	defer func() { _ = gz.Close() }()
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading MaxMind archive: %w", err)
		}
		if header.Typeflag == tar.TypeReg && strings.HasSuffix(header.Name, ".mmdb") {
			return io.ReadAll(io.LimitReader(reader, maxmindMaxArchiveSize))
		}
	}
	return nil, errors.New("no .mmdb entry in the MaxMind archive")
}

func (r *maxMindResolver) writeAtomically(mmdb []byte, lastModified string) error {
	dir := filepath.Dir(r.dbPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, maxmindEdition+".*.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(mmdb); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), r.dbPath); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	// The file mtime feeds the next If-Modified-Since check.
	if t, err := http.ParseTime(lastModified); err == nil {
		_ = os.Chtimes(r.dbPath, t, t)
	}
	return nil
}

func (r *maxMindResolver) Resolve(req *http.Request) *Location {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.resolver == nil {
		return nil
	}
	return r.resolver.resolveLocationFromIP(clientIPString(req))
}

// Close stops the refresh loop and releases the loaded database.
func (r *maxMindResolver) Close() {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() { close(r.stop) })
	<-r.done
	r.mu.Lock()
	r.resolver.close()
	r.resolver = nil
	r.mu.Unlock()
}

// mmdbReader resolves IPs against a local MaxMind GeoLite2/GeoIP2 City
// database (mmdb file); it is the storage layer under maxMindResolver.
type mmdbReader struct {
	db *geoip2.Reader
}

func openMMDBReader(path string) (*mmdbReader, error) {
	db, err := geoip2.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening GeoLite2 database %q: %w", path, err)
	}
	// Open succeeds on any mmdb type; check for City/Country here so a wrong database fails loud at boot.
	if dbType := db.Metadata().DatabaseType; !strings.Contains(dbType, "City") && !strings.Contains(dbType, "Country") {
		_ = db.Close()
		return nil, fmt.Errorf("GeoLite2 database %q has type %q; a City or Country database is required", path, dbType)
	}
	log.Printf("🌍 [GEOIP] GeoLite2 database loaded from %s", path)
	return &mmdbReader{db: db}, nil
}

func (r *mmdbReader) close() {
	if r != nil && r.db != nil {
		_ = r.db.Close()
	}
}

func (r *mmdbReader) resolveLocationFromIP(ipStr string) *Location {
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

	location := &Location{}
	resolved := false
	if code := record.Country.IsoCode; code != "" {
		location.CountryCode = &code
		resolved = true
	}
	if city := record.City.Names["en"]; city != "" {
		location.City = &city
		resolved = true
	}
	// 0,0 (Null Island) means the database has no location; treat it as absent.
	if record.Location.Latitude != 0 || record.Location.Longitude != 0 {
		lat, lng := record.Location.Latitude, record.Location.Longitude
		location.Lat = &lat
		location.Lng = &lng
		resolved = true
	}
	if !resolved {
		return nil
	}
	return location
}
