package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"xprem/internal/bucket"
	"xprem/internal/helpers"
	"xprem/internal/repository"
	"xprem/internal/requestmeta"
	"xprem/internal/services"
	"xprem/internal/types"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	"howett.net/plist"
)

const (
	registryApp        = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	registryIdentifier = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	registryBuild      = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
)

type registryIdentifierRepo struct {
	services.AppIdentifierRepository
}

func (registryIdentifierRepo) GetAppIdentifierByID(_ context.Context, app, id string) (*repository.AppIdentifierRef, error) {
	if app != registryApp || id != registryIdentifier {
		return nil, nil
	}
	return &repository.AppIdentifierRef{Id: id, Platform: types.PlatformAndroid, Identifier: "com.example.app"}, nil
}

type registryRepo struct {
	mu     sync.Mutex
	builds map[string]types.BuildRecord
	shares map[string]types.BuildShare
	logs   []types.BuildLogChunk
	err    error
}

func newRegistryRepo() *registryRepo {
	return &registryRepo{builds: map[string]types.BuildRecord{}, shares: map[string]types.BuildShare{}}
}

func (r *registryRepo) AppendLogs(_ context.Context, _, _ string, offset int32, content string) error {
	r.logs = append(r.logs, types.BuildLogChunk{Offset: offset, Content: content})
	return nil
}

func (r *registryRepo) ListLogs(_ context.Context, _, _ string, after int32) ([]types.BuildLogChunk, error) {
	chunks := []types.BuildLogChunk{}
	for _, chunk := range r.logs {
		if chunk.Offset >= after {
			chunks = append(chunks, chunk)
		}
	}
	return chunks, nil
}

func (r *registryRepo) Create(_ context.Context, record types.BuildRecord) (*types.BuildRecord, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return nil, false, r.err
	}
	if existing, ok := r.builds[record.ID]; ok {
		return &existing, false, nil
	}
	record.CreatedAt = time.Now()
	r.builds[record.ID] = record
	return &record, true, nil
}

func (r *registryRepo) Get(_ context.Context, appID, id string) (*types.BuildRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	record, ok := r.builds[id]
	if !ok || record.AppID != appID {
		return nil, &repository.ErrResourceNotFound{Resource: "build", Identifier: id}
	}
	return &record, nil
}

func (r *registryRepo) List(_ context.Context, appID string, _, _ int32, _ *types.BuildCursor) ([]types.BuildRecord, int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return nil, 0, r.err
	}
	records := []types.BuildRecord{}
	for _, record := range r.builds {
		if record.AppID == appID {
			records = append(records, record)
		}
	}
	return records, int64(len(records)), nil
}

func (r *registryRepo) Transition(_ context.Context, appID, id string, decide func(types.BuildRecord) (*types.BuildRecord, error)) (*types.BuildRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	current, ok := r.builds[id]
	if !ok || current.AppID != appID {
		return nil, &repository.ErrResourceNotFound{Resource: "build", Identifier: id}
	}
	next, err := decide(current)
	if err != nil {
		return nil, err
	}
	if next == nil {
		return &current, nil
	}
	if next.Status == types.BuildStatusReady && next.ReadyAt == nil {
		now := time.Now()
		next.ReadyAt = &now
	}
	r.builds[id] = *next
	return next, nil
}

func (r *registryRepo) CreateShare(_ context.Context, id, _, hash string, expires time.Time) (types.BuildShare, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	share := types.BuildShare{ID: id, CreatedAt: time.Now(), ExpiresAt: expires}
	r.shares[hash] = share
	return share, nil
}

func (r *registryRepo) ListShares(context.Context, string) ([]types.BuildShare, error) {
	return nil, nil
}

func (r *registryRepo) RevokeShare(context.Context, string, string) error { return nil }

func (r *registryRepo) ResolveShare(_ context.Context, hash string) (*types.BuildRecord, time.Time, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return nil, time.Time{}, r.err
	}
	share, ok := r.shares[hash]
	if !ok || share.RevokedAt != nil || !share.ExpiresAt.After(time.Now()) {
		return nil, time.Time{}, &repository.ErrResourceNotFound{Resource: "share", Identifier: "link"}
	}
	record := r.builds[registryBuild]
	return &record, share.ExpiresAt, nil
}

