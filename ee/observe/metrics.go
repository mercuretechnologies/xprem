// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import "github.com/prometheus/client_golang/prometheus"

// The ingestion route is a public, unauthenticated endpoint; these counters
// are how an operator sees rejection and abuse rates. A 429 spike is a source
// being throttled; a bad_request/too_large spike is a broken or hostile
// client; a telemetry drop count is the size of the analytics backlog waiting
// for the ClickHouse path. No appId label: the id is wire input on this route,
// so labeling by it would be an unbounded-cardinality hole.
var (
	observeBatchesVec = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "observe_batches_total",
			Help: "expo-observe ingestion batches, by result",
		},
		[]string{"result"},
	)

	observeRecordsDroppedVec = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "observe_records_dropped_total",
			Help: "Log records received but not stored, by reason",
		},
		[]string{"reason"},
	)

	// Deliberately NOT a reason on the counter above: that one counts RECORDS
	// that were not stored, and these two count something else entirely.
	// Folding them in made the series unreadable, since the enrichment skip is
	// both the loudest contributor and the one that loses nothing, which left
	// an operator unable to tell it apart from the one that loses data.
	observeBatchWorkSkippedVec = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "observe_batch_work_skipped_total",
			Help: "Per-batch database work a ceiling refused to do, by kind",
		},
		[]string{"kind"},
	)
)

const (
	reasonForgedClientID = "forged_client_id"
	reasonTelemetry      = "telemetry_no_sink"
	// Records the batch ceiling cut before anything read them: genuinely not
	// stored, which is what observe_records_dropped_total means.
	reasonOverCap = "over_batch_cap"
)

// The three kinds of work a per-batch ceiling can refuse, kept apart because
// they do not cost the same thing. An identity operation refused is data lost
// for good, since identity records never reach the telemetry rows. A health
// signal refused leaves the failure registry short of a transition while the
// raw events survive in ClickHouse. An enrichment refused stores the row whole
// minus its branch column.
const (
	workIdentityOp   = "identity_operation"
	workHealthSignal = "health_signal"
	workOriginLookup = "origin_lookup"
)

func init() {
	prometheus.MustRegister(observeBatchesVec, observeRecordsDroppedVec, observeBatchWorkSkippedVec)
}

func observeIdentityOpsDropped(n int) { observeBatchWorkSkipped(workIdentityOp, n) }

func observeHealthSignalsSkipped(n int) { observeBatchWorkSkipped(workHealthSignal, n) }

func observeOriginLookupsSkipped(n int) { observeBatchWorkSkipped(workOriginLookup, n) }

func observeBatchWorkSkipped(kind string, n int) {
	if n > 0 {
		observeBatchWorkSkippedVec.WithLabelValues(kind).Add(float64(n))
	}
}

func observeBatch(result string) {
	observeBatchesVec.WithLabelValues(result).Inc()
}

func observeRecordsDropped(reason string, n int) {
	if n > 0 {
		observeRecordsDroppedVec.WithLabelValues(reason).Add(float64(n))
	}
}
