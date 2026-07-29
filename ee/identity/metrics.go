// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package identity

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	identityApplyDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "identity_apply_duration_seconds",
			Help:    "Duration of identity operations against the store, by operation and outcome",
			Buckets: []float64{0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5},
		},
		[]string{"op", "outcome"},
	)

	identityApplyVec = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "identity_apply_total",
			Help: "Identity operations applied, by appId, operation and outcome",
		},
		[]string{"appId", "op", "outcome"},
	)

	identityDroppedKeysVec = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "identity_dropped_keys_total",
			Help: "Metadata keys rejected by the allowlist, by appId",
		},
		[]string{"appId"},
	)
)

func init() {
	prometheus.MustRegister(identityApplyDuration, identityApplyVec, identityDroppedKeysVec)
}

func observeApply(appID string, op Op, err error, droppedKeys int, elapsed time.Duration) {
	outcome := "ok"
	if err != nil {
		outcome = "error"
		// On error, appID may be arbitrary client input; aggregate under a sentinel to bound cardinality.
		appID = "unknown"
	}
	identityApplyDuration.WithLabelValues(string(op), outcome).Observe(elapsed.Seconds())
	identityApplyVec.WithLabelValues(appID, string(op), outcome).Inc()
	if droppedKeys > 0 && err == nil {
		identityDroppedKeysVec.WithLabelValues(appID).Add(float64(droppedKeys))
	}
}
