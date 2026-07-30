// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package identity

import (
	"context"
	"fmt"
	"sort"
	"time"

	"xprem/ee/licensing"
)

// Op is one identity operation as carried on the wire (the log event name): $set merges,
// $set_once only fills absent keys, $unset removes.
type Op string

const (
	OpSet     Op = "$set"
	OpSetOnce Op = "$set_once"
	OpUnset   Op = "$unset"
)

// IsIdentityOp reports whether a log event name is one of the identity operations.
func IsIdentityOp(eventName string) bool {
	switch Op(eventName) {
	case OpSet, OpSetOnce, OpUnset:
		return true
	}
	return false
}

// IdentityMutator is the ingest write path: $set / $set_once / $unset with
// geo enrichment. Kept as its own interface so the vocabulary stays explicit.
type IdentityMutator interface {
	ApplySet(ctx context.Context, appID string, easClientID string, raw map[string]any, geo *Geo) (ApplyResult, error)
	ApplySetOnce(ctx context.Context, appID string, easClientID string, raw map[string]any, geo *Geo) (ApplyResult, error)
	ApplyUnset(ctx context.Context, appID string, easClientID string, keys []string, geo *Geo) (ApplyResult, error)
	// TouchDevice registers a passive check-in (manifest poll, telemetry batch); current nil
	// means this check-in does not know the update, so the known value is kept.
	TouchDevice(ctx context.Context, appID string, easClientID string, geo *Geo, current *CurrentUpdate, device DeviceInfo) error
	RecordUpdateFailures(ctx context.Context, appID string, easClientID string, updateIDs []string, fatalError string, failureType FailureType) error
	// RecordRuntimeFailure and ResolveRuntimeFailure use the source event time, so offline
	// batch delivery order is harmless; a crash wins ties.
	RecordRuntimeFailure(ctx context.Context, appID string, easClientID string, updateID string, fatalError string, occurredAt time.Time) error
	ResolveRuntimeFailure(ctx context.Context, appID string, easClientID string, updateID string, occurredAt time.Time) error
}

// FailureType tags where a device_update_failures row came from: FailureTypeUpdate is a
// launch rollback (manifest error-recovery), FailureTypeRuntime is a JS crash while still
// running the update.
type FailureType string

const (
	FailureTypeUpdate  FailureType = "update_issue"
	FailureTypeRuntime FailureType = "runtime_issue"
)

// Store is the full data surface the service needs: the ingest write path plus the
// dashboard read/CRUD queries.
type Store interface {
	IdentityMutator
	GetSchema(ctx context.Context, appID string) (Schema, error)
	UpsertSchemaKey(ctx context.Context, appID string, spec KeySpec) (KeySpec, error)
	DeleteSchemaKey(ctx context.Context, appID string, key string) (bool, error)
	SearchMetadataValues(ctx context.Context, appID string, key string, search string, limit int) ([]ValueCount, error)
	ListDevices(ctx context.Context, appID string, query DeviceQuery, limit int, cursor *DeviceCursor) ([]Device, *DeviceCursor, error)
	GetDevice(ctx context.Context, appID string, easClientID string) (*Device, error)
	CountOnlineDevices(ctx context.Context, appID string, since time.Time, query DeviceQuery) (int64, error)
	UpdateHealthByIDs(ctx context.Context, appID string, updateIDs []string) (map[string]UpdateHealth, error)
}

// Service owns the store and the geo resolver. The ingest route calls Apply; the dashboard
// handler calls the read/CRUD methods below.
type Service struct {
	store Store
	geo   GeoResolver
	// licenseValid is a field, not a direct call, so tests can pin it without a signed key.
	licenseValid func() bool
}

// NewService builds the identity service; geo may be nil, in which case devices stay unlocated.
func NewService(store Store, geo GeoResolver) *Service {
	return &Service{store: store, geo: geo, licenseValid: licensing.IsEnterprise}
}

// Enabled reports whether custom attributes are being collected right now; the device
// registry itself is community and always works.
func (s *Service) Enabled() bool {
	return s.store != nil && s.licenseValid()
}

func (s *Service) GetSchema(ctx context.Context, appID string) (Schema, error) {
	return s.store.GetSchema(ctx, appID)
}

// UpsertSchemaKey is license gated; DeleteSchemaKey is not, so a lapsed license can still
// be cleaned up.
func (s *Service) UpsertSchemaKey(ctx context.Context, appID string, spec KeySpec) (KeySpec, error) {
	if !s.Enabled() {
		return KeySpec{}, ErrRequiresValidLicense
	}
	return s.store.UpsertSchemaKey(ctx, appID, spec)
}

func (s *Service) DeleteSchemaKey(ctx context.Context, appID string, key string) (bool, error) {
	return s.store.DeleteSchemaKey(ctx, appID, key)
}

func (s *Service) SearchMetadataValues(ctx context.Context, appID string, key string, search string, limit int) ([]ValueCount, error) {
	return s.store.SearchMetadataValues(ctx, appID, key, search, limit)
}

func (s *Service) ListDevices(ctx context.Context, appID string, query DeviceQuery, limit int, cursor *DeviceCursor) ([]Device, *DeviceCursor, error) {
	return s.store.ListDevices(ctx, appID, query, limit, cursor)
}

func (s *Service) GetDevice(ctx context.Context, appID string, easClientID string) (*Device, error) {
	return s.store.GetDevice(ctx, appID, easClientID)
}

