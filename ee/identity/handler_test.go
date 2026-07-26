// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package identity

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
)

// fakeStore is an in-memory identity.Store for handler tests. It defines the
// query/CRUD methods the dashboard exercises; the embedded Store supplies the
// unused ingest mutator methods so it satisfies the interface.
type fakeStore struct {
	Store
	schema  Schema
	devices map[string]*Device
	values  []ValueCount
	// listDevices lets a test control pagination output.
	listDevices func(query DeviceQuery, limit int, cursor *DeviceCursor) ([]Device, *DeviceCursor, error)
	upsertErr   error
	health      map[string]UpdateHealth
	onlineSince time.Time
	onlineQuery DeviceQuery
}

func newFakeStore() *fakeStore {
	return &fakeStore{schema: Schema{}, devices: map[string]*Device{}}
}

func (f *fakeStore) GetSchema(_ context.Context, _ string) (Schema, error) { return f.schema, nil }

func (f *fakeStore) UpsertSchemaKey(_ context.Context, _ string, spec KeySpec) (KeySpec, error) {
	if f.upsertErr != nil {
		return KeySpec{}, f.upsertErr
	}
	f.schema[spec.Key] = spec
	return spec, nil
}

func (f *fakeStore) DeleteSchemaKey(_ context.Context, _ string, key string) (bool, error) {
	if _, ok := f.schema[key]; !ok {
		return false, nil
	}
	delete(f.schema, key)
	return true, nil
}

func (f *fakeStore) SearchMetadataValues(_ context.Context, _ string, _ string, _ string, _ int) ([]ValueCount, error) {
	return f.values, nil
}

func (f *fakeStore) ListDevices(_ context.Context, _ string, query DeviceQuery, limit int, cursor *DeviceCursor) ([]Device, *DeviceCursor, error) {
	if f.listDevices != nil {
		return f.listDevices(query, limit, cursor)
	}
	return nil, nil, nil
}

func (f *fakeStore) CountOnlineDevices(_ context.Context, _ string, since time.Time, query DeviceQuery) (int64, error) {
	f.onlineSince = since
	f.onlineQuery = query
	return int64(len(f.devices)), nil
}

func (f *fakeStore) GetDevice(_ context.Context, _ string, easClientID string) (*Device, error) {
	return f.devices[easClientID], nil
}

func (f *fakeStore) UpdateHealthByIDs(_ context.Context, _ string, updateIDs []string) (map[string]UpdateHealth, error) {
	out := make(map[string]UpdateHealth, len(updateIDs))
	for _, id := range updateIDs {
		if h, ok := f.health[id]; ok {
			out[id] = h
		}
	}
	return out, nil
}

// serve routes a request through a real mux router so path vars resolve.
// licensedService builds a service with the enterprise gate open. Same-package
// tests flip the field rather than mint a signed key; the gate itself is
// covered by TestCustomAttributesRequireALicense.
func licensedService(store Store, geo GeoResolver) *Service {
	service := NewService(store, geo)
	service.licenseValid = func() bool { return true }
	return service
}