type registryFixture struct {
	handler *BuildRegistryHandler
	repo    *registryRepo
	router  *mux.Router
}

func newRegistryFixture(t *testing.T) *registryFixture {
	t.Helper()
	t.Setenv("JWT_SECRET", "registry-secret")
	t.Setenv("BASE_URL", "https://ota.example.com/sub/path/")
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_BUILDS_BASE_PATH", t.TempDir())
	t.Setenv("BUCKET_KEY_PREFIX", "")
	artifactStore, err := bucket.OpenBuildArtifactStore()
	require.NoError(t, err)
	repo := newRegistryRepo()
	service := services.NewBuildService(repo, registryIdentifierRepo{}, artifactStore)
	handler := NewBuildRegistryHandler(service)
	router := mux.NewRouter()
	authorized := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if ip := helpers.ClientIP(r); ip.IsValid() {
				r = r.WithContext(requestmeta.WithContext(r.Context(), requestmeta.Metadata{IP: ip.String()}))
			}
			next(w, r.WithContext(services.WithBuildIdentifier(services.WithCliAuth(r.Context(), services.CliCredential{AppID: registryApp, KeyID: 42, KeyName: "ci"}), registryIdentifier)))
		}
	}
	router.HandleFunc("/{APP_ID}/build/{IDENTIFIER_ID}/artifacts/{BUILD_ID}/start", authorized(handler.Start)).Methods(http.MethodPut)
	router.HandleFunc("/{APP_ID}/build/{IDENTIFIER_ID}/artifacts/{BUILD_ID}/logs", authorized(handler.AppendLogs)).Methods(http.MethodPost)
	router.HandleFunc("/{APP_ID}/build/{IDENTIFIER_ID}/artifacts/{BUILD_ID}", authorized(handler.RegisterArtifact)).Methods(http.MethodPut)
	router.HandleFunc("/{APP_ID}/build/{IDENTIFIER_ID}/artifacts/{BUILD_ID}/failed", authorized(handler.Fail)).Methods(http.MethodPost)
	router.HandleFunc("/{APP_ID}/build/{IDENTIFIER_ID}/artifacts/{BUILD_ID}/complete", authorized(handler.Complete)).Methods(http.MethodPost)
	router.HandleFunc("/{APP_ID}/build/{IDENTIFIER_ID}/artifacts/{BUILD_ID}/upload", authorized(handler.UploadLocal)).Methods(http.MethodPut)
	router.HandleFunc("/build-shares/{TOKEN}", handler.PublicShare).Methods(http.MethodGet)
	router.HandleFunc("/build-shares/{TOKEN}/manifest.plist", handler.PublicShareManifest).Methods(http.MethodGet)
	router.HandleFunc("/build-shares/{TOKEN}/app.ipa", handler.PublicShareArtifact).Methods(http.MethodGet)
	router.HandleFunc("/api/app/{APP_ID}/builds", handler.List).Methods(http.MethodGet)
	router.HandleFunc("/api/app/{APP_ID}/builds/{BUILD_ID}", handler.Get).Methods(http.MethodGet)
	router.HandleFunc("/api/app/{APP_ID}/builds/{BUILD_ID}/logs", handler.ListLogs).Methods(http.MethodGet)
	router.HandleFunc("/api/app/{APP_ID}/builds/{BUILD_ID}/download", handler.Download).Methods(http.MethodGet)
	router.HandleFunc("/api/app/{APP_ID}/builds/{BUILD_ID}/shares", handler.CreateShare).Methods(http.MethodPost)
	return &registryFixture{handler: handler, repo: repo, router: router}
}

func (f *registryFixture) do(method, path string, body string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	return w
}

func (f *registryFixture) build(t *testing.T, w *httptest.ResponseRecorder) types.BuildRecord {
	t.Helper()
	var record types.BuildRecord
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &record), w.Body.String())
	return record
}

const registryPath = "/" + registryApp + "/build/" + registryIdentifier + "/artifacts/" + registryBuild

