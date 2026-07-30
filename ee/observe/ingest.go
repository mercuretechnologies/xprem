// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"time"
	"xprem/ee/identity"
	"xprem/internal/handlers"
	"xprem/internal/helpers"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// The values of the result label on observe_batches_total; see metrics.go.
const (
	resultAccepted    = "accepted"
	resultBadRequest  = "bad_request"
	resultTooLarge    = "too_large"
	resultUnavailable = "unavailable"
)

// maxBatchBodyBytes caps the size of one ingestion body; oversized bodies get
// a 413 so the SDK drops the batch instead of retrying it forever.
const maxBatchBodyBytes = 16 << 20

// identityApplyTimeout bounds a single coalesced identity store operation.
const identityApplyTimeout = 5 * time.Second

// identityPhaseTimeout bounds the whole run of coalesced identity operations
// in addition to each operation's own timeout.
const identityPhaseTimeout = 30 * time.Second

// Per-batch ceilings on the PostgreSQL work one request can order. Over the
// ceiling, records are still stored; only their PostgreSQL side effect is
// skipped, and counted as dropped.
const (
	maxIdentityOpsPerBatch    = 64
	maxRuntimeSignalsPerBatch = 64
	maxRuntimeGroupsPerBatch  = 16
	maxUpdateLookupsPerBatch  = 16
)

// telemetryInsertTimeout bounds the ClickHouse insert for one batch.
const telemetryInsertTimeout = 15 * time.Second

// IngestHandler owns the expo-observe ingestion routes. Responses follow the
// SDK's retry contract: 2xx deletes the batch on the device, 429/502/503/504
// keeps it for a later retry, anything else deletes it permanently.
type IngestHandler struct {
	// identityService applies $set/$set_once/$unset records. nil in stateless
	// mode: identity ops are then acknowledged and dropped like any other record.
	identityService *identity.Service
	// telemetry persists flattened non-identity records in ClickHouse. nil
	// when no ClickHouse is configured.
	telemetry TelemetrySink
	// branches denormalizes update_id -> branch onto every row; nil leaves
	// the branch column empty.
	branches BranchResolver
	// checkIns records every ingesting device into the universal registry,
	// debounced; nil leaves telemetry unregistered.
	checkIns *CheckInRecorder
}

func NewIngestHandler(identityService *identity.Service, telemetry TelemetrySink, branches BranchResolver, checkIns *CheckInRecorder) *IngestHandler {
	return &IngestHandler{identityService: identityService, telemetry: telemetry, branches: branches, checkIns: checkIns}
}

// recordCheckIns registers the batch's device into the check-in registry,
// using the newest row's envelope so a backlog flush does not regress the
// recorded current update.
func recordCheckIns[R any](
	ctx context.Context,
	checkIns *CheckInRecorder,
	appID string,
	remoteIP string,
	rows []R,
	envelopeOf func(R) Envelope,
) {
	if checkIns == nil || len(rows) == 0 {
		return
	}
	newest := make(map[string]int, 1)
	for i, row := range rows {
		envelope := envelopeOf(row)
		best, seen := newest[envelope.EASClientID]
		// Ties go to the last row: a stale batch can have many rows sharing
		// one clamped timestamp.
		if !seen || !envelope.Timestamp.Before(envelopeOf(rows[best]).Timestamp) {
			newest[envelope.EASClientID] = i
		}
	}
	for device, i := range newest {
		envelope := envelopeOf(rows[i])
		checkIns.Record(ctx, handlers.DeviceCheckIn{
			AppID:           appID,
			EASClientID:     device,
			RemoteIP:        remoteIP,
			CurrentUpdateID: envelope.UpdateID,
			DeviceModel:     envelope.DeviceModel,
			OSName:          envelope.OSName,
			OSVersion:       envelope.OSVersion,
			AppVersion:      envelope.AppVersion,
			ObservedAt:      envelope.Timestamp,
		})
	}
}

