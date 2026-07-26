// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package identity

import (
	"context"
	"fmt"
	"sort"
	"time"

	"expo-open-ota/ee/licensing"
)

// Op is one identity operation as carried on the wire (the log event name).
// The vocabulary mirrors PostHog/Mixpanel/Amplitude on purpose: $set merges,
// $set_once only fills absent keys, $unset removes. There is deliberately no
// reset(): the eas_client_id cannot rotate, so logout is expressed as $unset.
type Op string

const (
	OpSet     Op = "$set"
	OpSetOnce Op = "$set_once"
	OpUnset   Op = "$unset"
)

// IsIdentityOp reports whether a log event name is one of the identity
// operations; the ingest route uses it to route identity events away from the
// telemetry path.
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
	// TouchDevice registers a passive check-in (manifest poll, telemetry
	// batch): bump-or-register, uncapped, the whole fleet is the registry.
	// currentUpdateID nil = this check-in does not know, keep the known value.
	TouchDevice(ctx context.Context, appID string, easClientID string, geo *Geo, current *CurrentUpdate, device DeviceInfo) error
	// RecordUpdateFailures stores failures per (device, update), fatal_error
	// and failure_type captured once.
	RecordUpdateFailures(ctx context.Context, appID string, easClientID string, updateIDs []string, fatalError string, failureType FailureType) error
	// RecordRuntimeFailure and ResolveRuntimeFailure apply timestamped JS
	// session transitions. Source event time makes offline batch delivery
	// order harmless; a crash wins ties.
	RecordRuntimeFailure(ctx context.Context, appID string, easClientID string, updateID string, fatalError string, occurredAt time.Time) error
	ResolveRuntimeFailure(ctx context.Context, appID string, easClientID string, updateID string, occurredAt time.Time) error
}

// FailureType tags where a device_update_failures row came from, which is
// also what it means for the device's current update:
//
//	FailureTypeUpdate   manifest error-recovery headers: crash at launch,
//	                    the device ROLLED BACK off the update.
//	FailureTypeRuntime  the expo_open_ota_js_crash observe event: a JS crash
//	                    while running the update, which expo-updates never
//	                    reports; the device KEEPS RUNNING the update.
type FailureType string

const (
	FailureTypeUpdate  FailureType = "update_issue"
	FailureTypeRuntime FailureType = "runtime_issue"
)

// Store is the full data surface the service needs: the ingest write path plus
// the dashboard read/CRUD queries. *PostgresIdentityStore implements it. The
// service is the single owner of the store: both the ingest route and the
// dashboard handler go through it.
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

// Service owns the store and the geo resolver. The ingest route calls Apply;
// the dashboard handler calls the read/CRUD methods below. The route/handler
// own transport (decoding, response codes); the service owns semantics.
type Service struct {
	store Store
	geo   GeoResolver
	// licenseValid is the live licensing state; a field so same-package tests
	// can pin it without minting signed keys.
	licenseValid func() bool
}

// NewService builds the identity service. geo may be nil: identity works
// without a GeoLite2 database, devices simply stay unlocated. The license gate
// defaults to licensing.IsEnterprise and is imported directly ON PURPOSE: it
// must live in EE code so bypassing it means editing an EE-licensed file (a
// license violation), not swapping a func passed in from the MIT composition
// root (which anyone may legally replace with `func() bool { return true }`).
func NewService(store Store, geo GeoResolver) *Service {
	return &Service{store: store, geo: geo, licenseValid: licensing.IsEnterprise}
}

// Enabled reports whether custom attributes are being collected right now.
// The device registry itself is community: what an Enterprise license buys is
// the operator-defined metadata on top of it, so only the allowlist and the
// values attached through it answer to this.
func (s *Service) Enabled() bool {
	return s.store != nil && s.licenseValid()
}

// Dashboard read/CRUD surface: thin delegations, the store owns semantics.

func (s *Service) GetSchema(ctx context.Context, appID string) (Schema, error) {
	return s.store.GetSchema(ctx, appID)
}

// Declaring a key is what turns the feature on for an app, so it is the write
// the license gates. Deleting one stays open below: an operator whose license
// lapsed must be able to stop using the feature, and refusing the cleanup path
// would only strand rows nobody can reach.
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

