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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func buildArchive(t *testing.T, entryName string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if entryName != "" {
		if err := tw.WriteHeader(&tar.Header{
			Name:     entryName,
			Typeflag: tar.TypeReg,
			Mode:     0o644,
			Size:     int64(len(content)),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type maxmindFixture struct {
	archive       []byte
	checksum      string
	status        int
	gotAuth       string
	gotIfModified string
}

func newMaxMindServer(t *testing.T, fixture *maxmindFixture) *httptest.Server {
	t.Helper()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/geoip/databases/"+maxmindEdition+"/download" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		fixture.gotAuth = r.Header.Get("Authorization")
		switch r.URL.Query().Get("suffix") {
		case "tar.gz":
			fixture.gotIfModified = r.Header.Get("If-Modified-Since")
			if fixture.status != 0 {
				w.WriteHeader(fixture.status)
				return
			}
			w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
			_, _ = w.Write(fixture.archive)
		case "tar.gz.sha256":
			_, _ = w.Write([]byte(fixture.checksum + "  " + maxmindEdition + "_20260101.tar.gz\n"))
		default:
			t.Fatalf("unexpected suffix %q", r.URL.Query().Get("suffix"))
		}
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func testResolver(t *testing.T, server *httptest.Server) *maxMindResolver {
	t.Helper()
	resolver := newMaxMindResolver("12345", "license", t.TempDir())
	resolver.baseURL = server.URL
	resolver.client = server.Client()
	return resolver
}

func checksumOf(archive []byte) string {
	sum := sha256.Sum256(archive)
	return hex.EncodeToString(sum[:])
}

// The full pipeline runs: auth is sent, the checksum is verified, the .mmdb
// entry is validated. The payload is not a real database, so it must be
// rejected WITHOUT replacing the cached file, and the resolver stays unloaded.
func TestMaxMindRefreshRejectsUnusableDatabaseWithoutInstalling(t *testing.T) {
	archive := buildArchive(t, maxmindEdition+"_20260101/"+maxmindEdition+".mmdb", []byte("not-a-real-mmdb"))
	fixture := &maxmindFixture{archive: archive, checksum: checksumOf(archive)}
	resolver := testResolver(t, newMaxMindServer(t, fixture))
	if err := os.MkdirAll(filepath.Dir(resolver.dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resolver.dbPath, []byte("good-cache"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := resolver.refresh(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not usable") {
		t.Fatalf("expected the fake database to be rejected at load, got %v", err)
	}
	if fixture.gotAuth == "" || !strings.HasPrefix(fixture.gotAuth, "Basic ") {
		t.Fatalf("expected basic auth to be sent, got %q", fixture.gotAuth)
	}
	cached, readErr := os.ReadFile(resolver.dbPath)
	if readErr != nil || string(cached) != "good-cache" {
		t.Fatalf("a rejected database must not replace the cached file, got %q (%v)", cached, readErr)
	}
	if entries, _ := filepath.Glob(filepath.Join(filepath.Dir(resolver.dbPath), "*.tmp")); len(entries) != 0 {
		t.Fatalf("a rejected database must not leave temp files behind: %v", entries)
	}
	if resolver.loaded() {
		t.Fatal("a rejected database must leave the resolver unloaded")
	}
	unloadedReq := httptest.NewRequest(http.MethodGet, "/manifest", nil)
	unloadedReq.RemoteAddr = "203.0.113.7:4242"
	if geo := resolver.Resolve(unloadedReq); geo != nil {
		t.Fatalf("expected nil resolution while unloaded, got %#v", geo)
	}
}

func TestMaxMindRefreshRejectsChecksumMismatch(t *testing.T) {
	archive := buildArchive(t, maxmindEdition+"_20260101/"+maxmindEdition+".mmdb", []byte("payload"))
	fixture := &maxmindFixture{archive: archive, checksum: strings.Repeat("0", 64)}
	resolver := testResolver(t, newMaxMindServer(t, fixture))

	err := resolver.refresh(context.Background())
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("expected a checksum error, got %v", err)
	}
	if _, statErr := os.Stat(resolver.dbPath); !os.IsNotExist(statErr) {
		t.Fatal("a mismatched archive must not be written to disk")
	}
}

func TestMaxMindRefreshRejectsArchiveWithoutDatabase(t *testing.T) {
	archive := buildArchive(t, maxmindEdition+"_20260101/COPYRIGHT.txt", []byte("legal"))
	fixture := &maxmindFixture{archive: archive, checksum: checksumOf(archive)}
	resolver := testResolver(t, newMaxMindServer(t, fixture))

	err := resolver.refresh(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no .mmdb entry") {
		t.Fatalf("expected a missing-database error, got %v", err)
	}
}

func TestMaxMindRefreshHonorsNotModified(t *testing.T) {
	fixture := &maxmindFixture{status: http.StatusNotModified}
	resolver := testResolver(t, newMaxMindServer(t, fixture))
	if err := os.MkdirAll(filepath.Dir(resolver.dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resolver.dbPath, []byte("cached"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolver.resolver = &mmdbReader{}

	if err := resolver.refresh(context.Background()); err != nil {
		t.Fatalf("a 304 answer with a loaded database is not an error: %v", err)
	}
	if fixture.gotIfModified == "" {
		t.Fatal("expected If-Modified-Since to be sent for an existing cache file")
	}
	written, err := os.ReadFile(resolver.dbPath)
	if err != nil || string(written) != "cached" {
		t.Fatalf("a 304 answer must leave the cached file untouched, got %q (%v)", written, err)
	}
}

// A 304 answer while nothing is loaded means the cached file itself is
// unusable; that must surface as an error, not as success.
func TestMaxMindRefreshReportsUnusableCacheOnNotModified(t *testing.T) {
	fixture := &maxmindFixture{status: http.StatusNotModified}
	resolver := testResolver(t, newMaxMindServer(t, fixture))
	if err := os.MkdirAll(filepath.Dir(resolver.dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resolver.dbPath, []byte("truncated"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := resolver.refresh(context.Background())
	if err == nil || !strings.Contains(err.Error(), "cannot be loaded") {
		t.Fatalf("expected an unusable-cache error, got %v", err)
	}
}

func TestMaxMindRefreshReportsBadCredentials(t *testing.T) {
	fixture := &maxmindFixture{status: http.StatusUnauthorized}
	resolver := testResolver(t, newMaxMindServer(t, fixture))

	err := resolver.refresh(context.Background())
	if err == nil || !strings.Contains(err.Error(), "MAXMIND_ACCOUNT_ID") {
		t.Fatalf("expected a credentials error naming the variables, got %v", err)
	}
}

// Close must abort an in-flight download instead of waiting out the HTTP
// timeout.
func TestMaxMindCloseAbortsInFlightDownload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	resolver := newMaxMindResolver("12345", "license", t.TempDir())
	resolver.baseURL = server.URL
	resolver.client = server.Client()
	resolver.start()

	closed := make(chan struct{})
	go func() {
		resolver.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not abort the in-flight download")
	}
}

func TestResolveGeoipCacheDir(t *testing.T) {
	if got := resolveGeoipCacheDir("/configured"); got != "/configured" {
		t.Fatalf("a configured dir must win, got %q", got)
	}
	if got := resolveGeoipCacheDir(""); got == "" {
		t.Fatal("the default cache dir must never be empty")
	}
}

func TestMMDBReaderGuards(t *testing.T) {
	if _, err := openMMDBReader("/nonexistent/GeoLite2-City.mmdb"); err == nil {
		t.Fatal("expected an error for a missing database file")
	}

	// The IP guards run before any database access, so an empty reader is safe to call.
	reader := &mmdbReader{}
	for _, ip := range []string{"", "not-an-ip", "10.1.2.3", "192.168.1.1", "127.0.0.1", "0.0.0.0", "::1", "fd00::1"} {
		if location := reader.resolveLocationFromIP(ip); location != nil {
			t.Fatalf("ip %q must not resolve, got %#v", ip, location)
		}
	}
	// Public IP with no database still resolves to nil instead of panicking.
	if location := reader.resolveLocationFromIP("203.0.113.7"); location != nil {
		t.Fatalf("expected nil without a database, got %#v", location)
	}
	reader.close()
	(*mmdbReader)(nil).close()
}