// namesOneInstallation reports whether every resource in a batch names the
// same device; a body naming two is forged, and the whole batch is refused
// rather than keeping a part of it.
func namesOneInstallation[R any](resources []R, attributesOf func(R) map[string]any) bool {
	first := ""
	for _, resource := range resources {
		raw, _ := attributesOf(resource)[EASClientIDKey].(string)
		// Compared parsed, never raw: iOS and Android spell the same id in
		// different case.
		parsed, err := uuid.Parse(raw)
		if err != nil {
			continue
		}
		clientID := parsed.String()
		if first == "" {
			first = clientID
			continue
		}
		if clientID != first {
			return false
		}
	}
	return true
}

// clientIP renders the request's client address, "" when it cannot be trusted
// or parsed. Geo resolution and the registry both key on it.
func clientIP(r *http.Request) string {
	if ip := helpers.ClientIP(r); ip.IsValid() {
		return ip.String()
	}
	return ""
}

// resolveOrigin fills MetricRow/LogRow.Branch and .UpdateGroupID.
func (h *IngestHandler) resolveOrigin(ctx context.Context, appID, updateID string) (string, string) {
	if h.branches == nil {
		return "", ""
	}
	return h.branches.UpdateOrigin(ctx, appID, updateID)
}

// The OTLP field naming the rejected count, per signal.
const (
	rejectedLogRecordsField = "rejectedLogRecords"
	rejectedDataPointsField = "rejectedDataPoints"
)

// acknowledgeBatch closes an ingested batch: a plain 204 if nothing was
// dropped, otherwise a 2xx carrying an OTLP partialSuccess so the client
// knows exactly how much was refused instead of assuming everything landed.
func acknowledgeBatch(w http.ResponseWriter, rejectedField string, rejected int) {
	observeBatch(resultAccepted)
	if rejected == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"partialSuccess": map[string]any{
			rejectedField: rejected,
			"errorMessage": fmt.Sprintf(
				"batch carried more than %d records; the newest %d were ingested",
				maxRecordsPerBatch, maxRecordsPerBatch),
		},
	})
}

// keepNewestIdentityWork bounds how many identity store transactions one
// batch may order, keeping the newest ones: coalescing leaves each key's
// final intended value at the tail. Unlike the other ceilings, records cut
// here are stored nowhere else, so they are dropped for good.
func keepNewestIdentityWork(requests []identity.Request) []identity.Request {
	if len(requests) <= maxIdentityOpsPerBatch {
		return requests
	}
	observeIdentityOpsDropped(len(requests) - maxIdentityOpsPerBatch)
	return requests[len(requests)-maxIdentityOpsPerBatch:]
}

// resolveRowOrigins denormalizes the branch and publish group onto every row,
// bounded by how many distinct updates one batch may look up. Rows past the
// ceiling keep an empty branch.
func resolveRowOrigins[R any](
	ctx context.Context,
	resolve func(ctx context.Context, appID, updateID string) (string, string),
	appID string,
	rows []R,
	envelopeOf func(*R) *Envelope,
) {
	// Walked backwards so a batch over the ceiling keeps its newest rows resolved.
	resolvable := make(map[string]struct{}, maxUpdateLookupsPerBatch)
	skipped := 0
	for i := len(rows) - 1; i >= 0; i-- {
		updateID := envelopeOf(&rows[i]).UpdateID
		if updateID == "" || updateID == ZeroUpdateID {
			continue
		}
		if _, known := resolvable[updateID]; known {
			continue
		}
		if len(resolvable) == maxUpdateLookupsPerBatch {
			skipped++
			continue
		}
		resolvable[updateID] = struct{}{}
	}
	observeOriginLookupsSkipped(skipped)

	for i := range rows {
		envelope := envelopeOf(&rows[i])
		if _, allowed := resolvable[envelope.UpdateID]; !allowed {
			continue
		}
		envelope.Branch, envelope.UpdateGroupID = resolve(ctx, appID, envelope.UpdateID)
	}
}