// CountOnlineDevices counts devices seen within the window, whatever contacted the server
// (manifest poll or telemetry). The query narrows it on the same dimensions as ListDevices.
func (s *Service) CountOnlineDevices(ctx context.Context, appID string, window time.Duration, query DeviceQuery) (int64, error) {
	if window <= 0 {
		window = DefaultOnlineWindow
	}
	if window > MaxOnlineWindow {
		window = MaxOnlineWindow
	}
	return s.store.CountOnlineDevices(ctx, appID, time.Now().UTC().Add(-window), query)
}

// UpdateHealthByIDs serves the dashboard's update-health display: MAU and launch failures per update.
func (s *Service) UpdateHealthByIDs(ctx context.Context, appID string, updateIDs []string) (map[string]UpdateHealth, error) {
	return s.store.UpdateHealthByIDs(ctx, appID, updateIDs)
}

// CurrentUpdate is the update a check-in reports the device is running, and when it saw that.
type CurrentUpdate struct {
	ID string
	// ObservedAt must never be in the future, or a device with a skewed clock would freeze
	// its own registry entry until real time caught up.
	ObservedAt time.Time
}

// DeviceInfo is the hardware and OS a device reports; only telemetry carries it, so every
// field is optional and empty means "not reported".
type DeviceInfo struct {
	Model     string
	OSName    string
	OSVersion string
	// AppVersion is the store version of the binary, not the OTA update.
	AppVersion string
}

func (d DeviceInfo) IsZero() bool {
	return d.Model == "" && d.OSName == "" && d.OSVersion == "" && d.AppVersion == ""
}

// PlaceOf resolves the country and the city centroid of a request IP; without a GeoLite2
// database it returns a zero Place, read as "not resolved".
func (s *Service) PlaceOf(remoteIP string) Place {
	if s == nil || s.geo == nil || remoteIP == "" {
		return Place{}
	}
	geo := s.geo.Resolve(remoteIP)
	if geo == nil {
		return Place{}
	}
	place := Place{Lat: geo.Lat, Lng: geo.Lng}
	if geo.CountryCode != nil {
		place.CountryCode = *geo.CountryCode
	}
	return place
}

// TouchDevice is Apply's passive sibling: every check-in a device makes registers it in
// device_identity, without touching its metadata.
func (s *Service) TouchDevice(ctx context.Context, appID string, easClientID string, remoteIP string, current *CurrentUpdate, device DeviceInfo) error {
	var geo *Geo
	if s.geo != nil && remoteIP != "" {
		geo = s.geo.Resolve(remoteIP)
	}
	return s.store.TouchDevice(ctx, appID, easClientID, geo, current, device)
}

// RecordUpdateFailures is the failure sink for both sources: manifest error recovery and
// xprem_js_crash events.
func (s *Service) RecordUpdateFailures(ctx context.Context, appID string, easClientID string, updateIDs []string, fatalError string, failureType FailureType) error {
	return s.store.RecordUpdateFailures(ctx, appID, easClientID, updateIDs, fatalError, failureType)
}

func (s *Service) RecordRuntimeFailure(ctx context.Context, appID string, easClientID string, updateID string, fatalError string, occurredAt time.Time) error {
	return s.store.RecordRuntimeFailure(ctx, appID, easClientID, updateID, fatalError, occurredAt)
}

func (s *Service) ResolveRuntimeFailure(ctx context.Context, appID string, easClientID string, updateID string, occurredAt time.Time) error {
	return s.store.ResolveRuntimeFailure(ctx, appID, easClientID, updateID, occurredAt)
}

// Request is one identity operation extracted from a log event.
type Request struct {
	AppID       string
	EASClientID string
	Op          Op
	// Attributes carries the key/value payload of $set and $set_once.
	Attributes map[string]any
	// UnsetKeys carries the key names of $unset.
	UnsetKeys []string
	// RemoteIP is the already-resolved client IP of the HTTP request that
	// delivered the batch (proxy handling happens upstream).
	RemoteIP string
}

func (s *Service) Apply(ctx context.Context, req Request) (ApplyResult, error) {
	start := time.Now()

	var geo *Geo
	if s.geo != nil && req.RemoteIP != "" {
		geo = s.geo.Resolve(req.RemoteIP)
	}

	// Without a license, attributes are dropped but the device is still registered ($unset
	// keeps its keys, since it only ever removes data).
	attributes := req.Attributes
	if !s.Enabled() {
		attributes = nil
	}

	var result ApplyResult
	var err error
	switch req.Op {
	case OpSet:
		result, err = s.store.ApplySet(ctx, req.AppID, req.EASClientID, attributes, geo)
	case OpSetOnce:
		result, err = s.store.ApplySetOnce(ctx, req.AppID, req.EASClientID, attributes, geo)
	case OpUnset:
		result, err = s.store.ApplyUnset(ctx, req.AppID, req.EASClientID, req.UnsetKeys, geo)
	default:
		// req.Op is wire input here, not one of our constants.
		err = fmt.Errorf("unknown identity op %q", req.Op)
		observeApply(req.AppID, Op("unknown"), err, 0, time.Since(start))
		return ApplyResult{}, err
	}

	// Only the writing ops report drops here; $unset never stores attributes to drop.
	writes := req.Op == OpSet || req.Op == OpSetOnce
	if writes && attributes == nil && len(req.Attributes) > 0 && err == nil {
		for key := range req.Attributes {
			result.DroppedKeys = append(result.DroppedKeys, key)
		}
		sort.Strings(result.DroppedKeys)
	}

	observeApply(req.AppID, req.Op, err, len(result.DroppedKeys), time.Since(start))
	if err != nil {
		return ApplyResult{}, err
	}
	return result, nil
}
