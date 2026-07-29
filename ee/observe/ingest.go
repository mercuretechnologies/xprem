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

// identityPhaseTimeout bounds the operations TOGETHER. Per-operation bounds
// multiply: sixty-four of them at five seconds each is a request entitled to
// five minutes of a connection, which is not a bound at all. The phase gets the
// budget, each operation still gets its own so one stall cannot eat all of it,
// and whichever expires first answers 503, which keeps the batch on the device.
const identityPhaseTimeout = 30 * time.Second

// Per-batch ceilings on the PostgreSQL work one request can order. They are
// NOT a second maxRecordsPerBatch: 10k records cost ONE ClickHouse insert,
// while each of the three paths below costs a round trip per item, so the
// record cap alone still let a single POST order ten thousand sequential
// operations.
//
// The numbers come from what the SDK can produce, not from taste. A batch is
// one device's backlog and Android evicts pending records after 7 days, so the
// worst legitimate batch is one device over a week. $set is an explicit call
// the app makes, a handful per session, and CoalesceRequests folds the
// adjacent ones. Runtime signals are grouped per (device, update) with equal
// consecutive states collapsed, and a device runs one update at a time. So a
// real client lands one to two orders of magnitude below these, while a
// crafted one no longer gets ten thousand transactions out of one request.
//
// Over the ceiling the records are still stored: only their PostgreSQL side
// effect is skipped, and counted so the drop is visible rather than assumed.
const (
	maxIdentityOpsPerBatch    = 64
	maxRuntimeSignalsPerBatch = 64
	// How many (device, update) pairs one batch may write health for. Same
	// reasoning as the signal ceiling and the same order of magnitude: a batch
	// is one device's backlog, and a device runs one update at a time, so the
	// pairs it can name are the updates it went through.
	maxRuntimeGroupsPerBatch = 16
	maxUpdateLookupsPerBatch = 16
)

// telemetryInsertTimeout bounds the ClickHouse write, which was the one store
// call on this path running on the bare request context: a degraded ClickHouse
// held the goroutine and its connection for as long as the client was willing
// to wait, while the SDK behind it retried into the same wall. Longer than the
// identity bound because this writes a whole batch rather than one operation.
// Expiring answers 503, which is the arm that keeps the batch on the device.
const telemetryInsertTimeout = 15 * time.Second

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

// recordCheckIns registers the device of a batch (one: namesOneInstallation
// refused the request otherwise) into the registry, debounced by the recorder's
// cache. Telemetry knows the device's running update (the
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
		// Not After: on equal timestamps the LAST row wins, because rows keep
		// the order the device sent them and a backlog is sent oldest first.
		// Ties are not a curiosity here, they are the normal shape of a stale
		// batch: clampTimestamp folds an unparseable timestamp (the documented
		// Android fallback) and anything past maxTimestampAge onto the
		// ingestion instant, so a whole backlog can arrive sharing one. Taking
		// the first of those is exactly the regression this function exists to
		// avoid, and the registry's own staleness guard cannot catch it since
		// both rows then claim the same observation time.
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
			// The newest row of the batch is what this device was running when
			// the batch was written, which for a backlog flushed after an
			// update is older than the manifest poll racing it.
			ObservedAt: envelope.Timestamp,
		})
	}
}