// CountOnlineDevices counts devices seen within the window, whatever contacted
// the server: a manifest poll counts exactly as much as a telemetry batch, so
// this works on a fleet with no expo-observe at all. The query narrows it on
// the same dimensions as ListDevices, so the count can sit next to a filtered
// view without contradicting it.
func (s *Service) CountOnlineDevices(ctx context.Context, appID string, window time.Duration, query DeviceQuery) (int64, error) {
	if window <= 0 {
		window = DefaultOnlineWindow
	}
	if window > MaxOnlineWindow {
		window = MaxOnlineWindow
	}
	return s.store.CountOnlineDevices(ctx, appID, time.Now().UTC().Add(-window), query)
}

// UpdateHealthByIDs serves the dashboard's update-health display: MAU and
// launch failures per update, straight from the Postgres registry.
func (s *Service) UpdateHealthByIDs(ctx context.Context, appID string, updateIDs []string) (map[string]UpdateHealth, error) {
	return s.store.UpdateHealthByIDs(ctx, appID, updateIDs)
}

// DeviceInfo is the hardware and OS a device reports, named after the fields
// expo-device exposes to app authors (modelName, osName, osVersion) so the
// dashboard, the SDK and the registry all use one vocabulary. Only telemetry
// carries it: the manifest headers say nothing about hardware, so every field
// is optional and an empty one means "not reported", never "changed to empty".
// CurrentUpdate is the update a check-in reports the device is running, and
// WHEN it saw that. The two travel together because one is worthless without
// the other: a telemetry backlog reports an update the device may have left
// since, and only the observation time can tell that apart from fresh news.
type CurrentUpdate struct {
	ID string
	// ObservedAt is when the device was running it, not when this arrived. For
	// telemetry it is the newest record of the batch; for a manifest poll it is
	// the poll itself. Never in the future: the store compares it against what
	// it already recorded, so a device with a skewed clock would otherwise
	// freeze its own registry entry until real time caught up.
	ObservedAt time.Time
}

type DeviceInfo struct {
	Model     string
	OSName    string
	OSVersion string
	// AppVersion is the store version of the binary, not the OTA update: two
	// devices on the same update can sit on different builds.
	AppVersion string
}

func (d DeviceInfo) IsZero() bool {
	return d.Model == "" && d.OSName == "" && d.OSVersion == "" && d.AppVersion == ""
}

// PlaceOf resolves the country and the city centroid of a request IP.
// Telemetry ingestion denormalizes both onto every row, and the resolver is
// optional: without a GeoLite2 database this returns a zero Place, which every
// consumer reads as "not resolved" rather than as a location. The two travel
// together because they come from one lookup, and separately because GeoLite2
// routinely places an address block in a country without placing it in a city.
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

// TouchDevice is Apply's passive sibling: every check-in a device makes with
// the server registers it (metadata untouched), so device_identity is the
// universal device registry and the identity ops only layer metadata on top.
// The geo enrichment rides along exactly as on Apply.
func (s *Service) TouchDevice(ctx context.Context, appID string, easClientID string, remoteIP string, current *CurrentUpdate, device DeviceInfo) error {
	var geo *Geo
	if s.geo != nil && remoteIP != "" {
		geo = s.geo.Resolve(remoteIP)
	}
	return s.store.TouchDevice(ctx, appID, easClientID, geo, current, device)
}

// RecordUpdateFailures is the failure sink for both sources (manifest error
// recovery, expo_open_ota_js_crash events): failures land in Postgres so
// update health works with no ClickHouse and no SDK.
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

	// Without a license the attributes are dropped, but the device is still
	// registered: the registry (and the geo behind it) is community, and a
	// device that identifies itself must not go missing from it. $unset stays
	// whole for the same reason it bypasses the allowlist: it is the path that
	// removes data, never the one that collects it.
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
		// The op sentinel keeps the label set bounded: req.Op is wire input
		// here, not one of our constants.
		err = fmt.Errorf("unknown identity op %q", req.Op)
		observeApply(req.AppID, Op("unknown"), err, 0, time.Since(start))
		return ApplyResult{}, err
	}

	// The store saw no attributes to drop, so the gate reports its own: a
	// counter reading zero here would hide the reason nothing is collected.
	// Only the writing ops: $unset never stores attributes, so listing its
	// payload as dropped would count a loss that never happened.
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