// preserveBatchOnPanic answers 503 rather than letting gorilla turn a panic
// into a 500: 500 destroys the batch on the device, 503 preserves it.
func preserveBatchOnPanic(w http.ResponseWriter, signal string) {
	if rec := recover(); rec != nil {
		log.Printf("observe: recovered panic in %s ingestion: %v", signal, rec)
		observeBatch(resultUnavailable)
		w.WriteHeader(http.StatusServiceUnavailable)
	}
}

// batchReader hands the decoder the body under the size ceiling, without
// buffering it whole.
func batchReader(w http.ResponseWriter, r *http.Request) io.Reader {
	return http.MaxBytesReader(w, r.Body, maxBatchBodyBytes)
}

// decodeFailure answers a body that could not be decoded: an oversized body
// gets a 2xx so the device is not stuck re-sending it forever, a malformed
// body gets a 400.
func decodeFailure(w http.ResponseWriter, err error, rejectedField string) {
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		log.Printf("observe: refusing a batch over %d bytes; acknowledged so the device can move on", maxBatchBodyBytes)
		observeBatch(resultTooLarge)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"partialSuccess": map[string]any{
				rejectedField: 0,
				"errorMessage": fmt.Sprintf(
					"batch body exceeded %d bytes and was refused whole; send smaller batches",
					maxBatchBodyBytes),
			},
		})
		return
	}
	observeBatch(resultBadRequest)
	w.WriteHeader(http.StatusBadRequest)
}

