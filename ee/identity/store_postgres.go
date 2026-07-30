// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"time"
	"xprem/internal/database"
	"xprem/internal/database/postgres/pgdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

type PostgresIdentityStore struct {
	engine *database.Engine
}

func NewPostgresIdentityStore(engine *database.Engine) *PostgresIdentityStore {
	return &PostgresIdentityStore{engine: engine}
}

// toPgUUID surfaces a parse failure as an error, never a zero UUID silently written to the database.
func toPgUUID(id string) (pgtype.UUID, error) {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("invalid uuid %q: %w", id, err)
	}
	return pgtype.UUID{Bytes: parsed, Valid: true}, nil
}

func specFromRow(row pgdb.IdentitySchema) KeySpec {
	return KeySpec{Key: row.Key, Type: ValueType(row.ValueType), MaxLength: int(row.MaxLength)}
}

func schemaFromRows(rows []pgdb.IdentitySchema) Schema {
	schema := make(Schema, len(rows))
	for _, row := range rows {
		schema[row.Key] = specFromRow(row)
	}
	return schema
}

// optionalUUID renders an unset uuid column as nil rather than the zero uuid,
// which downstream would read as a real update id.
func optionalUUID(value pgtype.UUID) *string {
	if !value.Valid {
		return nil
	}
	rendered := uuid.UUID(value.Bytes).String()
	return &rendered
}

func deviceFromRow(row pgdb.DeviceIdentity) (Device, error) {
	metadata := map[string]any{}
	if len(row.Metadata) > 0 {
		if err := json.Unmarshal(row.Metadata, &metadata); err != nil {
			return Device{}, fmt.Errorf("corrupt device metadata: %w", err)
		}
	}
	return Device{
		AppID:           uuid.UUID(row.AppID.Bytes).String(),
		EASClientID:     uuid.UUID(row.EasClientID.Bytes).String(),
		Metadata:        metadata,
		CountryCode:     row.CountryCode,
		City:            row.City,
		Lat:             row.Lat,
		Lng:             row.Lng,
		DeviceModel:     row.DeviceModel,
		OSName:          row.OsName,
		OSVersion:       row.OsVersion,
		CurrentUpdateID: optionalUUID(row.CurrentUpdateID),
		// Recorded at check-in from the update the device reported.
		Branch:         row.BranchName,
		RuntimeVersion: row.RuntimeVersion,
		Platform:       row.Platform,
		FirstSeenAt:    row.FirstSeenAt.Time,
		LastSeenAt:     row.LastSeenAt.Time,
	}, nil
}

// GetSchema returns the app's allowlist; an app with no declared keys gets an empty schema.
func (s *PostgresIdentityStore) GetSchema(ctx context.Context, appID string) (Schema, error) {
	appUUID, err := toPgUUID(appID)
	if err != nil {
		return nil, err
	}
	rows, err := s.engine.Queries.ListIdentitySchemaKeys(ctx, appUUID)
	if err != nil {
		return nil, fmt.Errorf("listing identity schema: %w", err)
	}
	return schemaFromRows(rows), nil
}

func (s *PostgresIdentityStore) UpsertSchemaKey(ctx context.Context, appID string, spec KeySpec) (KeySpec, error) {
	if spec.MaxLength == 0 {
		spec.MaxLength = DefaultMaxLength
	}
	if err := ValidateKeySpec(spec); err != nil {
		return KeySpec{}, err
	}
	appUUID, err := toPgUUID(appID)
	if err != nil {
		return KeySpec{}, err
	}

	var saved KeySpec
	err = s.engine.WithTx(ctx, func(q *pgdb.Queries) error {
		// The cap check runs in the insert's transaction so two concurrent declarations
		// cannot both slip under the limit.
		existing, err := q.ListIdentitySchemaKeys(ctx, appUUID)
		if err != nil {
			return fmt.Errorf("listing identity schema: %w", err)
		}
		if _, declared := schemaFromRows(existing)[spec.Key]; !declared && len(existing) >= MaxSchemaKeys {
			return ErrTooManySchemaKeys
		}
		row, err := q.UpsertIdentitySchemaKey(ctx, pgdb.UpsertIdentitySchemaKeyParams{
			AppID:     appUUID,
			Key:       spec.Key,
			ValueType: string(spec.Type),
			MaxLength: int32(spec.MaxLength),
		})
		if err != nil {
			return fmt.Errorf("upserting identity schema key: %w", err)
		}
		saved = specFromRow(row)
		return nil
	})
	if err != nil {
		return KeySpec{}, err
	}
	return saved, nil
}