func serve(handler *IdentityHandler, method, path, body string) *httptest.ResponseRecorder {
	router := mux.NewRouter()
	router.HandleFunc("/api/apps/{APP_ID}/identity/schema", handler.GetSchemaHandler).Methods(http.MethodGet)
	router.HandleFunc("/api/apps/{APP_ID}/identity/schema/{KEY}", handler.UpsertSchemaKeyHandler).Methods(http.MethodPut)
	router.HandleFunc("/api/apps/{APP_ID}/identity/schema/{KEY}", handler.DeleteSchemaKeyHandler).Methods(http.MethodDelete)
	router.HandleFunc("/api/apps/{APP_ID}/identity/values", handler.SearchValuesHandler).Methods(http.MethodGet)
	router.HandleFunc("/api/apps/{APP_ID}/identity/devices", handler.ListDevicesHandler).Methods(http.MethodGet)
	router.HandleFunc("/api/apps/{APP_ID}/identity/devices/{EAS_CLIENT_ID}", handler.GetDeviceHandler).Methods(http.MethodGet)
	router.HandleFunc("/api/apps/{APP_ID}/identity/update-health", handler.UpdateHealthHandler).Methods(http.MethodGet)
	router.HandleFunc("/api/apps/{APP_ID}/identity/online", handler.OnlineDevicesHandler).Methods(http.MethodGet)
	req := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

const appPath = "/api/apps/app-1/identity"

func TestNilStoreAnswers400(t *testing.T) {
	h := NewIdentityHandler(nil)
	for _, path := range []string{
		appPath + "/schema",
		appPath + "/values?key=userId",
		appPath + "/devices",
		appPath + "/devices/abc",
		appPath + "/update-health?ids=9b3b89b6-5a0d-4a57-b1f5-6e1d5b7c2a10",
	} {
		rec := serve(h, http.MethodGet, path, "")
		require.Equal(t, http.StatusBadRequest, rec.Code, path)
	}
}

func TestSchemaCRUDHandlers(t *testing.T) {
	store := newFakeStore()
	h := NewIdentityHandler(licensedService(store, nil))

	// Empty schema.
	rec := serve(h, http.MethodGet, appPath+"/schema", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	var listed struct {
		Keys []schemaKeyResponse `json:"keys"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &listed))
	require.Empty(t, listed.Keys)

	// Upsert a valid key; omitted maxLength defaults.
	rec = serve(h, http.MethodPut, appPath+"/schema/userId", `{"type":"string"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	var saved schemaKeyResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &saved))
	require.Equal(t, schemaKeyResponse{Key: "userId", Type: "string", MaxLength: DefaultMaxLength}, saved)

	// Invalid type is a 400 before the store.
	rec = serve(h, http.MethodPut, appPath+"/schema/bad", `{"type":"uuid"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "application/problem+json; charset=utf-8", rec.Header().Get("Content-Type"))

	// Malformed body is a 400.
	rec = serve(h, http.MethodPut, appPath+"/schema/userId", `not json`)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	// Delete existing then missing.
	rec = serve(h, http.MethodDelete, appPath+"/schema/userId", "")
	require.Equal(t, http.StatusNoContent, rec.Code)
	rec = serve(h, http.MethodDelete, appPath+"/schema/userId", "")
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestUpsertSchemaKeyLimitIs409(t *testing.T) {
	store := newFakeStore()
	store.upsertErr = ErrTooManySchemaKeys
	h := NewIdentityHandler(licensedService(store, nil))
	rec := serve(h, http.MethodPut, appPath+"/schema/userId", `{"type":"string"}`)
	require.Equal(t, http.StatusConflict, rec.Code)
}

// Declaring an attribute without a license is a 403 the dashboard can read,
// not an opaque 500.
func TestUpsertSchemaKeyWithoutLicenseIs403(t *testing.T) {
	service := NewService(newFakeStore(), nil)
	service.licenseValid = func() bool { return false }
	rec := serve(NewIdentityHandler(service), http.MethodPut, appPath+"/schema/plan", `{"type":"string"}`)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "enterprise license")
}

func TestSearchValuesHandler(t *testing.T) {
	store := newFakeStore()
	store.values = []ValueCount{{Value: "acme", DeviceCount: 3}, {Value: "globex", DeviceCount: 1}}
	h := NewIdentityHandler(licensedService(store, nil))

	// Missing key is a 400.
	rec := serve(h, http.MethodGet, appPath+"/values", "")
	require.Equal(t, http.StatusBadRequest, rec.Code)

	rec = serve(h, http.MethodGet, appPath+"/values?key=tenant&search=ac", "")
	require.Equal(t, http.StatusOK, rec.Code)
	var out struct {
		Values []ValueCount `json:"values"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Equal(t, store.values, out.Values)
}

func TestGetDeviceHandler(t *testing.T) {
	store := newFakeStore()
	now := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	deviceID := uuid.NewString()
	store.devices[deviceID] = &Device{
		EASClientID: deviceID,
		Metadata:    map[string]any{"userId": "u1"},
		CountryCode: strPtr("FR"),
		FirstSeenAt: now,
		LastSeenAt:  now,
	}
	h := NewIdentityHandler(licensedService(store, nil))

	rec := serve(h, http.MethodGet, appPath+"/devices/"+deviceID, "")
	require.Equal(t, http.StatusOK, rec.Code)
	var d deviceResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &d))
	require.Equal(t, deviceID, d.EasClientId)
	require.Equal(t, "u1", d.Metadata["userId"])
	require.Equal(t, "FR", *d.CountryCode)
	require.Equal(t, "2026-07-23T10:00:00Z", d.LastSeenAt)

	// A missing but well-formed uuid → 404.
	rec = serve(h, http.MethodGet, appPath+"/devices/"+uuid.NewString(), "")
	require.Equal(t, http.StatusNotFound, rec.Code)

	// A non-uuid path segment is 404, not a 500 from the store's uuid parse.
	rec = serve(h, http.MethodGet, appPath+"/devices/not-a-uuid", "")
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestListDevicesTamperedCursorIs400(t *testing.T) {
	store := newFakeStore()
	h := NewIdentityHandler(licensedService(store, nil))
	// Valid base64 + valid timestamp but a non-uuid second segment: must 400
	// at the handler, never reach the store to 500 on the uuid parse.
	tampered := base64.RawURLEncoding.EncodeToString([]byte("2026-01-01T00:00:00Z|not-a-uuid"))
	rec := serve(h, http.MethodGet, appPath+"/devices?cursor="+tampered, "")
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestListDevicesHandlerPaginationAndFilter(t *testing.T) {
	store := newFakeStore()
	now := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	deviceID := uuid.NewString()
	var gotQuery DeviceQuery
	var gotCursor *DeviceCursor
	store.listDevices = func(query DeviceQuery, limit int, cursor *DeviceCursor) ([]Device, *DeviceCursor, error) {
		gotQuery, gotCursor = query, cursor
		return []Device{{EASClientID: deviceID, FirstSeenAt: now, LastSeenAt: now}},
			&DeviceCursor{LastSeenAt: now, EASClientID: deviceID}, nil
	}
	// A filter is only accepted against a declared attribute: the type is what
	// says whether "u1" is a string, a number or a boolean once it reaches JSONB.
	store.schema = Schema{"userId": {Key: "userId", Type: ValueTypeString, MaxLength: 256}}
	h := NewIdentityHandler(licensedService(store, nil))

	rec := serve(h, http.MethodGet, appPath+"/devices?attr=userId:u1", "")
	require.Equal(t, http.StatusOK, rec.Code)
	var page struct {
		Devices    []deviceResponse `json:"devices"`
		NextCursor *string          `json:"nextCursor"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	require.Len(t, page.Devices, 1)
	require.NotNil(t, page.NextCursor)
	require.Equal(t, MetadataFilters{{Key: "userId", Values: []any{"u1"}}}, gotQuery.Metadata)
	require.Nil(t, gotCursor, "first page has no cursor")

	// The opaque nextCursor round-trips: sending it back decodes to the same position.
	rec2 := serve(h, http.MethodGet, appPath+"/devices?cursor="+*page.NextCursor, "")
	require.Equal(t, http.StatusOK, rec2.Code)
	require.NotNil(t, gotCursor)
	require.Equal(t, deviceID, gotCursor.EASClientID)
	require.True(t, gotCursor.LastSeenAt.Equal(now))

	// A malformed cursor is a 400.
	rec3 := serve(h, http.MethodGet, appPath+"/devices?cursor=!!!notbase64", "")
	require.Equal(t, http.StatusBadRequest, rec3.Code)
}

// Metadata is matched by JSONB containment, which is type-aware: a boolean
// stored as `true` is never found by the string `"true"`, so the filter has to
// reach the store carrying the type its schema declares.
func TestListDevicesFilterCarriesTheDeclaredType(t *testing.T) {
	store := newFakeStore()
	store.schema = Schema{
		"canaryUser": {Key: "canaryUser", Type: ValueTypeBoolean, MaxLength: 256},
		"planLevel":  {Key: "planLevel", Type: ValueTypeNumber, MaxLength: 256},
	}
	var gotQuery DeviceQuery
	store.listDevices = func(query DeviceQuery, _ int, _ *DeviceCursor) ([]Device, *DeviceCursor, error) {
		gotQuery = query
		return nil, nil, nil
	}
	h := NewIdentityHandler(licensedService(store, nil))

	rec := serve(h, http.MethodGet, appPath+"/devices?attr=canaryUser:true", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, MetadataFilters{{Key: "canaryUser", Values: []any{true}}}, gotQuery.Metadata)

	rec = serve(h, http.MethodGet, appPath+"/devices?attr=planLevel:42", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, MetadataFilters{{Key: "planLevel", Values: []any{float64(42)}}}, gotQuery.Metadata)

	// A value that cannot be the declared type, and a key nothing declares, are
	// both malformed questions rather than questions with an empty answer.
	rec = serve(h, http.MethodGet, appPath+"/devices?attr=canaryUser:perhaps", "")
	require.Equal(t, http.StatusBadRequest, rec.Code)

	rec = serve(h, http.MethodGet, appPath+"/devices?attr=nothing:x", "")
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDeviceCursorRoundTrip(t *testing.T) {
	now := time.Date(2026, 7, 23, 10, 30, 15, 123456789, time.UTC)
	deviceID := uuid.NewString()
	c := &DeviceCursor{LastSeenAt: now, EASClientID: deviceID}
	encoded := encodeDeviceCursor(c)
	require.NotNil(t, encoded)
	decoded, err := decodeDeviceCursor(*encoded)
	require.NoError(t, err)
	require.Equal(t, deviceID, decoded.EASClientID)
	require.True(t, decoded.LastSeenAt.Equal(now))

	require.Nil(t, encodeDeviceCursor(nil))
	got, err := decodeDeviceCursor("")
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestUpdateHealthHandler(t *testing.T) {
	store := newFakeStore()
	healthy := "9b3b89b6-5a0d-4a57-b1f5-6e1d5b7c2a10"
	broken := "0f61f1d1-3f5f-4b6a-9a44-6e9a1c2b3d4e"
	crashy := "1c2d3e4f-5a6b-4c7d-8e9f-0a1b2c3d4e5f"
	untried := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	store.health = map[string]UpdateHealth{
		healthy: {DevicesOnUpdate: 99, UpdateIssues: 1},
		broken:  {DevicesOnUpdate: 0, UpdateIssues: 7},
		// 10 devices run it; 2 JS-crashed and still run it, 1 more JS-crashed
		// then moved on: attempts 10+3-2=11, healthy 10-2=8.
		crashy: {DevicesOnUpdate: 10, RuntimeIssues: 3, FailedStillOn: 2},
	}
	h := NewIdentityHandler(licensedService(store, nil))

	rec := serve(h, http.MethodGet, appPath+"/update-health?ids="+healthy+","+broken+","+crashy+","+untried+",garbage", "")
	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Updates map[string]struct {
			DevicesOnUpdate   int64    `json:"devicesOnUpdate"`
			SuccessfulDevices int64    `json:"successfulDevices"`
			FaultyDevices     int64    `json:"faultyDevices"`
			LaunchFailures    int64    `json:"launchFailures"`
			UpdateIssues      int64    `json:"updateIssues"`
			RuntimeIssues     int64    `json:"runtimeIssues"`
			HealthPercent     *float64 `json:"healthPercent"`
		} `json:"updates"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	// Garbage id: silently absent. Every valid id gets an entry.
	require.Len(t, body.Updates, 4)
	require.NotNil(t, body.Updates[healthy].HealthPercent)
	require.InDelta(t, 99.0, *body.Updates[healthy].HealthPercent, 0.001)
	require.EqualValues(t, 99, body.Updates[healthy].SuccessfulDevices)
	require.EqualValues(t, 1, body.Updates[healthy].FaultyDevices)
	require.EqualValues(t, 1, body.Updates[healthy].UpdateIssues)
	require.EqualValues(t, 1, body.Updates[healthy].LaunchFailures)
	// Runtime failures overlap the current cohort: still-running crashers
	// count once as attempts and drop out of healthy.
	require.EqualValues(t, 3, body.Updates[crashy].RuntimeIssues)
	require.EqualValues(t, 3, body.Updates[crashy].LaunchFailures)
	require.EqualValues(t, 8, body.Updates[crashy].SuccessfulDevices)
	require.EqualValues(t, 3, body.Updates[crashy].FaultyDevices)
	require.NotNil(t, body.Updates[crashy].HealthPercent)
	require.InDelta(t, 100.0*8.0/11.0, *body.Updates[crashy].HealthPercent, 0.001)
	// Zero successes with failures is a hard 0%, the broken-update red badge.
	require.NotNil(t, body.Updates[broken].HealthPercent)
	require.InDelta(t, 0.0, *body.Updates[broken].HealthPercent, 0.001)
	// Nothing attempted it: percent stays null, never a fake 100%.
	require.Nil(t, body.Updates[untried].HealthPercent)
	require.EqualValues(t, 0, body.Updates[untried].DevicesOnUpdate)

	// Input contract: missing ids is a 400, an oversized list is a 400.
	require.Equal(t, http.StatusBadRequest, serve(h, http.MethodGet, appPath+"/update-health", "").Code)
	tooMany := make([]string, 101)
	for i := range tooMany {
		tooMany[i] = healthy
	}
	require.Equal(t, http.StatusBadRequest, serve(h, http.MethodGet, appPath+"/update-health?ids="+strings.Join(tooMany, ","), "").Code)
}

// "Online" is a window over last_seen_at, which every contact bumps. The
// handler's job is to turn the requested window into the right lower bound and
// to refuse a nonsensical one.
func TestOnlineDevicesHandler(t *testing.T) {
	store := newFakeStore()
	store.devices["a"] = &Device{EASClientID: "a"}
	store.devices["b"] = &Device{EASClientID: "b"}
	h := NewIdentityHandler(licensedService(store, nil))

	rec := serve(h, http.MethodGet, appPath+"/online", "")
	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Online        int64 `json:"online"`
		WindowMinutes int   `json:"windowMinutes"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.EqualValues(t, 2, body.Online)
	require.Equal(t, 20, body.WindowMinutes)
	require.WithinDuration(t, time.Now().UTC().Add(-DefaultOnlineWindow), store.onlineSince, time.Minute)

	rec = serve(h, http.MethodGet, appPath+"/online?minutes=5", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.WithinDuration(t, time.Now().UTC().Add(-5*time.Minute), store.onlineSince, time.Minute)

	// A window past the cap is clamped, not honoured, so one request cannot
	// turn into a full-table scan of the registry.
	rec = serve(h, http.MethodGet, appPath+"/online?minutes=100000", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, int(MaxOnlineWindow.Minutes()), body.WindowMinutes)

	// Large enough to overflow int64 when multiplied by time.Minute, which used
	// to wrap the duration negative and report a window from the future.
	rec = serve(h, http.MethodGet, appPath+"/online?minutes=200000000", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, int(MaxOnlineWindow.Minutes()), body.WindowMinutes)
	require.Positive(t, body.WindowMinutes)

	rec = serve(h, http.MethodGet, appPath+"/online?minutes=nope", "")
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// The count is shown next to filtered figures, so it takes the same filters as
// the inventory and refuses the same malformed ones. Anything the registry
// cannot honour never reaches it: the caller only sends the dimensions below.
func TestOnlineDevicesHandlerAppliesFilters(t *testing.T) {
	store := newFakeStore()
	store.devices["a"] = &Device{EASClientID: "a"}
	h := NewIdentityHandler(licensedService(store, nil))

	update := "9b3b89b6-5a0d-4a57-b1f5-6e1d5b7c2a10"
	rec := serve(h, http.MethodGet, appPath+"/online?platform=ios&branch=main&branch=staging&updateId="+update+"&countryCode=FR", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []string{"ios"}, store.onlineQuery.Platforms)
	require.Equal(t, []string{"main", "staging"}, store.onlineQuery.Branches)
	require.Equal(t, []string{update}, store.onlineQuery.CurrentUpdateIDs)
	require.Equal(t, []string{"FR"}, store.onlineQuery.CountryCodes)

	require.Equal(t, http.StatusBadRequest, serve(h, http.MethodGet, appPath+"/online?updateId=not-a-uuid", "").Code)
}

// Each filter list becomes a text[] in an `= ANY(...)`, and a hand-written URL
// can repeat one key thousands of times.
func TestListDevicesRejectsOversizedFilterLists(t *testing.T) {
	store := newFakeStore()
	h := NewIdentityHandler(licensedService(store, nil))

	query := strings.Repeat("&deviceModel=SM-A546B", maxDeviceFilterValues+1)
	rec := serve(h, http.MethodGet, appPath+"/devices?"+strings.TrimPrefix(query, "&"), "")
	require.Equal(t, http.StatusBadRequest, rec.Code)

	within := strings.Repeat("&deviceModel=SM-A546B", maxDeviceFilterValues)
	rec = serve(h, http.MethodGet, appPath+"/devices?"+strings.TrimPrefix(within, "&"), "")
	require.Equal(t, http.StatusOK, rec.Code)
}
