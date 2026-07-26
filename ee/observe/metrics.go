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
)

const (
	reasonForgedClientID = "forged_client_id"
	reasonTelemetry      = "telemetry_no_sink"
	reasonOverCap        = "over_batch_cap"
)

func init() {
	prometheus.MustRegister(observeBatchesVec, observeRecordsDroppedVec)
}

func observeBatch(result string) {
	observeBatchesVec.WithLabelValues(result).Inc()
}

func observeRecordsDropped(reason string, n int) {
	if n > 0 {
		observeRecordsDroppedVec.WithLabelValues(reason).Add(float64(n))
	}
}