// DeleteSchemaKey removes a key from the allowlist and wipes its autocomplete stats in the
// same transaction. Values already merged into device metadata are left in place.
func (s *PostgresIdentityStore) DeleteSchemaKey(ctx context.Context, appID string, key string) (bool, error) {
	appUUID, err := toPgUUID(appID)
	if err != nil {
		return false, err
	}
	var deleted bool
	err = s.engine.WithTx(ctx, func(q *pgdb.Queries) error {
		tag, err := q.DeleteIdentitySchemaKey(ctx, pgdb.DeleteIdentitySchemaKeyParams{AppID: appUUID, Key: key})
		if err != nil {
			return fmt.Errorf("deleting identity schema key: %w", err)
		}
		if err := q.DeleteIdentityValueStatsForKey(ctx, pgdb.DeleteIdentityValueStatsForKeyParams{AppID: appUUID, Key: key}); err != nil {
			return fmt.Errorf("deleting identity value stats: %w", err)
		}
		deleted = tag.RowsAffected() > 0
		return nil
	})
	if err != nil {
		return false, err
	}
	return deleted, nil
}

// statOp is one pending change to identity_value_stats, executed in deterministic
// (key, value) order so two devices sharing a stat row cannot acquire its lock in
// opposite orders and deadlock.
type statOp struct {
	key       string
	value     string
	decrement bool
}

type mutationKind int

const (
	mutationSet mutationKind = iota
	mutationSetOnce
	mutationUnset
)

// ApplySet sanitizes raw wire metadata against the allowlist, merges it into the device row,
// refreshes geo, and keeps the per-value device counts in sync, all in one transaction.
func (s *PostgresIdentityStore) ApplySet(ctx context.Context, appID string, easClientID string, raw map[string]any, geo *Geo) (ApplyResult, error) {
	return s.mutate(ctx, appID, easClientID, mutationSet, raw, nil, geo)
}

// ApplySetOnce is $set_once: a sanitized key is written only when the device
// does not hold it yet; keys already present are silently left untouched
// (same contract as PostHog/Mixpanel/Amplitude).
func (s *PostgresIdentityStore) ApplySetOnce(ctx context.Context, appID string, easClientID string, raw map[string]any, geo *Geo) (ApplyResult, error) {
	return s.mutate(ctx, appID, easClientID, mutationSetOnce, raw, nil, geo)
}

// ApplyUnset removes keys from the device and moves the stat counts down. It works even
// for keys since removed from the allowlist, since it is the cleanup path.
func (s *PostgresIdentityStore) ApplyUnset(ctx context.Context, appID string, easClientID string, keys []string, geo *Geo) (ApplyResult, error) {
	return s.mutate(ctx, appID, easClientID, mutationUnset, nil, keys, geo)
}

