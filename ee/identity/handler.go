// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package identity

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"expo-open-ota/internal/handlers"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// IdentityHandler serves the dashboard "Identity" section: the metadata allowlist, value
// autocomplete, and the device inventory. A nil service (stateless mode) answers 400.
type IdentityHandler struct {
	service *Service
}

func NewIdentityHandler(service *Service) *IdentityHandler {
	return &IdentityHandler{service: service}
}

// requireService short-circuits with a 400 when identity has no storage
// (stateless mode). Returns the service and true when it is available.
func (h *IdentityHandler) requireService(w http.ResponseWriter) (*Service, bool) {
	if h.service == nil {
		handlers.RenderError(w, http.StatusBadRequest, "Device identity requires a control plane (database).")
		return nil, false
	}
	return h.service, true
}

func renderIdentityServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrRequiresValidLicense):
		handlers.RenderError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, ErrTooManySchemaKeys):
		handlers.RenderError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrTooManyCombinations):
		// A bad request, not a server fault, however deep in the store it surfaces.
		handlers.RenderError(w, http.StatusBadRequest, err.Error())
	default:
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred.")
	}
}

// schemaKeyResponse is the wire shape of a KeySpec (camelCase; timestamps RFC3339 UTC).
type schemaKeyResponse struct {
	Key       string `json:"key"`
	Type      string `json:"type"`
	MaxLength int    `json:"maxLength"`
}

func schemaKeyResponseFrom(spec KeySpec) schemaKeyResponse {
	return schemaKeyResponse{Key: spec.Key, Type: string(spec.Type), MaxLength: spec.MaxLength}
}

// deviceResponse is the wire shape of a Device (camelCase; timestamps RFC3339 UTC).
type deviceResponse struct {
	EasClientId string         `json:"easClientId"`
	Metadata    map[string]any `json:"metadata"`
	CountryCode *string        `json:"countryCode,omitempty"`
	City        *string        `json:"city,omitempty"`
	Lat         *float64       `json:"lat,omitempty"`
	Lng         *float64       `json:"lng,omitempty"`
	DeviceModel *string        `json:"deviceModel,omitempty"`
	OsName      *string        `json:"osName,omitempty"`
	OsVersion   *string        `json:"osVersion,omitempty"`
	// Absent when the device runs the embedded bundle or an update this server never published.
	CurrentUpdateId *string `json:"currentUpdateId,omitempty"`
	Branch          *string `json:"branch,omitempty"`
	RuntimeVersion  *string `json:"runtimeVersion,omitempty"`
	Platform        *string `json:"platform,omitempty"`
	FirstSeenAt     string  `json:"firstSeenAt"`
	LastSeenAt      string  `json:"lastSeenAt"`
}

func deviceResponseFrom(d Device) deviceResponse {
	// A nil Metadata map would marshal to `null`; callers expect the empty object instead.
	metadata := d.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	return deviceResponse{
		EasClientId:     d.EASClientID,
		Metadata:        metadata,
		CountryCode:     d.CountryCode,
		City:            d.City,
		Lat:             d.Lat,
		Lng:             d.Lng,
		DeviceModel:     d.DeviceModel,
		OsName:          d.OSName,
		OsVersion:       d.OSVersion,
		CurrentUpdateId: d.CurrentUpdateID,
		Branch:          d.Branch,
		RuntimeVersion:  d.RuntimeVersion,
		Platform:        d.Platform,
		FirstSeenAt:     d.FirstSeenAt.UTC().Format(time.RFC3339),
		LastSeenAt:      d.LastSeenAt.UTC().Format(time.RFC3339),
	}
}

func (h *IdentityHandler) GetSchemaHandler(w http.ResponseWriter, r *http.Request) {
	service, ok := h.requireService(w)
	if !ok {
		return
	}
	appID := mux.Vars(r)["APP_ID"]
	schema, err := service.GetSchema(r.Context(), appID)
	if err != nil {
		renderIdentityServiceError(w, err)
		return
	}
	// Stable order so the dashboard list does not jitter between reads.
	keys := make([]schemaKeyResponse, 0, len(schema))
	for _, spec := range schema {
		keys = append(keys, schemaKeyResponseFrom(spec))
	}
	sortSchemaKeys(keys)
	handlers.RenderJSON(w, http.StatusOK, map[string]any{"keys": keys})
}

