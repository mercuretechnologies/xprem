// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"encoding/json"
	"errors"
	"expo-open-ota/ee/identity"
	"expo-open-ota/internal/handlers"
	"expo-open-ota/internal/helpers"
	"io"
	"log"
	"net/http"
	"sort"
	"time"

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

// maxBatchBodyBytes caps an ingestion body, logs and metrics alike. The SDK sends its whole pending
// backlog uncompressed in one POST with no client-side cap; a realistic batch
// is hundreds of KB, 16MB covers a device coming back from a long offline
// stretch. Beyond it we answer 413: the SDK treats it as a permanent failure
// and drops the batch, which is the point: a 5xx would make the device
// re-send the same oversized poison pill forever.
const maxBatchBodyBytes = 16 << 20

// identityApplyTimeout keeps a stalled store operation from tying up an
// ingestion request indefinitely. Each coalesced operation gets its own bound.
const identityApplyTimeout = 5 * time.Second

// IngestHandler owns the expo-observe ingestion routes. The response contract
// is dictated by the SDK's classification and every arm of it either destroys
// or preserves data on the device:
//
//	2xx              batch deleted on the device
//	429/502/503/504  batch kept, retried later
//	anything else    batch deleted (permanent failure)
//
// Two rules follow. Never answer 500 (a panic destroys a batch: the recover
// arm answers 503), and only answer 503 for genuinely transient conditions
// (a healthy retry, not a poison-pill loop).
type IngestHandler struct {
	// identityService applies $set/$set_once/$unset records. nil in stateless
	// mode (no control plane): identity ops are then acknowledged and dropped,
	// like every other record, so devices never accumulate a backlog.
	identityService *identity.Service
	// telemetry persists the flattened non-identity records in ClickHouse.
	// nil when no ClickHouse is configured: telemetry is then acknowledged,
	// counted and dropped.
	telemetry TelemetrySink
	// branches denormalizes update_id -> branch onto every row; nil leaves
	// the branch column empty.
	branches BranchResolver
	// checkIns records every ingesting device into the universal registry,
	// debounced; nil (stateless mode) leaves telemetry unregistered.
	checkIns *CheckInRecorder
}

func NewIngestHandler(identityService *identity.Service, telemetry TelemetrySink, branches BranchResolver, checkIns *CheckInRecorder) *IngestHandler {
	return &IngestHandler{identityService: identityService, telemetry: telemetry, branches: branches, checkIns: checkIns}
}

// recordCheckIns registers each distinct device of a batch (one, in practice: a
// batch is a single device's backlog) into the registry, debounced by the
// recorder's cache. Telemetry knows the device's running update (the
// expo.app.updates.id resource attribute, flattened onto every row), so the
// check-in carries it: devices that rarely poll the manifest still keep their
// adoption state fresh. Per device, the NEWEST row wins: a backlog flush
// leads with pre-update sessions carrying the OLD update id, and taking the
// first row would regress the recorded current right after a release.
// One accessor, not four: everything a check-in reads lives on the envelope
// both row types embed, so the caller only has to say where that envelope is.
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
		if !seen || envelope.Timestamp.After(envelopeOf(rows[best]).Timestamp) {
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
		})
	}
}

// clientIP renders the request's client address, "" when it cannot be trusted
// or parsed. Geo resolution and the registry both key on it.
func clientIP(r *http.Request) string {
	if ip := helpers.ClientIP(r); ip.IsValid() {
		return ip.String()
	}
	return ""
}

// resolveOrigin fills MetricRow/LogRow.Branch and .UpdateGroupID; the resolver
// caches, so the per-row call is a map hit for every row after the first of an
// update.
func (h *IngestHandler) resolveOrigin(ctx context.Context, appID, updateID string) (string, string) {
	if h.branches == nil {
		return "", ""
	}
	return h.branches.UpdateOrigin(ctx, appID, updateID)
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

// readBatch reads the body under the size ceiling and translates a failure into
// the status the SDK reads. ok=false means the response is already written and
// the caller has nothing left to do.
func readBatch(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBatchBodyBytes))
	if err == nil {
		return body, true
	}
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		observeBatch(resultTooLarge)
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return nil, false
	}
	// The body could not be read off the wire: transient, preserve.
	observeBatch(resultUnavailable)
	w.WriteHeader(http.StatusServiceUnavailable)
	return nil, false
}