// applyStatOps settles a batch of per-value stat mutations in two statements regardless of
// batch size. The sort makes every writer touch (key, value) rows in the same order across
// the whole batch, which is what keeps two devices sharing a stat row from deadlocking.
func applyStatOps(ctx context.Context, q *pgdb.Queries, appUUID pgtype.UUID, ops []statOp) error {
	if len(ops) == 0 {
		return nil
	}
	sort.Slice(ops, func(i, j int) bool {
		if ops[i].key != ops[j].key {
			return ops[i].key < ops[j].key
		}
		if ops[i].value != ops[j].value {
			return ops[i].value < ops[j].value
		}
		return ops[i].decrement
	})

	keys := make([]string, len(ops))
	values := make([]string, len(ops))
	deltas := make([]int32, len(ops))
	for i, op := range ops {
		keys[i], values[i] = op.key, op.value
		if op.decrement {
			deltas[i] = -1
		} else {
			deltas[i] = 1
		}
	}

	if err := q.ApplyIdentityValueStats(ctx, pgdb.ApplyIdentityValueStatsParams{
		AppID: appUUID, Keys: keys, Values: values, Deltas: deltas,
	}); err != nil {
		return fmt.Errorf("applying value stats: %w", err)
	}
	// Sweeps the rows the statement above floored at zero; same rows, already locked, no new ordering.
	if err := q.DeleteZeroIdentityValueStats(ctx, pgdb.DeleteZeroIdentityValueStatsParams{
		AppID: appUUID, Keys: keys, Values: values,
	}); err != nil {
		return fmt.Errorf("pruning zero value stats: %w", err)
	}
	return nil
}

func (s *PostgresIdentityStore) mutate(ctx context.Context, appID string, easClientID string, kind mutationKind, raw map[string]any, unsetKeys []string, geo *Geo) (ApplyResult, error) {
	appUUID, err := toPgUUID(appID)
	if err != nil {
		return ApplyResult{}, err
	}
	clientUUID, err := toPgUUID(easClientID)
	if err != nil {
		return ApplyResult{}, err
	}

	var result ApplyResult
	err = s.engine.WithTx(ctx, func(q *pgdb.Queries) error {
		var sanitized map[string]any
		var dropped []string
		if kind != mutationUnset {
			// Shares the transaction so a concurrent allowlist change cannot mix two schema versions.
			schemaRows, err := q.ListIdentitySchemaKeys(ctx, appUUID)
			if err != nil {
				return fmt.Errorf("listing identity schema: %w", err)
			}
			sanitized, dropped = schemaFromRows(schemaRows).Sanitize(raw)
		}

		if err := q.EnsureDeviceIdentity(ctx, pgdb.EnsureDeviceIdentityParams{AppID: appUUID, EasClientID: clientUUID}); err != nil {
			return fmt.Errorf("ensuring device row: %w", err)
		}
		current, err := q.GetDeviceIdentityForUpdate(ctx, pgdb.GetDeviceIdentityForUpdateParams{AppID: appUUID, EasClientID: clientUUID})
		if err != nil {
			return fmt.Errorf("locking device row: %w", err)
		}
		previous := map[string]any{}
		if len(current.Metadata) > 0 {
			if err := json.Unmarshal(current.Metadata, &previous); err != nil {
				return fmt.Errorf("corrupt device metadata: %w", err)
			}
		}

		merged := make(map[string]any, len(previous)+len(sanitized))
		for key, value := range previous {
			merged[key] = value
		}
		var ops []statOp
		switch kind {
		case mutationSet, mutationSetOnce:
			for key, value := range sanitized {
				oldValue, existed := previous[key]
				if kind == mutationSetOnce && existed {
					continue
				}
				merged[key] = value
				newRendered := RenderValue(value)
				if existed {
					oldRendered := RenderValue(oldValue)
					if oldRendered == newRendered {
						continue
					}
					ops = append(ops, statOp{key: key, value: oldRendered, decrement: true})
				}
				ops = append(ops, statOp{key: key, value: newRendered})
			}
		case mutationUnset:
			for _, key := range unsetKeys {
				oldValue, existed := previous[key]
				if !existed {
					continue
				}
				// Also remove from previous so a duplicated key in the payload cannot double-decrement.
				delete(previous, key)
				delete(merged, key)
				ops = append(ops, statOp{key: key, value: RenderValue(oldValue), decrement: true})
			}
		}
		mergedJSON, err := json.Marshal(merged)
		if err != nil {
			return fmt.Errorf("marshalling merged metadata: %w", err)
		}

		params := pgdb.UpdateDeviceIdentityParams{
			AppID:       appUUID,
			EasClientID: clientUUID,
			Metadata:    mergedJSON,
		}
		if geo != nil {
			params.CountryCode = geo.CountryCode
			params.City = geo.City
			params.Lat = geo.Lat
			params.Lng = geo.Lng
		}
		updated, err := q.UpdateDeviceIdentity(ctx, params)
		if err != nil {
			return fmt.Errorf("updating device row: %w", err)
		}

		if err := applyStatOps(ctx, q, appUUID, ops); err != nil {
			return err
		}

		device, err := deviceFromRow(updated)
		if err != nil {
			return err
		}
		result = ApplyResult{Device: device, DroppedKeys: dropped}
		return nil
	})
	if err != nil {
		return ApplyResult{}, err
	}
	return result, nil
}