// HandleLogs ingests POST /observe/{APP_ID}/{projectId}/v1/logs: identity ops
// ($set/$set_once/$unset) are applied first, then telemetry rows are
// flattened and inserted into ClickHouse, with each device registered in the
// check-in registry.
func (h *IngestHandler) HandleLogs(w http.ResponseWriter, r *http.Request) {
	defer preserveBatchOnPanic(w, "logs")

	batch, err := DecodeLogs(batchReader(w, r))
	if err != nil {
		decodeFailure(w, err, rejectedLogRecordsField)
		return
	}
	if !namesOneInstallation(batch.Resources, func(r ResourceLogs) map[string]any { return r.Attributes }) {
		observeBatch(resultBadRequest)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	appID := mux.Vars(r)["APP_ID"]
	remoteIP := clientIP(r)
	observeRecordsDropped(reasonOverCap, batch.DroppedRecords)

	if h.identityService != nil {
		requests := keepNewestIdentityWork(
			identity.CoalesceRequests(identityRequestsFromBatch(batch, appID, remoteIP)))
		phaseContext, cancelPhase := context.WithTimeout(r.Context(), identityPhaseTimeout)
		defer cancelPhase()
		for _, req := range requests {
			applyContext, cancelApply := context.WithTimeout(phaseContext, identityApplyTimeout)
			_, err := h.identityService.Apply(applyContext, req)
			cancelApply()
			if err != nil {
				// 503 keeps the batch for a retry; re-applying the committed
				// prefix is idempotent, so no double effects.
				log.Printf("observe: identity apply failed: %v", err)
				observeBatch(resultUnavailable)
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
	}

	rows := FlattenLogs(appID, batch, time.Now().UTC())
	if err := h.recordRuntimeHealth(r.Context(), appID, rows); err != nil {
		log.Printf("observe: recording runtime health failed: %v", err)
		observeBatch(resultUnavailable)
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if h.telemetry != nil {
		place := h.identityService.PlaceOf(remoteIP)
		for i := range rows {
			rows[i].CountryCode, rows[i].Lat, rows[i].Lng = place.CountryCode, place.Lat, place.Lng
		}
	}
	recordCheckIns(r.Context(), h.checkIns, appID, remoteIP, rows,
		func(row LogRow) Envelope { return row.Envelope })
	if h.telemetry == nil {
		observeRecordsDropped(reasonTelemetry, len(rows))
	} else {
		if len(rows) > 0 {
			resolveRowOrigins(r.Context(), h.resolveOrigin, appID, rows,
				func(row *LogRow) *Envelope { return &row.Envelope })
			insertContext, cancelInsert := context.WithTimeout(r.Context(), telemetryInsertTimeout)
			err := h.telemetry.InsertLogs(insertContext, rows)
			cancelInsert()
			if err != nil {
				log.Printf("observe: clickhouse logs insert failed: %v", err)
				observeBatch(resultUnavailable)
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
	}

	acknowledgeBatch(w, rejectedLogRecordsField, batch.DroppedRecords)
}

// JSCrashEventName is the documented log-event convention for reporting a JS
// runtime crash into update health.
const (
	JSCrashEventName    = "xprem_js_crash"
	AppStartedEventName = "app_started"
)

type runtimeHealthState uint8

const (
	runtimeHealthy runtimeHealthState = iota
	runtimeFaulty
)

type runtimeHealthKey struct {
	device string
	update string
}

type runtimeHealthSignal struct {
	state      runtimeHealthState
	fatalError string
	occurredAt time.Time
}

// recordRuntimeHealth projects JS crash/start transitions into PostgreSQL,
// ordering signals by event timestamp rather than ingestion order.
func (h *IngestHandler) recordRuntimeHealth(ctx context.Context, appID string, rows []LogRow) error {
	if h.identityService == nil {
		return nil
	}

	grouped := groupRuntimeHealthSignals(rows)
	// Sorted for a deterministic budget: a retried batch must apply the same way.
	keys := make([]runtimeHealthKey, 0, len(grouped))
	for key := range grouped {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].device != keys[j].device {
			return keys[i].device < keys[j].device
		}
		return keys[i].update < keys[j].update
	})

	if len(keys) > maxRuntimeGroupsPerBatch {
		for _, key := range keys[maxRuntimeGroupsPerBatch:] {
			observeHealthSignalsSkipped(len(grouped[key]))
		}
		keys = keys[:maxRuntimeGroupsPerBatch]
	}

	perGroup := max(1, maxRuntimeSignalsPerBatch/max(1, len(keys)))
	for _, key := range keys {
		signals := normalizeRuntimeHealthSignals(grouped[key])
		// Keeps the newest signals so a still-crashing update isn't recorded as recovered.
		if len(signals) > perGroup {
			observeHealthSignalsSkipped(len(signals) - perGroup)
			signals = signals[len(signals)-perGroup:]
		}
		for _, signal := range signals {
			if err := h.applyRuntimeHealthSignal(ctx, appID, key, signal); err != nil {
				return err
			}
		}
	}
	return nil
}

func groupRuntimeHealthSignals(rows []LogRow) map[runtimeHealthKey][]runtimeHealthSignal {
	grouped := make(map[runtimeHealthKey][]runtimeHealthSignal)
	for _, row := range rows {
		if row.EventName != JSCrashEventName && row.EventName != AppStartedEventName {
			continue
		}
		if _, err := uuid.Parse(row.EASClientID); err != nil {
			observeRecordsDropped(reasonForgedClientID, 1)
			continue
		}
		if row.UpdateID == ZeroUpdateID {
			continue
		}
		key := runtimeHealthKey{device: row.EASClientID, update: row.UpdateID}
		state := runtimeHealthy
		if row.EventName == JSCrashEventName {
			state = runtimeFaulty
		}
		grouped[key] = append(grouped[key], runtimeHealthSignal{
			state:      state,
			fatalError: jsCrashMessage(row.Attributes),
			occurredAt: row.Timestamp.UTC(),
		})
	}
	return grouped
}

// normalizeRuntimeHealthSignals restores source-event order and collapses
// repeated states. On equal timestamps, a crash wins over a healthy startup.
func normalizeRuntimeHealthSignals(signals []runtimeHealthSignal) []runtimeHealthSignal {
	sort.SliceStable(signals, func(i, j int) bool {
		if signals[i].occurredAt.Equal(signals[j].occurredAt) {
			return signals[i].state == runtimeHealthy && signals[j].state == runtimeFaulty
		}
		return signals[i].occurredAt.Before(signals[j].occurredAt)
	})

	compacted := make([]runtimeHealthSignal, 0, len(signals))
	for _, signal := range signals {
		if len(compacted) == 0 || compacted[len(compacted)-1].state != signal.state {
			compacted = append(compacted, signal)
			continue
		}
		last := &compacted[len(compacted)-1]
		if last.fatalError == "" {
			last.fatalError = signal.fatalError
		}
		last.occurredAt = signal.occurredAt
	}
	return compacted
}

func (h *IngestHandler) applyRuntimeHealthSignal(ctx context.Context, appID string, key runtimeHealthKey, signal runtimeHealthSignal) error {
	if signal.state == runtimeFaulty {
		return h.identityService.RecordRuntimeFailure(
			ctx, appID, key.device, key.update, signal.fatalError, signal.occurredAt,
		)
	}
	return h.identityService.ResolveRuntimeFailure(
		ctx, appID, key.device, key.update, signal.occurredAt,
	)
}

// jsCrashMessage extracts the conventional `message` attribute for the
// fatal_error column; missing, non-string or unparseable yields "".
func jsCrashMessage(attributes string) string {
	if attributes == "" {
		return ""
	}
	var attrs map[string]any
	if err := json.Unmarshal([]byte(attributes), &attrs); err != nil {
		return ""
	}
	message, _ := attrs["message"].(string)
	return boundFatalError(message)
}

// identityRequestsFromBatch extracts the identity operations a decoded logs
// batch carries, dropping and counting records that cannot be attributed or
// are telemetry, not identity.
func identityRequestsFromBatch(batch LogBatch, appID, remoteIP string) []identity.Request {
	var requests []identity.Request
	for _, resource := range batch.Resources {
		clientID, _ := resource.Attributes[EASClientIDKey].(string)
		if _, err := uuid.Parse(clientID); err != nil {
			observeRecordsDropped(reasonForgedClientID, len(resource.Records))
			continue
		}
		for _, record := range resource.Records {
			eventName, _ := record.Attributes[EventNameKey].(string)
			if !identity.IsIdentityOp(eventName) {
				continue
			}
			if req, ok := identity.RequestFromRecord(appID, clientID, identity.Op(eventName), record.Attributes, remoteIP); ok {
				requests = append(requests, req)
			}
		}
	}
	return requests
}

// HandleMetrics ingests POST /observe/{APP_ID}/{projectId}/v1/metrics: same
// pipeline as HandleLogs without the identity split. Without a sink it stays
// the pre-ClickHouse acknowledge-and-drop, skipping even the decode.
func (h *IngestHandler) HandleMetrics(w http.ResponseWriter, r *http.Request) {
	defer preserveBatchOnPanic(w, "metrics")

	if h.telemetry == nil {
		_, _ = io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, maxBatchBodyBytes))
		observeBatch(resultAccepted)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	batch, err := DecodeMetrics(batchReader(w, r))
	if err != nil {
		decodeFailure(w, err, rejectedDataPointsField)
		return
	}
	if !namesOneInstallation(batch.Resources, func(r ResourceMetrics) map[string]any { return r.Attributes }) {
		observeBatch(resultBadRequest)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	appID := mux.Vars(r)["APP_ID"]
	remoteIP := clientIP(r)
	observeRecordsDropped(reasonOverCap, batch.DroppedRecords)
	rows := FlattenMetrics(appID, batch, time.Now().UTC())
	place := h.identityService.PlaceOf(remoteIP)
	for i := range rows {
		rows[i].CountryCode, rows[i].Lat, rows[i].Lng = place.CountryCode, place.Lat, place.Lng
	}
	recordCheckIns(r.Context(), h.checkIns, appID, remoteIP, rows,
		func(row MetricRow) Envelope { return row.Envelope })
	if len(rows) > 0 {
		resolveRowOrigins(r.Context(), h.resolveOrigin, appID, rows,
			func(row *MetricRow) *Envelope { return &row.Envelope })
		insertContext, cancelInsert := context.WithTimeout(r.Context(), telemetryInsertTimeout)
		err := h.telemetry.InsertMetrics(insertContext, rows)
		cancelInsert()
		if err != nil {
			log.Printf("observe: clickhouse metrics insert failed: %v", err)
			observeBatch(resultUnavailable)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
	}

	acknowledgeBatch(w, rejectedDataPointsField, batch.DroppedRecords)
}
