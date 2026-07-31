// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package geoip

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
	"xprem/config"

	"github.com/oschwald/geoip2-golang"
)

const (
	maxmindEdition = "GeoLite2-City"
	maxmindBaseURL = "https://download.maxmind.com"
	// The City database is ~60MB; anything past this is not a database.
	maxmindMaxArchiveSize = 512 << 20
)

// maxMindResolver downloads a GeoLite2 City database once at startup using
// MaxMind account credentials, so deployments do not have to mount an .mmdb
// file. Resolve answers nil until the database is available; a failed
// download never blocks the server.
type maxMindResolver struct {
	accountID  string
	licenseKey string
	dbPath     string
	baseURL    string
	client     *http.Client
	cancel     context.CancelFunc
	done       chan struct{}

	mu       sync.RWMutex
	resolver *mmdbReader
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
		log.Fatalf("🚨 [GEOIP] MAXMIND_ACCOUNT_ID and MAXMIND_LICENSE_KEY must be set together")
	}
	r := newMaxMindResolver(accountID, licenseKey, config.GetEnv("GEOIP_CACHE_DIR"))
	r.start()
	return r
}

// newMaxMindResolver builds the resolver without starting the boot download,
// for tests that drive refresh directly.
func newMaxMindResolver(accountID, licenseKey, cacheDir string) *maxMindResolver {
	return &maxMindResolver{
		accountID:  accountID,
		licenseKey: licenseKey,
		dbPath:     filepath.Join(resolveGeoipCacheDir(cacheDir), maxmindEdition+".mmdb"),
		baseURL:    maxmindBaseURL,
		client:     &http.Client{Timeout: 5 * time.Minute},
	}
}

func resolveGeoipCacheDir(configured string) string {
	if configured != "" {
		return configured
	}
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(dir, "xprem", "geoip")
	}
	return filepath.Join(os.TempDir(), "xprem-geoip")
}

func (r *maxMindResolver) start() {
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.done = make(chan struct{})
	go r.download(ctx)
}

// download runs once at startup: a database cached by a previous run serves
// immediately, then the current MaxMind build replaces it if newer. There is
// no periodic sync; a restart picks up newer builds.
func (r *maxMindResolver) download(ctx context.Context) {
	defer close(r.done)
	r.loadFromDisk()
	if err := r.refresh(ctx); err != nil && ctx.Err() == nil {
		if r.loaded() {
			log.Printf("⚠️ [GEOIP] GeoLite2 download failed, serving the cached database: %v", err)
		} else {
			log.Printf("⚠️ [GEOIP] GeoLite2 download failed and no cached database exists; devices stay unlocated until the server restarts: %v", err)
		}
	}
}

func (r *maxMindResolver) loadFromDisk() {
	resolver, err := openMMDBReader(r.dbPath)
	if err != nil {
		return
	}
	log.Printf("🌍 [GEOIP] GeoLite2 database loaded from %s", r.dbPath)
	r.swap(resolver)
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
func (r *maxMindResolver) refresh(ctx context.Context) error {
	req, err := r.downloadRequest(ctx, "tar.gz")
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
		if !r.loaded() {
			return fmt.Errorf("MaxMind reports the cached database %s as current, but it cannot be loaded; delete it to force a fresh download", r.dbPath)
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
	if err := r.verifyChecksum(ctx, archive); err != nil {
		return err
	}
	mmdb, err := extractMmdb(archive)
	if err != nil {
		return err
	}
	return r.install(mmdb, resp.Header.Get("Last-Modified"))
}

// install validates the downloaded database before it replaces the cached
// file, so a bad build never destroys a good cache.
func (r *maxMindResolver) install(mmdb []byte, lastModified string) error {
	dir := filepath.Dir(r.dbPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, maxmindEdition+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(mmdb); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	reader, err := openMMDBReader(tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("downloaded GeoLite2 database is not usable: %w", err)
	}
	if err := os.Rename(tmpPath, r.dbPath); err != nil {
		reader.close()
		_ = os.Remove(tmpPath)
		return err
	}
	// The file mtime feeds the next If-Modified-Since check.
	if t, err := http.ParseTime(lastModified); err == nil {
		_ = os.Chtimes(r.dbPath, t, t)
	}
	log.Printf("🌍 [GEOIP] GeoLite2 database downloaded to %s", r.dbPath)
	r.swap(reader)
	return nil
}

func (r *maxMindResolver) downloadRequest(ctx context.Context, suffix string) (*http.Request, error) {
	url := fmt.Sprintf("%s/geoip/databases/%s/download?suffix=%s", r.baseURL, maxmindEdition, suffix)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(r.accountID, r.licenseKey)
	return req, nil
}

func (r *maxMindResolver) verifyChecksum(ctx context.Context, archive []byte) error {
	req, err := r.downloadRequest(ctx, "tar.gz.sha256")
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

// Close aborts any in-flight download and releases the loaded database.
func (r *maxMindResolver) Close() {
	if r == nil {
		return
	}
	if r.cancel != nil {
		r.cancel()
		<-r.done
	}
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