// GetDevice returns nil when the install was never seen.
func (s *PostgresIdentityStore) GetDevice(ctx context.Context, appID string, easClientID string) (*Device, error) {
	appUUID, err := toPgUUID(appID)
	if err != nil {
		return nil, err
	}
	clientUUID, err := toPgUUID(easClientID)
	if err != nil {
		return nil, err
	}
	row, err := s.engine.Queries.GetDeviceIdentity(ctx, pgdb.GetDeviceIdentityParams{AppID: appUUID, EasClientID: clientUUID})
	if err != nil {
		if database.IsNoRows(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("getting device identity: %w", err)
	}
	device, err := deviceFromRow(row)
	if err != nil {
		return nil, err
	}
	return &device, nil
}

// convertedDeviceFilters holds the DeviceQuery fields that need converting before a query.
type convertedDeviceFilters struct {
	metadata      [][]byte
	clientIDs     []pgtype.UUID
	updateIDs     []pgtype.UUID
	publishGroups []pgtype.UUID
}

// deviceFilterParams is shared by the inventory page and the online count so both filter
// on exactly the same dimensions.
func deviceFilterParams(query DeviceQuery) (convertedDeviceFilters, error) {
	docs, err := query.Metadata.ContainmentDocs()
	if err != nil {
		return convertedDeviceFilters{}, fmt.Errorf("marshalling device filter: %w", err)
	}
	clientIDs, err := toPgUUIDs(query.EASClientIDs)
	if err != nil {
		return convertedDeviceFilters{}, err
	}
	updateIDs, err := toPgUUIDs(query.CurrentUpdateIDs)
	if err != nil {
		return convertedDeviceFilters{}, err
	}
	publishGroups, err := toPgUUIDs(query.UpdateGroupIDs)
	if err != nil {
		return convertedDeviceFilters{}, err
	}
	return convertedDeviceFilters{
		metadata:      docs,
		clientIDs:     clientIDs,
		updateIDs:     updateIDs,
		publishGroups: publishGroups,
	}, nil
}

// ListDevices returns one page of the device inventory, newest-seen first, keyset-paginated.
// A nil cursor starts at the first page; the returned cursor is nil on the last page.
func (s *PostgresIdentityStore) ListDevices(ctx context.Context, appID string, query DeviceQuery, limit int, cursor *DeviceCursor) ([]Device, *DeviceCursor, error) {
	appUUID, err := toPgUUID(appID)
	if err != nil {
		return nil, nil, err
	}
	switch {
	case limit < 1:
		limit = DefaultDevicesPageSize
	case limit > MaxDevicesPageSize:
		limit = MaxDevicesPageSize
	}

	filters, err := deviceFilterParams(query)
	if err != nil {
		return nil, nil, err
	}
	params := pgdb.ListDevicesParams{
		AppID: appUUID,
		// One extra row detects whether a next page exists.
		Lim:             int32(limit + 1),
		Filters:         filters.metadata,
		EasClientID:     filters.clientIDs,
		CurrentUpdateID: filters.updateIDs,
		PublishGroup:    filters.publishGroups,
		Branch:          query.Branches,
		RuntimeVersion:  query.RuntimeVersions,
		Platform:        query.Platforms,
		DeviceModel:     query.DeviceModels,
		OsName:          query.OSNames,
		OsVersion:       query.OSVersions,
		CountryCode:     query.CountryCodes,
	}
	if cursor != nil {
		params.BeforeLastSeen = pgtype.Timestamptz{Time: cursor.LastSeenAt, Valid: true}
		cursorUUID, err := toPgUUID(cursor.EASClientID)
		if err != nil {
			return nil, nil, err
		}
		params.BeforeClientID = cursorUUID
	}

	rows, err := s.engine.Queries.ListDevices(ctx, params)
	if err != nil {
		return nil, nil, fmt.Errorf("listing devices: %w", err)
	}

	var next *DeviceCursor
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		next = &DeviceCursor{
			LastSeenAt:  last.LastSeenAt.Time,
			EASClientID: uuid.UUID(last.EasClientID.Bytes).String(),
		}
	}
	devices := make([]Device, 0, len(rows))
	for _, row := range rows {
		device, err := deviceFromRow(row)
		if err != nil {
			return nil, nil, err
		}
		devices = append(devices, device)
	}
	return devices, next, nil
}