// namesOneInstallation reports whether every resource of a batch names the same
// device. The wire allows any number of them, since the client id lives in the
// attributes of each resource and a body carries a list of them, but no client
// produces that: the app id comes from the URL and the client id is persisted
// per install, so one dispatch is one installation's own backlog.
//
// A body naming two is therefore forged, and the answer is to refuse the whole
// of it rather than to keep a part. Keeping the first would mean storing
// records under a device chosen from a body built to lie about which device
// sent them.
func namesOneInstallation[R any](resources []R, attributesOf func(R) map[string]any) bool {
	first := ""
	for _, resource := range resources {
		raw, _ := attributesOf(resource)[EASClientIDKey].(string)
		// Compared parsed, never raw. The iOS client spells its UUIDs in
		// upper case and Android in lower, and the refusal below is permanent:
		// a published client drops the batch for good on a 4xx. Two spellings
		// of one id must not be able to destroy a legitimate dispatch.
		parsed, err := uuid.Parse(raw)
		if err != nil {
			// No id, or one that is not an id: unattributable, dropped and
			// counted further down the pipeline. It claims no device, so it
			// cannot claim a second one.
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

// resolveOrigin fills MetricRow/LogRow.Branch and .UpdateGroupID; the resolver
// caches, so the per-row call is a map hit for every row after the first of an
// update.
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

// acknowledgeBatch closes an ingested batch. With nothing dropped it is the
// plain 204. When the record cap cut records it is a 2xx carrying an OTLP
// partialSuccess: the client marks the batch sent either way, which is what we
// want (a non-2xx would have it re-send the same oversized body forever), and
// a client that reads the body learns exactly how much was refused instead of
// believing everything landed.
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

// keepNewestIdentityWork bounds how many transactions one batch may order of
// the identity store, keeping the TAIL exactly as the record cap keeps the
// newest records. Coalescing leaves each key's changing values in order, so the
// last requests carry the state the row is meant to end in; keeping the head
// would leave the profile on a value the device abandoned long ago, and never
// mention it again.
//
// This is the one ceiling that truly loses data. An identity record is excluded
// from the telemetry rows (FlattenLogs skips it), so what is refused here is
// stored nowhere, which is why it is counted apart from the enrichment a
// ceiling merely postpones.
func keepNewestIdentityWork(requests []identity.Request) []identity.Request {
	if len(requests) <= maxIdentityOpsPerBatch {
		return requests
	}
	observeIdentityOpsDropped(len(requests) - maxIdentityOpsPerBatch)
	return requests[len(requests)-maxIdentityOpsPerBatch:]
}

// resolveRowOrigins denormalizes the branch and the publish onto every row,
// bounded by how many DISTINCT updates one batch may look up. The resolver
// caches, negatives included, so a repeated id is free; what is not free is a
// batch naming thousands of ids that have never been seen, each of which is a
// query before it becomes a cache entry. Past the ceiling the rows keep an
// empty branch, which is the same thing they carry when the lookup fails.
// Generic for the same reason recordCheckIns is: both row types embed the
// envelope this writes to, and one accessor beats two copies of the ceiling.
func resolveRowOrigins[R any](
	ctx context.Context,
	resolve func(ctx context.Context, appID, updateID string) (string, string),
	appID string,
	rows []R,
	envelopeOf func(*R) *Envelope,
) {
	// Chosen walking BACKWARDS, so a batch over the ceiling keeps the branch on
	// its newest rows rather than its oldest, which is the same end the record
	// cap picks. The embedded-bundle sentinel never costs a lookup (the
	// resolver returns before touching its cache), so it must not spend a slot
	// either.
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
// holding it. It used to read the whole thing into a []byte first, so the peak
// was the raw bytes AND the object tree json.Unmarshal built from them, both
// alive at once and the tree several times the size of its source. Decoding
// straight off the wire keeps only the decoder's own buffer, a few kilobytes.
//
// The ceiling is unchanged: MaxBytesReader still stops at maxBatchBodyBytes,
// and the decoder surfaces its error, which is why the caller still gets a 413
// on an oversized body.
func batchReader(w http.ResponseWriter, r *http.Request) io.Reader {
	return http.MaxBytesReader(w, r.Body, maxBatchBodyBytes)
}

// decodeFailure answers a body that could not be decoded, and the two reasons
// are answered differently.
//
// OVERSIZED gets a 2xx, which reads backwards until you look at what the
// published clients do: any non-2xx keeps the batch pending and re-sends it
// WHOLESALE on the next dispatch, with no backoff and no permanent drop (the
// 4xx classification only exists on the SDK's unreleased main). A 413 would
// therefore pin a device on the same oversized body until Android's seven-day
// eviction finally threw it away: it would never drain, never send anything
// newer, and would pay the upload every time it backgrounded. Acknowledging it
// costs this one batch and lets the device move on to the next, which is the
// same trade acknowledgeBatch makes for the record cap.
//
// MALFORMED gets a 400. Same reasoning as everywhere else here: a body that
// will never parse is not worth retrying, and a broken client does not repair
// itself.
func decodeFailure(w http.ResponseWriter, err error, rejectedField string) {
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		log.Printf("observe: refusing a batch over %d bytes; acknowledged so the device can move on", maxBatchBodyBytes)
		observeBatch(resultTooLarge)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// The count is unknown: nothing was decoded, which is the point. The
		// message carries what a reader needs instead.
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

// HandleLogs ingests POST /observe/{APP_ID}/{projectId}/v1/logs. App-existence
// runs in middleware ahead of this handler; request rate limiting does not
// exist yet and belongs at the edge (see middleware.go), which is why the work
// ONE request can trigger is capped here instead: maxBatchBodyBytes on the
// wire, maxRecordsPerBatch on what the body is allowed to become.
// The pipeline: identity ops ($set/$set_once/$unset) are applied first, then
// the telemetry records are flattened and inserted into ClickHouse, with each
// ingesting device registered in the universal registry (debounced).
func (h *IngestHandler) HandleLogs(w http.ResponseWriter, r *http.Request) {
	defer preserveBatchOnPanic(w, "logs")

	batch, err := DecodeLogs(batchReader(w, r))
	if err != nil {
		// Oversized, or structurally unreadable JSON: a broken client will not
		// repair itself, so both answer permanently rather than invite an
		// eternal retry loop.
		decodeFailure(w, err, rejectedLogRecordsField)
		return
	}
	// Same permanence, same reasoning: a body naming two installations is not
	// something a client will send differently next time.
	if !namesOneInstallation(batch.Resources, func(r ResourceLogs) map[string]any { return r.Attributes }) {
		observeBatch(resultBadRequest)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	appID := mux.Vars(r)["APP_ID"]
	remoteIP := clientIP(r)
	// Counted before anything reads the batch: what the cap cut never becomes
	// an identity operation, a check-in or a ClickHouse row.
	observeRecordsDropped(reasonOverCap, batch.DroppedRecords)

	if h.identityService != nil {
		// One installation per batch is already guaranteed by the rejection
		// above, so coalescing sees one device and the ceiling here only has
		// to bound the transactions that survive the fold.
		requests := keepNewestIdentityWork(
			identity.CoalesceRequests(identityRequestsFromBatch(batch, appID, remoteIP)))
		phaseContext, cancelPhase := context.WithTimeout(r.Context(), identityPhaseTimeout)
		defer cancelPhase()
		for _, req := range requests {
			applyContext, cancelApply := context.WithTimeout(phaseContext, identityApplyTimeout)
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
	// identical re-flattened rows carry the same content_key for query-time
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
// runtime crash into update health. expo-updates only ever reports
// launch-level failures (and on the new architecture no JS throw can fail a
// launch at all), so a JS crash while running an update is invisible to the
// manifest path; apps report it explicitly from their error boundary:
//
//	Observe.logEvent('xprem_js_crash', { attributes: { message } });
//	Observe.dispatchEvents();
//
// The resource attributes of such a record carry the running (= crashing)
// update id, projected into device_update_failures as a runtime_issue. The
// device keeps running the update (no rollback), unlike the manifest path.
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

// recordRuntimeHealth projects JS crash/start transitions into PostgreSQL.
// Signals are ordered by their bounded OTLP event timestamps rather than
// ingestion order: an offline batch can contain a newer startup before an
// older crash on the wire. Consecutive equal states collapse to their newest
// timestamp; raw ClickHouse logs still retain every original event.
func (h *IngestHandler) recordRuntimeHealth(ctx context.Context, appID string, rows []LogRow) error {
	if h.identityService == nil {
		return nil
	}

	grouped := groupRuntimeHealthSignals(rows)
	// Sorted, because the budget below is shared: ranging over the map would
	// hand it out in a different order every request, so the same body posted
	// twice would leave PostgreSQL in two different states, and the published
	// clients re-post a batch after any failure.
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

	// The GROUPS are bounded first. Budgeting per group without bounding how
	// many there are only moved the problem: a floor of one signal each turns
	// ten thousand invented update ids into ten thousand round trips, which is
	// the very thing the ceiling exists to stop. A batch is one device, and a
	// device runs one update at a time, so the groups it can legitimately name
	// are the updates it ran over the backlog.
	if len(keys) > maxRuntimeGroupsPerBatch {
		for _, key := range keys[maxRuntimeGroupsPerBatch:] {
			observeHealthSignalsSkipped(len(grouped[key]))
		}
		keys = keys[:maxRuntimeGroupsPerBatch]
	}

	// Then per group, not shared: what the registry has to get right is each
	// (device, update)'s FINAL state, and a budget spent in order would leave
	// the last groups with nothing at all.
	perGroup := max(1, maxRuntimeSignalsPerBatch/max(1, len(keys)))
	for _, key := range keys {
		signals := normalizeRuntimeHealthSignals(grouped[key])
		// The NEWEST of the group. A crash loop alternates crash and start, so
		// nothing collapses, and keeping the head would end on whichever state
		// came first: an update still crashing would be recorded as recovered
		// because a restart from an hour ago was the last one applied.
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
	// Bounded here too, and not only on the manifest path: this one comes out
	// of a batch body that may be 16MB, and it lands in the same columns.
	return boundFatalError(message)
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
// pre-ClickHouse acknowledge-and-drop, skipping even the decode.
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

	batch, err := DecodeMetrics(batchReader(w, r))
	if err != nil {
		// Oversized, or structurally unreadable JSON: both permanent, so the
		// client is told to stop rather than to retry a body that will never
		// parse or never fit.
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
	// One resolution per batch, not per row: a batch is one device's backlog,
	// so every row shares the request IP.
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