func (h *IdentityHandler) UpsertSchemaKeyHandler(w http.ResponseWriter, r *http.Request) {
	service, ok := h.requireService(w)
	if !ok {
		return
	}
	appID := mux.Vars(r)["APP_ID"]
	key := mux.Vars(r)["KEY"]

	var body struct {
		Type      string `json:"type"`
		MaxLength int    `json:"maxLength"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	spec := KeySpec{Key: key, Type: ValueType(body.Type), MaxLength: body.MaxLength}
	if spec.MaxLength == 0 {
		spec.MaxLength = DefaultMaxLength
	}
	// Validate before the store so a bad spec is a clear 400, not a 500.
	if err := ValidateKeySpec(spec); err != nil {
		handlers.RenderError(w, http.StatusBadRequest, err.Error())
		return
	}
	saved, err := service.UpsertSchemaKey(r.Context(), appID, spec)
	if err != nil {
		renderIdentityServiceError(w, err)
		return
	}
	handlers.RenderJSON(w, http.StatusOK, schemaKeyResponseFrom(saved))
}

func (h *IdentityHandler) DeleteSchemaKeyHandler(w http.ResponseWriter, r *http.Request) {
	service, ok := h.requireService(w)
	if !ok {
		return
	}
	appID := mux.Vars(r)["APP_ID"]
	key := mux.Vars(r)["KEY"]
	deleted, err := service.DeleteSchemaKey(r.Context(), appID, key)
	if err != nil {
		renderIdentityServiceError(w, err)
		return
	}
	if !deleted {
		handlers.RenderError(w, http.StatusNotFound, "No such identity key.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *IdentityHandler) SearchValuesHandler(w http.ResponseWriter, r *http.Request) {
	service, ok := h.requireService(w)
	if !ok {
		return
	}
	appID := mux.Vars(r)["APP_ID"]
	query := r.URL.Query()
	key := query.Get("key")
	if key == "" {
		handlers.RenderError(w, http.StatusBadRequest, "Query parameter 'key' is required.")
		return
	}
	limit := parseLimit(query.Get("limit"), 20)
	values, err := service.SearchMetadataValues(r.Context(), appID, key, query.Get("search"), limit)
	if err != nil {
		renderIdentityServiceError(w, err)
		return
	}
	out := make([]ValueCount, 0, len(values))
	out = append(out, values...) // To init with [] instead of nil for the renderJSON
	handlers.RenderJSON(w, http.StatusOK, map[string]any{"values": out})
}

// parseDeviceQuery reads the filter parameters shared by the inventory page and the online
// count. It renders the 400 itself and reports false when the request is not answerable.
func parseDeviceQuery(w http.ResponseWriter, r *http.Request, service *Service, appID string) (DeviceQuery, bool) {
	query := r.URL.Query()

	// Repeated parameters, never a separator: a hardware identifier carries a comma of its own.
	tooMany := false
	values := func(name string) []string {
		out := make([]string, 0, len(query[name]))
		for _, raw := range query[name] {
			if trimmed := strings.TrimSpace(raw); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		if len(out) > maxDeviceFilterValues {
			tooMany = true
			return nil
		}
		return out
	}
	deviceQuery := DeviceQuery{
		EASClientIDs:     values("easClientId"),
		CurrentUpdateIDs: values("updateId"),
		UpdateGroupIDs:   values("updateGroupId"),
		Branches:         values("branch"),
		RuntimeVersions:  values("runtimeVersion"),
		Platforms:        values("platform"),
		DeviceModels:     values("deviceModel"),
		OSNames:          values("osName"),
		OSVersions:       values("osVersion"),
		CountryCodes:     values("countryCode"),
	}
	pairs := values("attr")
	if tooMany {
		handlers.RenderError(
			w,
			http.StatusBadRequest,
			"A device filter carries too many values.",
		)
		return DeviceQuery{}, false
	}
	if len(pairs) > 0 {
		schema, err := service.GetSchema(r.Context(), appID)
		if err != nil {
			renderIdentityServiceError(w, err)
			return DeviceQuery{}, false
		}
		// An undeclared key or a bad-fit value is a 400, not an empty list.
		filters, err := ParseFilterPairs(schema, pairs)
		if err != nil {
			handlers.RenderError(w, http.StatusBadRequest, "'attr' must be key:value pairs of declared Identity attributes.")
			return DeviceQuery{}, false
		}
		deviceQuery.Metadata = filters
	}
	// A malformed update id is a 400, not a silently dropped filter.
	for name, ids := range map[string][]string{
		"updateId":      deviceQuery.CurrentUpdateIDs,
		"updateGroupId": deviceQuery.UpdateGroupIDs,
		"easClientId":   deviceQuery.EASClientIDs,
	} {
		for _, id := range ids {
			if _, err := uuid.Parse(id); err != nil {
				handlers.RenderError(w, http.StatusBadRequest, "'"+name+"' must be a UUID.")
				return DeviceQuery{}, false
			}
		}
	}
	return deviceQuery, true
}

func (h *IdentityHandler) ListDevicesHandler(w http.ResponseWriter, r *http.Request) {
	service, ok := h.requireService(w)
	if !ok {
		return
	}
	appID := mux.Vars(r)["APP_ID"]
	query := r.URL.Query()

	deviceQuery, ok := parseDeviceQuery(w, r, service, appID)
	if !ok {
		return
	}

	cursor, err := decodeDeviceCursor(query.Get("cursor"))
	if err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "Invalid cursor.")
		return
	}
	limit := parseLimit(query.Get("limit"), DefaultDevicesPageSize)

	devices, next, err := service.ListDevices(r.Context(), appID, deviceQuery, limit, cursor)
	if err != nil {
		renderIdentityServiceError(w, err)
		return
	}
	items := make([]deviceResponse, 0, len(devices))
	for _, d := range devices {
		items = append(items, deviceResponseFrom(d))
	}
	handlers.RenderJSON(w, http.StatusOK, map[string]any{
		"devices":    items,
		"nextCursor": encodeDeviceCursor(next),
	})
}

// OnlineDevicesHandler answers "how many devices are live right now", taking the same
// filters as the inventory.
func (h *IdentityHandler) OnlineDevicesHandler(w http.ResponseWriter, r *http.Request) {
	service, ok := h.requireService(w)
	if !ok {
		return
	}
	appID := mux.Vars(r)["APP_ID"]
	deviceQuery, ok := parseDeviceQuery(w, r, service, appID)
	if !ok {
		return
	}
	window := DefaultOnlineWindow
	if raw := r.URL.Query().Get("minutes"); raw != "" {
		minutes, err := strconv.Atoi(raw)
		if err != nil || minutes < 1 {
			handlers.RenderError(w, http.StatusBadRequest, "'minutes' must be a positive integer.")
			return
		}
		// Clamped before the multiplication: an unbounded minutes value can overflow int64.
		if maximum := int(MaxOnlineWindow / time.Minute); minutes > maximum {
			minutes = maximum
		}
		window = time.Duration(minutes) * time.Minute
	}
	count, err := service.CountOnlineDevices(r.Context(), appID, window, deviceQuery)
	if err != nil {
		renderIdentityServiceError(w, err)
		return
	}
	handlers.RenderJSON(w, http.StatusOK, map[string]any{
		"online":        count,
		"windowMinutes": int(min(window, MaxOnlineWindow).Minutes()),
	})
}

func (h *IdentityHandler) GetDeviceHandler(w http.ResponseWriter, r *http.Request) {
	service, ok := h.requireService(w)
	if !ok {
		return
	}
	appID := mux.Vars(r)["APP_ID"]
	easClientID := mux.Vars(r)["EAS_CLIENT_ID"]
	// A non-UUID path segment is definitionally not a device.
	if _, err := uuid.Parse(easClientID); err != nil {
		handlers.RenderError(w, http.StatusNotFound, "No such device.")
		return
	}
	device, err := service.GetDevice(r.Context(), appID, easClientID)
	if err != nil {
		renderIdentityServiceError(w, err)
		return
	}
	if device == nil {
		handlers.RenderError(w, http.StatusNotFound, "No such device.")
		return
	}
	handlers.RenderJSON(w, http.StatusOK, deviceResponseFrom(*device))
}

func parseLimit(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n // the store clamps to its own bounds
}

func sortSchemaKeys(keys []schemaKeyResponse) {
	slices.SortFunc(keys, func(left, right schemaKeyResponse) int {
		return strings.Compare(left.Key, right.Key)
	})
}

// encodeDeviceCursor makes the cursor opaque on the wire: base64 of "RFC3339Nano|uuid".
func encodeDeviceCursor(c *DeviceCursor) *string {
	if c == nil {
		return nil
	}
	raw := c.LastSeenAt.UTC().Format(time.RFC3339Nano) + "|" + c.EASClientID
	encoded := base64.RawURLEncoding.EncodeToString([]byte(raw))
	return &encoded
}

func decodeDeviceCursor(encoded string) (*DeviceCursor, error) {
	if encoded == "" {
		return nil, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	parts := strings.SplitN(string(decoded), "|", 2)
	if len(parts) != 2 {
		return nil, errors.New("malformed cursor")
	}
	ts, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return nil, err
	}
	// Validated here so a tampered cursor is a 400, not a 500 from the store's parse.
	if _, err := uuid.Parse(parts[1]); err != nil {
		return nil, err
	}
	return &DeviceCursor{LastSeenAt: ts, EASClientID: parts[1]}, nil
}

// maxDeviceFilterValues bounds one device-inventory filter list.
const maxDeviceFilterValues = 100

// maxHealthUpdateIDs bounds one update-health request.
const maxHealthUpdateIDs = 100

type updateHealthResponse struct {
	DevicesOnUpdate int64 `json:"devicesOnUpdate"`
	// SuccessfulDevices currently run the update and never reported it as faulty.
	SuccessfulDevices int64 `json:"successfulDevices"`
	// FaultyDevices is counted per device, whether it reported a rollback, a JS crash, or both.
	FaultyDevices int64 `json:"faultyDevices"`
	// LaunchFailures duplicates faultyDevices, kept for API compatibility.
	LaunchFailures int64 `json:"launchFailures"`
	// UpdateIssues and RuntimeIssues are NOT a partition of faultyDevices: a device reporting
	// both is counted in each, so their sum can exceed faultyDevices.
	UpdateIssues  int64 `json:"updateIssues"`
	RuntimeIssues int64 `json:"runtimeIssues"`
	// HealthPercent is healthy/attempts over devices that attempted the update; nil if none did.
	HealthPercent *float64 `json:"healthPercent"`
}

// UpdateHealthHandler serves GET .../identity/update-health?ids=uuid,uuid. Every id gets an
// entry, zeroes when nothing was recorded for it.
func (h *IdentityHandler) UpdateHealthHandler(w http.ResponseWriter, r *http.Request) {
	service, ok := h.requireService(w)
	if !ok {
		return
	}
	appID := mux.Vars(r)["APP_ID"]
	rawIDs := strings.Split(r.URL.Query().Get("ids"), ",")
	ids := make([]string, 0, len(rawIDs))
	for _, raw := range rawIDs {
		if trimmed := strings.TrimSpace(raw); trimmed != "" {
			ids = append(ids, trimmed)
		}
	}
	if len(ids) == 0 {
		handlers.RenderError(w, http.StatusBadRequest, "Query parameter 'ids' is required.")
		return
	}
	if len(ids) > maxHealthUpdateIDs {
		handlers.RenderError(w, http.StatusBadRequest, "Too many update ids in one request.")
		return
	}

	health, err := service.UpdateHealthByIDs(r.Context(), appID, ids)
	if err != nil {
		renderIdentityServiceError(w, err)
		return
	}
	out := make(map[string]updateHealthResponse, len(ids))
	for _, id := range ids {
		parsed, err := uuid.Parse(id)
		if err != nil {
			continue // non-UUID input: no entry, never an error
		}
		entry := health[parsed.String()]
		failures := entry.FaultyDevices
		successes := entry.DevicesOnUpdate - entry.FailedStillOn
		response := updateHealthResponse{
			DevicesOnUpdate:   entry.DevicesOnUpdate,
			SuccessfulDevices: successes,
			FaultyDevices:     failures,
			LaunchFailures:    failures,
			UpdateIssues:      entry.UpdateIssues,
			RuntimeIssues:     entry.RuntimeIssues,
		}
		// successfulDevices excludes the overlap so successes/(successes+faulty) counts each device once.
		if attempts := successes + failures; attempts > 0 {
			percent := 100 * float64(successes) / float64(attempts)
			response.HealthPercent = &percent
		}
		out[parsed.String()] = response
	}
	handlers.RenderJSON(w, http.StatusOK, map[string]any{"updates": out})
}
