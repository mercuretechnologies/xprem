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

// observeResult labels the batches_total counter; see metrics.go.
const (
	resultAccepted    = "accepted"
	resultBadRequest  = "bad_request"
	resultTooLarge    = "too_large"
	resultUnavailable = "unavailable"
)

// maxLogsBodyBytes caps a /v1/logs body. The SDK sends its whole pending
// backlog uncompressed in one POST with no client-side cap; a realistic batch
// is hundreds of KB, 16MB covers a device coming back from a long offline
// stretch. Beyond it we answer 413: the SDK treats it as a permanent failure
// and drops the batch, which is the point: a 5xx would make the device
// re-send the same oversized poison pill forever.
const maxLogsBodyBytes = 16 << 20

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
func recordCheckIns[R any](ctx context.Context, checkIns *CheckInRecorder, appID string, remoteIP string, rows []R, clientID func(R) string, updateID func(R) string, timestamp func(R) time.Time) {
	if checkIns == nil || len(rows) == 0 {
		return
	}
	newest := make(map[string]int, 1)
	for i, row := range rows {
		device := clientID(row)
		best, seen := newest[device]
		if !seen || timestamp(row).After(timestamp(rows[best])) {
			newest[device] = i
		}
	}
	for device, i := range newest {
		checkIns.Record(ctx, handlers.DeviceCheckIn{
			AppID:           appID,
			EASClientID:     device,
			RemoteIP:        remoteIP,
			CurrentUpdateID: updateID(rows[i]),
		})
	}
}

// resolveBranch fills MetricRow/LogRow.Branch; the resolver caches, so the
// per-row call is a map hit for every row after the first of an update.
func (h *IngestHandler) resolveBranch(ctx context.Context, appID, updateID string) string {
	if h.branches == nil {
		return ""
	}
	return h.branches.BranchName(ctx, appID, updateID)
}

// HandleLogs ingests POST /observe/{APP_ID}/{projectId}/v1/logs. Rate
// limiting and app-existence run in middleware ahead of this handler. The
// pipeline: identity ops ($set/$set_once/$unset) are applied first, then the
// telemetry records are flattened and inserted into ClickHouse, with each
// ingesting device registered in the universal registry (debounced).
func (h *IngestHandler) HandleLogs(w http.ResponseWriter, r *http.Request) {
	// A panic must not turn into gorilla's 500: 500 destroys the batch on the
	// device, 503 preserves it for a retry.
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("observe: recovered panic in logs ingestion: %v", rec)
			observeBatch(resultUnavailable)
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}()

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxLogsBodyBytes))
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			observeBatch(resultTooLarge)
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		// The body could not be read off the wire: transient, preserve.
		observeBatch(resultUnavailable)
		w.WriteHeader(http.StatusServiceUnavailable)
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

	if h.identityService != nil {
		remoteIP := ""
		if clientIP := helpers.ClientIP(r); clientIP.IsValid() {
			remoteIP = clientIP.String()
		}
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
	{
		remoteIP := ""
		if clientIP := helpers.ClientIP(r); clientIP.IsValid() {
			remoteIP = clientIP.String()
		}
		recordCheckIns(r.Context(), h.checkIns, appID, remoteIP, rows,
			func(row LogRow) string { return row.EASClientID },
			func(row LogRow) string { return row.UpdateID },
			func(row LogRow) time.Time { return row.Timestamp })
	}
	if h.telemetry == nil {
		observeRecordsDropped(reasonTelemetry, len(rows))
	} else {
		if len(rows) > 0 {
			for i := range rows {
				rows[i].Branch = h.resolveBranch(r.Context(), appID, rows[i].UpdateID)
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
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("observe: recovered panic in metrics ingestion: %v", rec)
			observeBatch(resultUnavailable)
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}()

	if h.telemetry == nil {
		// Drain within the same cap so keep-alive connections stay reusable.
		// Deliberately no decode and therefore no check-ins on this path:
		// every expo-updates device polls the manifest anyway (the seam
		// registers it there), so decoding metrics just to register would be
		// pure cost for a deployment that dropped the data.
		_, _ = io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, maxLogsBodyBytes))
		observeBatch(resultAccepted)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxLogsBodyBytes))
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			observeBatch(resultTooLarge)
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		observeBatch(resultUnavailable)
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	batch, err := DecodeMetrics(body)
	if err != nil {
		observeBatch(resultBadRequest)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	appID := mux.Vars(r)["APP_ID"]
	remoteIP := ""
	if clientIP := helpers.ClientIP(r); clientIP.IsValid() {
		remoteIP = clientIP.String()
	}
	rows := FlattenMetrics(appID, batch, time.Now().UTC())
	recordCheckIns(r.Context(), h.checkIns, appID, remoteIP, rows,
		func(row MetricRow) string { return row.EASClientID },
		func(row MetricRow) string { return row.UpdateID },
		func(row MetricRow) time.Time { return row.Timestamp })
	if len(rows) > 0 {
		for i := range rows {
			rows[i].Branch = h.resolveBranch(r.Context(), appID, rows[i].UpdateID)
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