// HandleLogs ingests POST /observe/{APP_ID}/{projectId}/v1/logs. Rate
// limiting and app-existence run in middleware ahead of this handler. The
// pipeline: identity ops ($set/$set_once/$unset) are applied first, then the
// telemetry records are flattened and inserted into ClickHouse, with each
// ingesting device registered in the universal registry (debounced).
func (h *IngestHandler) HandleLogs(w http.ResponseWriter, r *http.Request) {
	defer preserveBatchOnPanic(w, "logs")

	body, ok := readBatch(w, r)
	if !ok {
		return
	}

	batch, err := DecodeLogs(body)
	if err != nil {
		// Structurally unreadable JSON: a broken client will not repair
		// itself, 400 (permanent) rather than an eternal retry loop.
		observeBatch(resultBadRequest)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	appID := mux.Vars(r)["APP_ID"]
	remoteIP := clientIP(r)

	if h.identityService != nil {
		requests := identityRequestsFromBatch(batch, appID, remoteIP)
		for _, req := range identity.CoalesceRequests(requests) {
			applyContext, cancelApply := context.WithTimeout(r.Context(), identityApplyTimeout)
			_, err := h.identityService.Apply(applyContext, req)
			cancelApply()
			if err != nil {
				// Store errors are transient (pool exhausted, database down):
				// 503 keeps the batch on the device for a retry. Re-applying the
				// already-committed prefix on that retry is idempotent ($set
				// merges, $unset ignores absent keys), so no double effects.
				log.Printf("observe: identity apply failed: %v", err)
				observeBatch(resultUnavailable)
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
	}

	// The telemetry path runs after the identity split: rows are the
	// non-identity records; each ingesting device is also registered in the
	// universal registry (debounced). On insert failure, 503 preserves the
	// batch; the identity re-apply on that retry is idempotent, and the
	// identical re-flattened rows carry the same content_hash for query-time
	// dedup.
	rows := FlattenLogs(appID, batch, time.Now().UTC())
	// JS crash reports are projected into the failure registry before the
	// telemetry insert: like a failed identity apply, a failed projection
	// answers 503 so the device re-sends the batch (re-recording is
	// idempotent, the upsert dedups).
	if err := h.recordRuntimeHealth(r.Context(), appID, rows); err != nil {
		log.Printf("observe: recording runtime health failed: %v", err)
		observeBatch(resultUnavailable)
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	// Check-ins ride every log batch, sink or not: a no-ClickHouse deployment
	// still keeps its registry (and update health) fresh from telemetry.
	// The geo lookup only feeds the telemetry rows: check-ins resolve the place
	// themselves from the same IP. No sink, nothing to enrich.
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
			for i := range rows {
				rows[i].Branch, rows[i].UpdateGroupID = h.resolveOrigin(r.Context(), appID, rows[i].UpdateID)
			}
			if err := h.telemetry.InsertLogs(r.Context(), rows); err != nil {
				log.Printf("observe: clickhouse logs insert failed: %v", err)
				observeBatch(resultUnavailable)
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
	}

	observeBatch(resultAccepted)
	w.WriteHeader(http.StatusNoContent)
}

// JSCrashEventName is the documented log-event convention for reporting a JS
// runtime crash into update health. expo-updates only ever reports
// launch-level failures (and on the new architecture no JS throw can fail a
// launch at all), so a JS crash while running an update is invisible to the
// manifest path; apps report it explicitly from their error boundary:
//
//	Observe.logEvent('expo_open_ota_js_crash', { attributes: { message } });
//	Observe.dispatchEvents();
//
// The resource attributes of such a record carry the running (= crashing)
// update id, projected into device_update_failures as a runtime_issue. The
// device keeps running the update (no rollback), unlike the manifest path.
const (
	JSCrashEventName    = "expo_open_ota_js_crash"
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

// recordRuntimeHealth projects JS crash/start transitions into PostgreSQL.
// Signals are ordered by their bounded OTLP event timestamps rather than
// ingestion order: an offline batch can contain a newer startup before an
// older crash on the wire. Consecutive equal states collapse to their newest
// timestamp; raw ClickHouse logs still retain every original event.
func (h *IngestHandler) recordRuntimeHealth(ctx context.Context, appID string, rows []LogRow) error {
	if h.identityService == nil {
		return nil
	}

	for key, signals := range groupRuntimeHealthSignals(rows) {
		for _, signal := range normalizeRuntimeHealthSignals(signals) {
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

// jsCrashMessage pulls the conventional `message` attribute out of the row's
// leftover-attributes JSON for the fatal_error column; absent, non-string or
// unparseable yields "" and the capture-once upsert leaves the column open
// for a later capture.
func jsCrashMessage(attributes string) string {
	if attributes == "" {
		return ""
	}
	var attrs map[string]any
	if err := json.Unmarshal([]byte(attributes), &attrs); err != nil {
		return ""
	}
	message, _ := attrs["message"].(string)
	return message
}

// identityRequestsFromBatch turns a decoded logs batch into the identity
// operations it carries, dropping (and counting) records that cannot be
// attributed or are telemetry, not identity. Pure apart from the drop
// counters, so it is unit-tested directly without an HTTP round-trip.
func identityRequestsFromBatch(batch LogBatch, appID, remoteIP string) []identity.Request {
	var requests []identity.Request
	for _, resource := range batch.Resources {
		clientID, _ := resource.Attributes[EASClientIDKey].(string)
		// A missing or forged client id cannot be attributed to an install:
		// skip those records instead of failing the batch (a non-2xx would
		// also destroy or block every legitimate record around them).
		if _, err := uuid.Parse(clientID); err != nil {
			observeRecordsDropped(reasonForgedClientID, len(resource.Records))
			continue
		}
		for _, record := range resource.Records {
			eventName, _ := record.Attributes[EventNameKey].(string)
			if !identity.IsIdentityOp(eventName) {
				// Telemetry, not identity: the ClickHouse path picks these up
				// after the identity split (and counts them dropped when no
				// sink is configured).
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
// response contract and same pipeline as HandleLogs minus the identity split
// (identity ops only ever arrive on /v1/logs). Without a sink it stays the
// pre-ClickHouse acknowledge-and-drop, skipping even the decode. Rate
// limiting runs in middleware ahead of this handler.
func (h *IngestHandler) HandleMetrics(w http.ResponseWriter, r *http.Request) {
	defer preserveBatchOnPanic(w, "metrics")

	if h.telemetry == nil {
		// Drain within the same cap so keep-alive connections stay reusable.
		// Deliberately no decode and therefore no check-ins on this path:
		// every expo-updates device polls the manifest anyway (the seam
		// registers it there), so decoding metrics just to register would be
		// pure cost for a deployment that dropped the data.
		_, _ = io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, maxBatchBodyBytes))
		observeBatch(resultAccepted)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	body, ok := readBatch(w, r)
	if !ok {
		return
	}

	batch, err := DecodeMetrics(body)
	if err != nil {
		observeBatch(resultBadRequest)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	appID := mux.Vars(r)["APP_ID"]
	remoteIP := clientIP(r)
	rows := FlattenMetrics(appID, batch, time.Now().UTC())
	// One resolution per batch, not per row: a batch is one device's backlog,
	// so every row shares the request IP.
	place := h.identityService.PlaceOf(remoteIP)
	for i := range rows {
		rows[i].CountryCode, rows[i].Lat, rows[i].Lng = place.CountryCode, place.Lat, place.Lng
	}
	recordCheckIns(r.Context(), h.checkIns, appID, remoteIP, rows,
		func(row MetricRow) Envelope { return row.Envelope })
	if len(rows) > 0 {
		for i := range rows {
			rows[i].Branch, rows[i].UpdateGroupID = h.resolveOrigin(r.Context(), appID, rows[i].UpdateID)
		}
		if err := h.telemetry.InsertMetrics(r.Context(), rows); err != nil {
			log.Printf("observe: clickhouse metrics insert failed: %v", err)
			observeBatch(resultUnavailable)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
	}

	observeBatch(resultAccepted)
	w.WriteHeader(http.StatusNoContent)
}
