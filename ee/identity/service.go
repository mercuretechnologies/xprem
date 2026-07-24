// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package identity

import (
	"context"
	"fmt"
	"time"
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
	// TouchDevice registers a passive contact (manifest poll, telemetry
	// batch): bump-or-register, uncapped, the whole fleet is the registry.
	// currentUpdateID nil = this contact does not know, keep the known value.
	TouchDevice(ctx context.Context, appID string, easClientID string, geo *Geo, currentUpdateID *string) error
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
	ListDevices(ctx context.Context, appID string, filter *MetadataFilter, limit int, cursor *DeviceCursor) ([]Device, *DeviceCursor, error)
	GetDevice(ctx context.Context, appID string, easClientID string) (*Device, error)
	UpdateHealthByIDs(ctx context.Context, appID string, updateIDs []string) (map[string]UpdateHealth, error)
}

// Service owns the store and the geo resolver. The ingest route calls Apply;
// the dashboard handler calls the read/CRUD methods below. The route/handler
// own transport (decoding, response codes); the service owns semantics.
type Service struct {
	store Store
	geo   GeoResolver
}

// NewService builds the identity service. geo may be nil: identity works
// without a GeoLite2 database, devices simply stay unlocated.
func NewService(store Store, geo GeoResolver) *Service {
	return &Service{store: store, geo: geo}
}

// Dashboard read/CRUD surface: thin delegations, the store owns semantics.

func (s *Service) GetSchema(ctx context.Context, appID string) (Schema, error) {
	return s.store.GetSchema(ctx, appID)
}

func (s *Service) UpsertSchemaKey(ctx context.Context, appID string, spec KeySpec) (KeySpec, error) {
	return s.store.UpsertSchemaKey(ctx, appID, spec)
}

func (s *Service) DeleteSchemaKey(ctx context.Context, appID string, key string) (bool, error) {
	return s.store.DeleteSchemaKey(ctx, appID, key)
}

func (s *Service) SearchMetadataValues(ctx context.Context, appID string, key string, search string, limit int) ([]ValueCount, error) {
	return s.store.SearchMetadataValues(ctx, appID, key, search, limit)
}

func (s *Service) ListDevices(ctx context.Context, appID string, filter *MetadataFilter, limit int, cursor *DeviceCursor) ([]Device, *DeviceCursor, error) {
	return s.store.ListDevices(ctx, appID, filter, limit, cursor)
}

func (s *Service) GetDevice(ctx context.Context, appID string, easClientID string) (*Device, error) {
	return s.store.GetDevice(ctx, appID, easClientID)
}

// UpdateHealthByIDs serves the dashboard's update-health display: MAU and
// launch failures per update, straight from the Postgres registry.
func (s *Service) UpdateHealthByIDs(ctx context.Context, appID string, updateIDs []string) (map[string]UpdateHealth, error) {
	return s.store.UpdateHealthByIDs(ctx, appID, updateIDs)
}

// TouchDevice is Apply's passive sibling: every contact a device makes with
// the server registers it (metadata untouched), so device_identity is the
// universal device registry and the identity ops only layer metadata on top.
// The geo enrichment rides along exactly as on Apply.
func (s *Service) TouchDevice(ctx context.Context, appID string, easClientID string, remoteIP string, currentUpdateID *string) error {
	var geo *Geo
	if s.geo != nil && remoteIP != "" {
		geo = s.geo.Resolve(remoteIP)
	}
	return s.store.TouchDevice(ctx, appID, easClientID, geo, currentUpdateID)
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

	var result ApplyResult
	var err error
	switch req.Op {
	case OpSet:
		result, err = s.store.ApplySet(ctx, req.AppID, req.EASClientID, req.Attributes, geo)
	case OpSetOnce:
		result, err = s.store.ApplySetOnce(ctx, req.AppID, req.EASClientID, req.Attributes, geo)
	case OpUnset:
		result, err = s.store.ApplyUnset(ctx, req.AppID, req.EASClientID, req.UnsetKeys, geo)
	default:
		// The op sentinel keeps the label set bounded: req.Op is wire input
		// here, not one of our constants.
		err = fmt.Errorf("unknown identity op %q", req.Op)
		observeApply(req.AppID, Op("unknown"), err, 0, time.Since(start))
		return ApplyResult{}, err
	}

	observeApply(req.AppID, req.Op, err, len(result.DroppedKeys), time.Since(start))
	if err != nil {
		return ApplyResult{}, err
	}
	return result, nil
}
