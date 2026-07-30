// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import "github.com/prometheus/client_golang/prometheus"

// Ingestion metrics, deliberately not labeled by appId since it is unbounded
// wire input on this route.
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
	reasonOverCap        = "over_batch_cap"
)

// The three kinds of work a per-batch ceiling can refuse.
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