func (s *PostgresIdentityStore) CountOnlineDevices(ctx context.Context, appID string, since time.Time, query DeviceQuery) (int64, error) {
	appUUID, err := toPgUUID(appID)
	if err != nil {
		return 0, err
	}
	filters, err := deviceFilterParams(query)
	if err != nil {
		return 0, err
	}
	count, err := s.engine.Queries.CountOnlineDevices(ctx, pgdb.CountOnlineDevicesParams{
		AppID:           appUUID,
		Since:           pgtype.Timestamptz{Time: since, Valid: true},
		Filters:         filters.metadata,
		EasClientID:     filters.clientIDs,
		CurrentUpdateID: filters.updateIDs,
		PublishGroup:    filters.publishGroups,
		Branch:          query.Branches,
		RuntimeVersion:  query.RuntimeVersions,
		Platform:        query.Platforms,
		DeviceModel:     query.DeviceModels,
		OsName:          query.OSNames,
		OsVersion:       query.OSVersions,
		CountryCode:     query.CountryCodes,
	})
	if err != nil {
		return 0, fmt.Errorf("counting online devices: %w", err)
	}
	return count, nil
}

// SearchMetadataValues returns top values of one key ranked by device count, optionally
// narrowed by a substring.
func (s *PostgresIdentityStore) SearchMetadataValues(ctx context.Context, appID string, key string, search string, limit int) ([]ValueCount, error) {
	appUUID, err := toPgUUID(appID)
	if err != nil {
		return nil, err
	}
	switch {
	case limit < 1:
		limit = 20
	case limit > 100:
		limit = 100
	}

	if search == "" {
		rows, err := s.engine.Queries.TopIdentityValues(ctx, pgdb.TopIdentityValuesParams{
			AppID:      appUUID,
			Key:        key,
			MaxResults: int32(limit),
		})
		if err != nil {
			return nil, fmt.Errorf("listing top identity values: %w", err)
		}
		values := make([]ValueCount, 0, len(rows))
		for _, row := range rows {
			values = append(values, ValueCount{Value: row.Value, DeviceCount: row.DeviceCount})
		}
		return values, nil
	}

	rows, err := s.engine.Queries.SearchIdentityValues(ctx, pgdb.SearchIdentityValuesParams{
		AppID:      appUUID,
		Key:        key,
		Search:     search,
		MaxResults: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("searching identity values: %w", err)
	}
	values := make([]ValueCount, 0, len(rows))
	for _, row := range rows {
		values = append(values, ValueCount{Value: row.Value, DeviceCount: row.DeviceCount})
	}
	return values, nil
}

// toPgUUIDs surfaces an unparseable id as an error, not a silently empty result set.
func toPgUUIDs(values []string) ([]pgtype.UUID, error) {
	if len(values) == 0 {
		return nil, nil
	}
	parsed := make([]pgtype.UUID, 0, len(values))
	for _, value := range values {
		id, err := toPgUUID(value)
		if err != nil {
			return nil, err
		}
		parsed = append(parsed, id)
	}
	return parsed, nil
}

// maxHardwareTextRunes bounds the hardware strings before they reach the registry, since
// Model, OSName and OSVersion are unauthenticated client input into unbounded TEXT columns.
const maxHardwareTextRunes = 128

// boundHardwareText caps a hardware string at maxHardwareTextRunes.
func boundHardwareText(value string) string {
	if runes := []rune(value); len(runes) > maxHardwareTextRunes {
		return string(runes[:maxHardwareTextRunes])
	}
	return value
}

// optionalText maps "not reported" onto SQL NULL, since the COALESCE-written registry
// columns would otherwise blank a known value.
func optionalText(value string) *string {
	if value == "" {
		return nil
	}
	bounded := boundHardwareText(value)
	return &bounded
}

// TouchDevice is the universal device registration: every check-in lands here, bumping
// last_seen (and geo/current update) for a known device or registering a new one. A nil
// current means this check-in does not know the update and leaves that column alone.
func (s *PostgresIdentityStore) TouchDevice(ctx context.Context, appID string, easClientID string, geo *Geo, current *CurrentUpdate, device DeviceInfo) error {
	appUUID, err := toPgUUID(appID)
	if err != nil {
		return err
	}
	clientUUID, err := toPgUUID(easClientID)
	if err != nil {
		return err
	}
	var currentUpdate pgtype.UUID // Valid:false = NULL = keep the known value
	// pgx binds every parameter, so observedAt still needs a valid value even when unused.
	observedAt := pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	if current != nil {
		if currentUpdate, err = toPgUUID(current.ID); err != nil {
			return err
		}
		if !current.ObservedAt.IsZero() {
			observedAt.Time = current.ObservedAt.UTC()
		}
	}

	touch := pgdb.TouchDeviceIdentityParams{
		AppID: appUUID, EasClientID: clientUUID,
		CurrentUpdateID: currentUpdate, ObservedAt: observedAt,
	}
	touch.DeviceModel = optionalText(device.Model)
	touch.OsName = optionalText(device.OSName)
	touch.OsVersion = optionalText(device.OSVersion)
	touch.AppVersion = optionalText(device.AppVersion)
	if geo != nil {
		touch.CountryCode = geo.CountryCode
		touch.City = geo.City
		touch.Lat = geo.Lat
		touch.Lng = geo.Lng
	}
	rows, err := s.engine.TouchDeviceIdentity(ctx, touch)
	if err != nil {
		return fmt.Errorf("touching device: %w", err)
	}
	if rows == 1 {
		return s.resolveUpdateFailures(ctx, appUUID, clientUUID, currentUpdate, observedAt)
	}

	register := pgdb.RegisterDeviceParams{
		AppID: appUUID, EasClientID: clientUUID,
		CurrentUpdateID: currentUpdate, ObservedAt: observedAt,
	}
	register.DeviceModel = optionalText(device.Model)
	register.OsName = optionalText(device.OSName)
	register.OsVersion = optionalText(device.OSVersion)
	register.AppVersion = optionalText(device.AppVersion)
	if geo != nil {
		register.CountryCode = geo.CountryCode
		register.City = geo.City
		register.Lat = geo.Lat
		register.Lng = geo.Lng
	}
	// Two racers both landing here is absorbed by the upsert's ON CONFLICT.
	registered, err := s.engine.RegisterDevice(ctx, register)
	if err != nil {
		return fmt.Errorf("registering device: %w", err)
	}
	// Zero rows means the conflict arm rejected this observation as older than the one on
	// file; it must not be allowed to close or reopen a failure either.
	if registered == 0 {
		return nil
	}
	return s.resolveUpdateFailures(ctx, appUUID, clientUUID, currentUpdate, observedAt)
}

// resolveUpdateFailures closes the manifest failures this poll disproves: the device is
// running an update it had reported as failed, or has moved past it. Best effort by design.
func (s *PostgresIdentityStore) resolveUpdateFailures(
	ctx context.Context,
	appUUID, clientUUID, currentUpdate pgtype.UUID,
	observedAt pgtype.Timestamptz,
) error {
	// No named update means the poll says nothing about what the device runs.
	if !currentUpdate.Valid {
		return nil
	}
	if _, err := s.engine.ResolveDeviceUpdateFailures(ctx, pgdb.ResolveDeviceUpdateFailuresParams{
		AppID:       appUUID,
		EasClientID: clientUUID,
		UpdateUuid:  currentUpdate,
		ObservedAt:  observedAt,
	}); err != nil {
		log.Printf("identity: resolving update failures failed: %v", err)
	}
	// Reversible, for a device that meets the same broken update again after an aborted rollout.
	if _, err := s.engine.ReopenDeviceUpdateFailures(ctx, pgdb.ReopenDeviceUpdateFailuresParams{
		AppID:       appUUID,
		EasClientID: clientUUID,
		UpdateUuid:  currentUpdate,
	}); err != nil {
		log.Printf("identity: reopening update failures failed: %v", err)
	}
	return nil
}

// RecordUpdateFailures stores failures, one row per (device, update). fatalError and
// failureType are capture-once: the first record of a (device, update) failure names them,
// and sticky re-sends never overwrite it.
func (s *PostgresIdentityStore) RecordUpdateFailures(ctx context.Context, appID string, easClientID string, updateIDs []string, fatalError string, failureType FailureType) error {
	appUUID, err := toPgUUID(appID)
	if err != nil {
		return err
	}
	clientUUID, err := toPgUUID(easClientID)
	if err != nil {
		return err
	}
	for _, updateID := range updateIDs {
		updateUUID, err := toPgUUID(updateID)
		if err != nil {
			continue // forged id in the header: skip, never fail the batch
		}
		if err := s.engine.UpsertDeviceUpdateFailure(ctx, pgdb.UpsertDeviceUpdateFailureParams{
			AppID:       appUUID,
			EasClientID: clientUUID,
			UpdateUuid:  updateUUID,
			FailureType: string(failureType),
			FatalError:  fatalError,
			OccurredAt:  pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
		}); err != nil {
			return fmt.Errorf("recording update failure: %w", err)
		}
	}
	return nil
}

// RecordRuntimeFailure persists one JS crash using the device event timestamp.
// That timestamp is bounded by the OTLP decoder before reaching this method.
func (s *PostgresIdentityStore) RecordRuntimeFailure(ctx context.Context, appID string, easClientID string, updateID string, fatalError string, occurredAt time.Time) error {
	appUUID, err := toPgUUID(appID)
	if err != nil {
		return err
	}
	clientUUID, err := toPgUUID(easClientID)
	if err != nil {
		return err
	}
	updateUUID, err := toPgUUID(updateID)
	if err != nil {
		return err
	}
	if err := s.engine.RecordDeviceRuntimeFailure(ctx, pgdb.RecordDeviceRuntimeFailureParams{
		AppID:       appUUID,
		EasClientID: clientUUID,
		UpdateUuid:  updateUUID,
		FatalError:  fatalError,
		OccurredAt:  pgtype.Timestamptz{Time: occurredAt.UTC(), Valid: true},
	}); err != nil {
		return fmt.Errorf("recording runtime failure: %w", err)
	}
	return nil
}

// ResolveRuntimeFailure marks a JS failure healthy only when this startup is
// strictly newer than the latest crash. Native update_issue rows are never
// resolved by JS activity.
func (s *PostgresIdentityStore) ResolveRuntimeFailure(ctx context.Context, appID string, easClientID string, updateID string, occurredAt time.Time) error {
	appUUID, err := toPgUUID(appID)
	if err != nil {
		return err
	}
	clientUUID, err := toPgUUID(easClientID)
	if err != nil {
		return err
	}
	updateUUID, err := toPgUUID(updateID)
	if err != nil {
		return err
	}
	if _, err := s.engine.ResolveDeviceRuntimeFailure(ctx, pgdb.ResolveDeviceRuntimeFailureParams{
		AppID:       appUUID,
		EasClientID: clientUUID,
		UpdateUuid:  updateUUID,
		OccurredAt:  pgtype.Timestamptz{Time: occurredAt.UTC(), Valid: true},
	}); err != nil {
		return fmt.Errorf("resolving runtime failure: %w", err)
	}
	return nil
}

// UpdateHealth is one update's instant-T adoption and health, from the registry alone.
// DevicesOnUpdate counts devices currently running the update; FaultyDevices is the size
// of the failure set, and UpdateIssues/RuntimeIssues break it down by cause but are NOT a
// partition (a device can report both, so their sum can exceed FaultyDevices).
// FailedStillOn is the overlap between the failure set and DevicesOnUpdate, keeping the two
// addable:
//
//	attempts = DevicesOnUpdate + (FaultyDevices - FailedStillOn)
//	healthy  = DevicesOnUpdate - FailedStillOn
type UpdateHealth struct {
	DevicesOnUpdate int64
	FaultyDevices   int64
	UpdateIssues    int64
	RuntimeIssues   int64
	FailedStillOn   int64
}

// UpdateHealthByIDs returns health per update uuid, skipping non-UUID ids.
func (s *PostgresIdentityStore) UpdateHealthByIDs(ctx context.Context, appID string, updateIDs []string) (map[string]UpdateHealth, error) {
	appUUID, err := toPgUUID(appID)
	if err != nil {
		return nil, err
	}
	ids := make([]pgtype.UUID, 0, len(updateIDs))
	for _, raw := range updateIDs {
		if parsed, err := toPgUUID(raw); err == nil {
			ids = append(ids, parsed)
		}
	}
	health := make(map[string]UpdateHealth, len(ids))
	if len(ids) == 0 {
		return health, nil
	}
	// Every id asked about gets an entry, even zero, so a caller can't confuse "no data" with
	// "not measured" (both queries below are GROUP BYs that answer nothing for an unused update).
	for _, raw := range updateIDs {
		if _, err := toPgUUID(raw); err == nil {
			health[raw] = UpdateHealth{}
		}
	}

	active, err := s.engine.DevicesOnUpdateByIDs(ctx, pgdb.DevicesOnUpdateByIDsParams{AppID: appUUID, UpdateIds: ids})
	if err != nil {
		return nil, fmt.Errorf("counting devices on updates: %w", err)
	}
	for _, row := range active {
		key := uuid.UUID(row.UpdateUuid.Bytes).String()
		entry := health[key]
		entry.DevicesOnUpdate = row.DeviceCount
		health[key] = entry
	}

	failures, err := s.engine.UpdateFailureBreakdownByIDs(ctx, pgdb.UpdateFailureBreakdownByIDsParams{AppID: appUUID, UpdateIds: ids})
	if err != nil {
		return nil, fmt.Errorf("counting update failures: %w", err)
	}
	for _, row := range failures {
		key := uuid.UUID(row.UpdateUuid.Bytes).String()
		entry := health[key]
		entry.FaultyDevices = row.FailedDevices
		entry.UpdateIssues = row.UpdateDevices
		entry.RuntimeIssues = row.RuntimeDevices
		entry.FailedStillOn = row.StillOnUpdate
		health[key] = entry
	}
	return health, nil
}