func TestBuildRegistryLogRequests(t *testing.T) {
	f := newRegistryFixture(t)
	w := f.do(http.MethodPut, registryPath+"/start", startBody(time.Now().Add(-time.Minute)))
	require.Equal(t, http.StatusOK, w.Code)
	firstContent := `{"buildStepId":"general","buildStepDisplayName":"Build","time":"2026-09-09T10:00:00Z","level":30,"msg":"héllo"}` + "\n"
	firstBody, err := json.Marshal(map[string]any{"offset": 0, "content": firstContent})
	require.NoError(t, err)
	nextOffset := strconv.Itoa(len(firstContent))
	w = f.do(http.MethodPost, registryPath+"/logs", string(firstBody))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.JSONEq(t, `{"nextOffset":`+nextOffset+`}`, w.Body.String())
	for _, body := range []string{
		`{}`, `null`, `{"offset":0,"content":"plain output\n"}`, `{"offset":-1,"content":"x"}`, `{"offset":2147483648,"content":"x"}`,
		`{"offset":0,"content":"x","token":"no"}`, `{"offset":0,"content":"x"} {}`,
		`{"offset":0,"content":"` + strings.Repeat("x", types.MaxBuildLogChunkBytes+1) + `"}`,
	} {
		require.Equal(t, http.StatusBadRequest, f.do(http.MethodPost, registryPath+"/logs", body).Code, body[:min(80, len(body))])
	}
	withFormat, err := json.Marshal(map[string]any{"offset": len(firstContent), "content": firstContent, "format": "ndjson"})
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, f.do(http.MethodPost, registryPath+"/logs", string(withFormat)).Code)
	require.Len(t, f.repo.logs, 1)
	path := "/api/app/" + registryApp + "/builds/" + registryBuild + "/logs"
	w = f.do(http.MethodGet, path, "")
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"nextOffset":`+nextOffset)
	require.Contains(t, w.Body.String(), "héllo")
	require.NotContains(t, w.Body.String(), `"format"`)
	w = f.do(http.MethodGet, path+"?after="+nextOffset, "")
	require.Equal(t, http.StatusOK, w.Code)
	require.JSONEq(t, `{"chunks":[],"nextOffset":`+nextOffset+`}`, w.Body.String())
	for _, after := range []string{"-1", "hello", "2147483648", "10485761"} {
		require.Equal(t, http.StatusBadRequest, f.do(http.MethodGet, path+"?after="+after, "").Code)
	}
	w = f.do(http.MethodGet, strings.Replace(path, registryApp, registryIdentifier, 1), "")
	require.Equal(t, http.StatusNotFound, w.Code)
	content := `{"time":"2026-09-09T10:00:00Z","level":30,"msg":"Gradle terminé","buildStepDisplayName":"Run Gradle","buildStepId":"gradle","marker":"END_STEP","result":"success","durationMs":12345}` + "\n"
	body, err := json.Marshal(map[string]any{"offset": len(firstContent), "content": content})
	require.NoError(t, err)
	w = f.do(http.MethodPost, registryPath+"/logs", string(body))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var appended struct {
		NextOffset int `json:"nextOffset"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &appended))
	require.Equal(t, len(firstContent)+len(content), appended.NextOffset)
	w = f.do(http.MethodGet, path+"?after="+nextOffset, "")
	require.Equal(t, http.StatusOK, w.Code)
	var page struct {
		Chunks []types.BuildLogChunk `json:"chunks"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &page))
	require.Len(t, page.Chunks, 1)
	require.Equal(t, content, page.Chunks[0].Content)
}

func startBody(startedAt time.Time) string {
	body, _ := json.Marshal(map[string]any{"artifactType": "apk", "metadata": map[string]any{"profile": "production", "channel": "stable", "cliVersion": "2.0.0", "gitCommit": "abc", "gitMessage": "release", "gitDirty": true, "startedAt": startedAt}})
	return string(body)
}

func registerBody(content []byte, startedAt time.Time) string {
	sum := sha256.Sum256(content)
	body, _ := json.Marshal(map[string]any{"artifactType": "apk", "size": len(content), "sha256": hex.EncodeToString(sum[:]), "metadata": map[string]any{
		"profile": "production", "channel": "stable", "cliVersion": "2.0.0", "gitCommit": "abc", "gitMessage": "release", "gitDirty": true, "startedAt": startedAt,
		"version": "1.0.0", "buildNumber": "42", "fingerprint": strings.Repeat("a", 64), "finishedAt": startedAt.Add(4 * time.Minute), "durationMs": 1,
	}})
	return string(body)
}

func TestBuildRegistryRecordsInitialClientIP(t *testing.T) {
	for _, startFirst := range []bool{true, false} {
		t.Run(strconv.FormatBool(startFirst), func(t *testing.T) {
			f := newRegistryFixture(t)
			t.Setenv("TRUST_PROXY_HEADERS", "false")
			startedAt := time.Now().Add(-5 * time.Minute).UTC().Truncate(time.Microsecond)
			request := func(path, body, address string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
				req.RemoteAddr = address
				req.Header.Set("X-Forwarded-For", "203.0.113.99")
				w := httptest.NewRecorder()
				f.router.ServeHTTP(w, req)
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				return w
			}
			if startFirst {
				first := request(registryPath+"/start", startBody(startedAt), "192.0.2.10:1234")
				require.Contains(t, first.Body.String(), `"clientIp":"192.0.2.10"`)
				request(registryPath+"/start", startBody(startedAt), "[2001:db8::20]:1234")
			}
			body := strings.Replace(registerBody([]byte("apk"), startedAt), `"metadata":{`, `"metadata":{"clientIp":"203.0.113.99",`, 1)
			request(registryPath, body, "[2001:db8::20]:1234")
			request(registryPath, body, "192.0.2.30:1234")
			wantIP := "2001:db8::20"
			if startFirst {
				wantIP = "192.0.2.10"
			}
			w := f.do(http.MethodGet, "/api/app/"+registryApp+"/builds/"+registryBuild, "")
			require.Equal(t, http.StatusOK, w.Code)
			require.Contains(t, w.Body.String(), `"clientIp":"`+wantIP+`"`)
		})
	}
}

func TestBuildRegistryStartAndFail(t *testing.T) {
	f := newRegistryFixture(t)
	startedAt := time.Now().Add(-5 * time.Minute).UTC().Truncate(time.Microsecond)

	w := f.do(http.MethodPut, registryPath+"/start", startBody(startedAt))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	started := f.build(t, w)
	require.Equal(t, types.BuildStatusBuilding, started.Status)
	require.Equal(t, registryBuild, started.ID)
	require.Equal(t, registryIdentifier, started.AppIdentifierID)
	require.Equal(t, "com.example.app", started.ApplicationID)
	require.Equal(t, "42", started.ActorID)
	require.NotContains(t, w.Body.String(), `"size"`, "unknown artifact fields are omitted while building")
	require.NotContains(t, w.Body.String(), `"finishedAt"`)
	require.NotContains(t, w.Body.String(), `"fingerprint"`)

	w = f.do(http.MethodPut, registryPath+"/start", startBody(startedAt))
	require.Equal(t, http.StatusOK, w.Code, "idempotent")
	w = f.do(http.MethodPut, registryPath+"/start", strings.Replace(startBody(startedAt), `"production"`, `"preview"`, 1))
	require.Equal(t, http.StatusConflict, w.Code)

	for name, body := range map[string]string{
		"unknown field":  `{"artifactType":"apk","metadata":{"profile":"p","cliVersion":"1","startedAt":"2026-09-09T10:00:00Z","fingerprint":"abc"}}`,
		"not json":       `nope`,
		"trailing json":  startBody(startedAt) + `{}`,
		"missing fields": `{"artifactType":"apk","metadata":{}}`,
		"oversized":      `{"artifactType":"apk","metadata":{"profile":"` + strings.Repeat("p", 17<<10) + `"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			w := f.do(http.MethodPut, "/"+registryApp+"/build/"+registryIdentifier+"/artifacts/dddddddd-dddd-4ddd-8ddd-dddddddddddd/start", body)
			require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			require.Len(t, f.repo.builds, 1)
		})
	}

	w = f.do(http.MethodPost, registryPath+"/failed", `{"finishedAt":"`+time.Now().UTC().Format(time.RFC3339Nano)+`","error":"Exception: keystore password hunter2"}`)
	require.Equal(t, http.StatusBadRequest, w.Code, "raw exception text is not accepted")
	require.Equal(t, types.BuildStatusBuilding, f.repo.builds[registryBuild].Status)
	w = f.do(http.MethodPost, registryPath+"/failed", `{}`)
	require.Equal(t, http.StatusBadRequest, w.Code)
	w = f.do(http.MethodPost, registryPath+"/failed", `{"finishedAt":"`+startedAt.Add(-time.Second).Format(time.RFC3339Nano)+`"}`)
	require.Equal(t, http.StatusBadRequest, w.Code)

	finishedAt := startedAt.Add(2 * time.Minute)
	w = f.do(http.MethodPost, registryPath+"/failed", `{"finishedAt":"`+finishedAt.Format(time.RFC3339Nano)+`"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	failed := f.build(t, w)
	require.Equal(t, types.BuildStatusFailed, failed.Status)
	require.Equal(t, int64(120000), failed.Metadata.DurationMs)
	require.True(t, failed.Metadata.FinishedAt.Equal(finishedAt))
	require.Nil(t, failed.ReadyAt)

	w = f.do(http.MethodPost, registryPath+"/failed", `{"finishedAt":"`+time.Now().UTC().Format(time.RFC3339Nano)+`"}`)
	require.Equal(t, http.StatusOK, w.Code)
	require.True(t, f.build(t, w).Metadata.FinishedAt.Equal(finishedAt), "idempotent")
	w = f.do(http.MethodPost, registryPath+"/complete", "")
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())

	w = f.do(http.MethodPost, "/"+registryApp+"/build/"+registryIdentifier+"/artifacts/dddddddd-dddd-4ddd-8ddd-dddddddddddd/failed", `{"finishedAt":"`+time.Now().UTC().Format(time.RFC3339Nano)+`"}`)
	require.Equal(t, http.StatusNotFound, w.Code, "failure reports do not create rows")
	require.Len(t, f.repo.builds, 1)
}

func TestBuildRegistryLocalUploadFlow(t *testing.T) {
	f := newRegistryFixture(t)
	startedAt := time.Now().Add(-5 * time.Minute).UTC()
	content := []byte("apk payload")

	w := f.do(http.MethodPut, registryPath+"/start", startBody(startedAt))
	require.Equal(t, http.StatusOK, w.Code)
	w = f.do(http.MethodPut, registryPath, registerBody(content, startedAt))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	var registration struct {
		Build  types.BuildRecord `json:"build"`
		Upload *struct {
			URL     string            `json:"url"`
			Method  string            `json:"method"`
			Headers map[string]string `json:"headers"`
		} `json:"upload"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &registration))
	require.Equal(t, types.BuildStatusUploading, registration.Build.Status)
	require.Equal(t, int64(240000), registration.Build.Metadata.DurationMs, "the client's durationMs is ignored")
	require.NotNil(t, registration.Upload)
	require.Equal(t, "PUT", registration.Upload.Method)
	require.Equal(t, "https://ota.example.com/sub/path"+registryPath+"/upload", registration.Upload.URL)
	token := registration.Upload.Headers[bucket.LocalUploadTokenHeader]
	require.NotEmpty(t, token)
	require.NotContains(t, registration.Upload.URL, token)

	for _, target := range []string{registryPath + "/upload", registryPath + "/upload?token=" + token} {
		w = f.do(http.MethodPut, target, string(content))
		require.Equal(t, http.StatusUnauthorized, w.Code, "only the local-upload-token header authorizes the upload")
	}
	w = f.do(http.MethodPut, registryPath+"/upload", string(content)+"extra", bucket.LocalUploadTokenHeader, token)
	require.Equal(t, http.StatusBadRequest, w.Code, "bodies beyond the declared size are refused")
	w = f.do(http.MethodPut, registryPath+"/upload", string(content), bucket.LocalUploadTokenHeader, "forged")
	require.Equal(t, http.StatusUnauthorized, w.Code)
	w = f.do(http.MethodPut, registryPath+"/upload", string(content), bucket.LocalUploadTokenHeader, token)
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))

	w = f.do(http.MethodPost, registryPath+"/complete", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	ready := f.build(t, w)
	require.Equal(t, types.BuildStatusReady, ready.Status)
	require.NotNil(t, ready.ReadyAt)

	w = f.do(http.MethodPut, registryPath+"/upload", string(content), bucket.LocalUploadTokenHeader, token)
	require.Equal(t, http.StatusConflict, w.Code, "a ready build accepts no further bytes")
	w = f.do(http.MethodPut, registryPath, registerBody(content, startedAt))
	require.Equal(t, http.StatusOK, w.Code)
	require.NotContains(t, w.Body.String(), `"upload"`, "ready builds get no upload grant")
	w = f.do(http.MethodPut, registryPath, registerBody([]byte("other"), startedAt))
	require.Equal(t, http.StatusConflict, w.Code)
	w = f.do(http.MethodPost, registryPath+"/failed", `{"finishedAt":"`+time.Now().UTC().Format(time.RFC3339Nano)+`"}`)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, types.BuildStatusReady, f.build(t, w).Status, "ready never regresses")

	w = f.do(http.MethodGet, "/api/app/"+registryApp+"/builds/"+registryBuild+"/download", "")
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "application/vnd.android.package-archive", w.Header().Get("Content-Type"))
	require.Equal(t, content, w.Body.Bytes())
	w = f.do(http.MethodGet, "/api/app/"+registryApp+"/builds", "")
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"count":1`)
	w = f.do(http.MethodGet, "/api/app/"+registryApp+"/builds?limit=0", "")
	require.Equal(t, http.StatusBadRequest, w.Code)
	w = f.do(http.MethodGet, "/api/app/"+registryApp+"/builds/not-a-uuid", "")
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestBuildRegistryRejectsInvalidPagination(t *testing.T) {
	f := newRegistryFixture(t)
	encode := func(payload string) string { return base64.RawURLEncoding.EncodeToString([]byte(payload)) }
	validCursor := encode(`{"createdAt":"2026-09-13T12:00:00.123456Z","id":"` + registryBuild + `"}`)
	for _, query := range []string{
		"limit=0", "limit=101", "limit=bad", "offset=-1", "offset=100001", "offset=bad",
		"cursor=not-base64!", "cursor=" + strings.Repeat("a", 513),
		"cursor=" + encode(`null`), "cursor=" + encode(`{}`),
		"cursor=" + encode(`{"id":"`+registryBuild+`"}`),
		"cursor=" + encode(`{"createdAt":"2026-09-13T12:00:00Z"}`),
		"cursor=" + encode(`{"createdAt":"invalid","id":"`+registryBuild+`"}`),
		"cursor=" + encode(`{"createdAt":"2026-09-13T12:00:00Z","id":"invalid"}`),
		"cursor=" + validCursor + "&offset=1",
	} {
		t.Run(query, func(t *testing.T) {
			response := f.do(http.MethodGet, "/api/app/"+registryApp+"/builds?"+query, "")
			require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
		})
	}
	response := f.do(http.MethodGet, "/api/app/"+registryApp+"/builds?cursor="+validCursor, "")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
}

func TestBuildRegistryUsesAuthorizedIdentifierNotPath(t *testing.T) {
	f := newRegistryFixture(t)
	startedAt := time.Now().Add(-time.Minute).UTC()
	w := f.do(http.MethodPut, "/"+registryApp+"/build/eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee/artifacts/"+registryBuild+"/start", startBody(startedAt))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, registryIdentifier, f.build(t, w).AppIdentifierID, "the identifier comes from the authorization context")

	bare := mux.NewRouter()
	bare.HandleFunc("/{APP_ID}/build/{IDENTIFIER_ID}/artifacts/{BUILD_ID}/start", f.handler.Start).Methods(http.MethodPut)
	bare.HandleFunc("/{APP_ID}/build/{IDENTIFIER_ID}/artifacts/{BUILD_ID}/failed", f.handler.Fail).Methods(http.MethodPost)
	for _, tc := range []struct{ method, suffix, body string }{{http.MethodPut, "/start", startBody(startedAt)}, {http.MethodPost, "/failed", `{"finishedAt":"` + time.Now().UTC().Format(time.RFC3339Nano) + `"}`}} {
		req := httptest.NewRequest(tc.method, registryPath+tc.suffix, strings.NewReader(tc.body))
		w := httptest.NewRecorder()
		bare.ServeHTTP(w, req)
		require.Equal(t, http.StatusNotFound, w.Code, "without an authorized identifier in the context the path variable is never trusted")
	}
	require.Equal(t, types.BuildStatusBuilding, f.repo.builds[registryBuild].Status)
}

func TestBuildRegistryShareLinks(t *testing.T) {
	f := newRegistryFixture(t)
	startedAt := time.Now().Add(-5 * time.Minute).UTC()
	content := []byte("shared apk")
	w := f.do(http.MethodPut, registryPath, registerBody(content, startedAt))
	require.Equal(t, http.StatusOK, w.Code)
	var registration struct {
		Upload struct {
			Headers map[string]string `json:"headers"`
		} `json:"upload"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &registration))
	require.Equal(t, http.StatusNoContent, f.do(http.MethodPut, registryPath+"/upload", string(content), bucket.LocalUploadTokenHeader, registration.Upload.Headers[bucket.LocalUploadTokenHeader]).Code)
	require.Equal(t, http.StatusOK, f.do(http.MethodPost, registryPath+"/complete", "").Code)

	t.Setenv("BASE_URL", "https://ota.example.com/sub/path")
	w = f.do(http.MethodPost, "/api/app/"+registryApp+"/builds/"+registryBuild+"/shares", "")
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	var created struct {
		Share types.BuildShare `json:"share"`
		URL   string           `json:"url"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	require.True(t, strings.HasPrefix(created.URL, "https://ota.example.com/sub/path/build-shares/"), created.URL)
	require.WithinDuration(t, time.Now().Add(24*time.Hour), created.Share.ExpiresAt, time.Minute, "the default expiry is one day")
	shareToken := strings.TrimPrefix(created.URL, "https://ota.example.com/sub/path/build-shares/")
	require.Len(t, shareToken, 64)

	w = f.do(http.MethodGet, "/build-shares/"+shareToken, "")
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "private, no-store", w.Header().Get("Cache-Control"))
	require.Equal(t, "noindex, nofollow", w.Header().Get("X-Robots-Tag"))
	require.Equal(t, content, w.Body.Bytes())
	require.Equal(t, "application/vnd.android.package-archive", w.Header().Get("Content-Type"))
	require.Equal(t, `attachment; filename="`+registryBuild+`.apk"`, w.Header().Get("Content-Disposition"))
	require.Equal(t, "no-referrer", w.Header().Get("Referrer-Policy"))

	for _, bad := range []string{"short", strings.Repeat("0", 64), strings.ToUpper(shareToken)} {
		w = f.do(http.MethodGet, "/build-shares/"+bad, "")
		require.Equal(t, http.StatusBadRequest, w.Code, bad)
		require.Equal(t, "Expired link\n", w.Body.String())
		require.Equal(t, "text/plain; charset=utf-8", w.Header().Get("Content-Type"))
	}
	for _, state := range []string{"expired", "revoked"} {
		share := created.Share
		past := time.Now().Add(-time.Minute)
		if state == "expired" {
			share.ExpiresAt = past
		} else {
			share.RevokedAt = &past
		}
		hash := sha256.Sum256([]byte(shareToken))
		f.repo.shares[hex.EncodeToString(hash[:])] = share
		w = f.do(http.MethodGet, "/build-shares/"+shareToken, "")
		require.Equal(t, http.StatusBadRequest, w.Code, state)
		require.Equal(t, "Expired link\n", w.Body.String())
		require.Equal(t, "text/plain; charset=utf-8", w.Header().Get("Content-Type"))
	}
	f.repo.err = errors.New("database down")
	w = f.do(http.MethodGet, "/build-shares/"+shareToken, "")
	require.Equal(t, http.StatusInternalServerError, w.Code, "outages are not reported as expired links")
	require.NotContains(t, w.Body.String(), "database down")
}

func TestBuildRegistryErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{
		{services.ErrUnauthorized, http.StatusUnauthorized},
		{services.ErrBuildConflict, http.StatusConflict},
		{services.ErrBuildNotReady, http.StatusConflict},
		{services.ErrBuildState, http.StatusConflict},
		{services.ErrBuildIntegrity, http.StatusBadRequest},
		{&repository.ErrResourceNotFound{Resource: "build", Identifier: "x"}, http.StatusNotFound},
		{repository.ErrNotSupportedInStatelessMode, http.StatusBadRequest},
		{services.ErrBuildStorageUnavailable, http.StatusBadRequest},
		{errors.New("connection refused to 10.0.0.1"), http.StatusInternalServerError},
	} {
		w := httptest.NewRecorder()
		renderBuildRegistryError(w, tc.err)
		require.Equal(t, tc.status, w.Code, tc.err.Error())
		if tc.status == http.StatusInternalServerError {
			require.NotContains(t, w.Body.String(), "10.0.0.1")
		}
	}
}

func TestBuildRegistryUploadBodyIsBounded(t *testing.T) {
	f := newRegistryFixture(t)
	req := httptest.NewRequest(http.MethodPut, registryPath+"/upload", io.LimitReader(bytes.NewReader(make([]byte, 1)), 1))
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code, "the token is checked before any byte is read")
}

func TestSharedIosBuildInstallsThroughAManifest(t *testing.T) {
	f := newRegistryFixture(t)
	f.repo.builds[registryBuild] = types.BuildRecord{
		ID: registryBuild, AppID: registryApp, AppIdentifierID: registryIdentifier, Platform: types.PlatformIOS,
		ApplicationID: "com.example.app", Status: types.BuildStatusReady, ArtifactType: types.BuildArtifactIPA,
		Metadata: types.BuildMetadata{Distribution: types.IosDistributionAdHoc, Version: "1.4.0", BuildNumber: "42"},
	}
	w := f.do(http.MethodPost, "/api/app/"+registryApp+"/builds/"+registryBuild+"/shares", "")
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var created struct {
		URL string `json:"url"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	sharePath := strings.TrimPrefix(created.URL, "https://ota.example.com/sub/path")

	w = f.do(http.MethodGet, sharePath, "")
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "text/html; charset=utf-8", w.Header().Get("Content-Type"))
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	require.Contains(t, w.Body.String(), `href="itms-services://?action=download-manifest&amp;url=`+url.QueryEscape(created.URL+"/manifest.plist")+`"`)
	require.Contains(t, w.Body.String(), "Version 1.4.0 (42)")

	w = f.do(http.MethodGet, sharePath+"/manifest.plist", "")
	require.Equal(t, http.StatusOK, w.Code)
	var manifest struct {
		Items []struct {
			Assets []struct {
				Kind string `plist:"kind"`
				URL  string `plist:"url"`
			} `plist:"assets"`
			Metadata map[string]string `plist:"metadata"`
		} `plist:"items"`
	}
	_, err := plist.Unmarshal(w.Body.Bytes(), &manifest)
	require.NoError(t, err)
	require.Len(t, manifest.Items, 1)
	require.Equal(t, "software-package", manifest.Items[0].Assets[0].Kind)
	require.Equal(t, created.URL+"/app.ipa", manifest.Items[0].Assets[0].URL)
	require.Equal(t, map[string]string{"bundle-identifier": "com.example.app", "bundle-version": "1.4.0", "kind": "software", "title": "com.example.app"}, manifest.Items[0].Metadata)

	require.Equal(t, http.StatusBadRequest, f.do(http.MethodGet, "/build-shares/"+strings.Repeat("0", 64)+"/manifest.plist", "").Code)
}
