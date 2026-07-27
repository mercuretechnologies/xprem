// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

// Package observe receives expo-observe telemetry: the OTLP/JSON ingestion
// routes, decoding, flattening and dispatch. It sees EVERY log record a
// device ships; identity operations ($set, $set_once, $unset) are routed to
// ee/identity, the remaining telemetry records are flattened into ClickHouse
// rows. The whole surface is free: no license gate anywhere on this path.
package observe

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
)

// Minimal, tolerant decoder for the OTLP/JSON bodies the expo-observe SDK
// POSTs. Deliberately hand-rolled over the otlp protobuf bindings: we only
// need resource attributes and per-record attributes, and the two platforms
// diverge from strict OTLP anyway (iOS sends intValue as a raw JSON number
// where the spec says string). Unknown fields are ignored by encoding/json,
// which is exactly the tolerance we want: rejecting a batch destroys it on
// the device. The decoded types are neutral on purpose: the ClickHouse
// flattener (flatten.go) extends them without another decoder.

// maxRecordsPerBatch caps how many log records (or metric points) one POST can
// turn into work. The body cap alone does not: a record is what costs a
// database round trip (an identity transaction, a registry write, an origin
// lookup, a health signal), and a hostile body packs them at roughly a hundred
// bytes each, so 16MB of them is six figures of Postgres operations from a
// single unauthenticated request.
//
// 10k is several times what any real device can hold: expo-observe keeps no
// row cap of its own, but Android evicts pending records after 7 days, and a
// heavy user generates a few hundred a day. Ingestion keeps the NEWEST ones,
// see DecodeLogs.
const maxRecordsPerBatch = 10_000

// LogBatch is one decoded /v1/logs body.
type LogBatch struct {
	Resources []ResourceLogs
	// DroppedRecords is how many records maxRecordsPerBatch cut. Reported back
	// to the client as an OTLP partialSuccess, never as an error status.
	DroppedRecords int
}

// ResourceLogs is one device session's worth of records: the resource
// attributes (device, app, update...) and the log records they scope.
type ResourceLogs struct {
	Attributes map[string]any
	Records    []LogRecord
}

// LogRecord is one decoded log record: the telemetry fields the flattener
// stores plus the attribute map identity and classification read.
type LogRecord struct {
	// TimeUnixNano is nanoseconds since epoch; 0 when the client could not
	// parse its stored timestamp (Android) or omitted it. The flattener maps
	// out-of-range values (0 included) to the ingestion time.
	TimeUnixNano   uint64
	SeverityNumber uint8
	SeverityText   string
	Body           string
	Attributes     map[string]any
}

// MetricBatch is one decoded /v1/metrics body.
type MetricBatch struct {
	Resources []ResourceMetrics
	// DroppedRecords is how many gauge points maxRecordsPerBatch cut.
	DroppedRecords int
}

// ResourceMetrics is one device session's worth of gauge points and the
// resource attributes that scope them.
type ResourceMetrics struct {
	Attributes map[string]any
	Points     []MetricPoint
}

// MetricPoint is one gauge data point. The SDK only ever emits gauges with a
// single point (unit "s"), but the decoder accepts any number of points per
// metric.
type MetricPoint struct {
	MetricName   string
	TimeUnixNano uint64
	Value        float64
	Attributes   map[string]any
}

// EASClientIDKey is the resource attribute carrying the persistent
// per-install UUID from expo-eas-client.
const EASClientIDKey = "expo.eas_client.id"

// EventNameKey is the record attribute carrying the log event name; it is
// what identity operations are recognized by. Deliberately also defined in
// ee/identity (recordEventNameKey): observe reads it here to classify
// identity-vs-telemetry, identity strips it there as envelope. The dependency
// direction (observe -> identity) and the flattener wanting it as a real
// column both forbid collapsing the two into one owner today.
const EventNameKey = "event.name"

// leadingSkip is how many records to pass over so that at most
// maxRecordsPerBatch survive. The TAIL is what we keep: a device coming back
// from a long offline stretch sends its backlog oldest first, the batch is
// marked sent on the device whatever we answer, and the newest records are
// both what the dashboards read and what the check-in reads the device's
// current update from. Keeping the head would store ancient sessions and
// permanently lose the recent ones.
func leadingSkip(total int) int {
	if total <= maxRecordsPerBatch {
		return 0
	}
	return total - maxRecordsPerBatch
}

// DecodeLogs parses an OTLP/JSON logs body. An error means the body is not
// JSON we can read at all; a readable body with zero records is normal, and a
// body over the record cap is decoded down to its newest maxRecordsPerBatch
// rather than rejected.
func DecodeLogs(body io.Reader) (LogBatch, error) {
	var decoded otlpLogsBody
	if err := json.NewDecoder(body).Decode(&decoded); err != nil {
		return LogBatch{}, fmt.Errorf("unreadable OTLP logs body: %w", err)
	}
	total := 0
	for _, resource := range decoded.ResourceLogs {
		total += countLogRecords(resource)
	}
	skip := leadingSkip(total)

	batch := LogBatch{Resources: make([]ResourceLogs, 0, len(decoded.ResourceLogs)), DroppedRecords: skip}
	skipped := 0
	for _, resource := range decoded.ResourceLogs {
		// Resources entirely behind the cap are stepped over whole: their
		// attribute maps are the allocation the cap exists to avoid, and
		// building them only to drop them would hand the cost back.
		if records := countLogRecords(resource); skipped < skip && skipped+records <= skip {
			skipped += records
			continue
		}
		entry := ResourceLogs{Attributes: kvToMap(resource.Resource.Attributes)}
		for _, scope := range resource.ScopeLogs {
			for _, record := range scope.LogRecords {
				if skipped < skip {
					skipped++
					continue
				}
				bodyText, _ := record.Body.toGo().(string)
				entry.Records = append(entry.Records, LogRecord{
					TimeUnixNano:   record.TimeUnixNano.uint64(),
					SeverityNumber: uint8(min(max(record.SeverityNumber, 0), 255)),
					SeverityText:   record.SeverityText,
					Body:           bodyText,
					Attributes:     kvToMap(record.Attributes),
				})
			}
		}
		batch.Resources = append(batch.Resources, entry)
	}
	return batch, nil
}

func countLogRecords(resource otlpResourceLogs) int {
	records := 0
	for _, scope := range resource.ScopeLogs {
		records += len(scope.LogRecords)
	}
	return records
}

// DecodeMetrics parses an OTLP/JSON metrics body, same tolerance contract as
// DecodeLogs. Only gauges are read: the SDK never emits sums or histograms,
// and an unknown metric shape must not fail the batch.
func DecodeMetrics(body io.Reader) (MetricBatch, error) {
	var decoded otlpMetricsBody
	if err := json.NewDecoder(body).Decode(&decoded); err != nil {
		return MetricBatch{}, fmt.Errorf("unreadable OTLP metrics body: %w", err)
	}
	total := 0
	for _, resource := range decoded.ResourceMetrics {
		total += countMetricPoints(resource)
	}
	skip := leadingSkip(total)

	batch := MetricBatch{Resources: make([]ResourceMetrics, 0, len(decoded.ResourceMetrics)), DroppedRecords: skip}
	skipped := 0
	for _, resource := range decoded.ResourceMetrics {
		if points := countMetricPoints(resource); skipped < skip && skipped+points <= skip {
			skipped += points
			continue
		}
		entry := ResourceMetrics{Attributes: kvToMap(resource.Resource.Attributes)}
		for _, scope := range resource.ScopeMetrics {
			for _, metric := range scope.Metrics {
				if metric.Gauge == nil {
					continue
				}
				for _, point := range metric.Gauge.DataPoints {
					if skipped < skip {
						skipped++
						continue
					}
					entry.Points = append(entry.Points, MetricPoint{
						MetricName:   metric.Name,
						TimeUnixNano: point.TimeUnixNano.uint64(),
						Value:        point.AsDouble,
						Attributes:   kvToMap(point.Attributes),
					})
				}
			}
		}
		batch.Resources = append(batch.Resources, entry)
	}
	return batch, nil
}

// Only gauge points are counted, matching what the decoder keeps: a sum or a
// histogram the SDK never emits must not eat into the cap.
func countMetricPoints(resource otlpResourceMetrics) int {
	points := 0
	for _, scope := range resource.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if metric.Gauge != nil {
				points += len(metric.Gauge.DataPoints)
			}
		}
	}
	return points
}

type otlpLogsBody struct {
	ResourceLogs []otlpResourceLogs `json:"resourceLogs"`
}

type otlpResourceLogs struct {
	Resource  otlpResource    `json:"resource"`
	ScopeLogs []otlpScopeLogs `json:"scopeLogs"`
}

type otlpResource struct {
	Attributes []otlpKV `json:"attributes"`
}

type otlpScopeLogs struct {
	LogRecords []otlpLogRecord `json:"logRecords"`
}

type otlpLogRecord struct {
	TimeUnixNano   otlpUint64   `json:"timeUnixNano"`
	SeverityNumber int          `json:"severityNumber"`
	SeverityText   string       `json:"severityText"`
	Body           otlpAnyValue `json:"body"`
	Attributes     []otlpKV     `json:"attributes"`
}

type otlpMetricsBody struct {
	ResourceMetrics []otlpResourceMetrics `json:"resourceMetrics"`
}

type otlpResourceMetrics struct {
	Resource     otlpResource       `json:"resource"`
	ScopeMetrics []otlpScopeMetrics `json:"scopeMetrics"`
}

type otlpScopeMetrics struct {
	Metrics []otlpMetric `json:"metrics"`
}

type otlpMetric struct {
	Name  string     `json:"name"`
	Gauge *otlpGauge `json:"gauge"`
}

type otlpGauge struct {
	DataPoints []otlpDataPoint `json:"dataPoints"`
}

type otlpDataPoint struct {
	TimeUnixNano otlpUint64 `json:"timeUnixNano"`
	AsDouble     float64    `json:"asDouble"`
	Attributes   []otlpKV   `json:"attributes"`
}

// otlpUint64 tolerates both wire forms of a 64-bit value, the same divergence
// intValue has: a raw JSON number (what both SDK platforms emit for
// timeUnixNano) and the OTLP-JSON-conformant string. Unparseable input reads
// as 0, which downstream maps to the ingestion time.
type otlpUint64 json.RawMessage

func (u *otlpUint64) UnmarshalJSON(data []byte) error {
	*u = otlpUint64(data)
	return nil
}

func (u otlpUint64) uint64() uint64 {
	if len(u) == 0 {
		return 0
	}
	var num json.Number
	if err := json.Unmarshal([]byte(u), &num); err == nil {
		if v, err := strconv.ParseUint(num.String(), 10, 64); err == nil {
			return v
		}
	}
	var str string
	if err := json.Unmarshal([]byte(u), &str); err == nil {
		if v, err := strconv.ParseUint(str, 10, 64); err == nil {
			return v
		}
	}
	return 0
}

type otlpKV struct {
	Key   string       `json:"key"`
	Value otlpAnyValue `json:"value"`
}

type otlpAnyValue struct {
	StringValue *string          `json:"stringValue"`
	IntValue    json.RawMessage  `json:"intValue"`
	DoubleValue *float64         `json:"doubleValue"`
	BoolValue   *bool            `json:"boolValue"`
	ArrayValue  *otlpArrayValue  `json:"arrayValue"`
	KvlistValue *otlpKvlistValue `json:"kvlistValue"`
}

type otlpArrayValue struct {
	Values []otlpAnyValue `json:"values"`
}

type otlpKvlistValue struct {
	Values []otlpKV `json:"values"`
}

// toGo unwraps the single-key AnyValue union into plain Go values. intValue
// accepts both wire forms: Android emits the OTLP-conformant string ("42"),
// iOS deliberately emits a raw JSON number (42). Unrepresentable values decode
// to nil; downstream consumers (identity's Sanitize, the flattener)
// drop them.
func (v otlpAnyValue) toGo() any {
	switch {
	case v.StringValue != nil:
		return *v.StringValue
	case len(v.IntValue) > 0:
		var num json.Number
		if err := json.Unmarshal(v.IntValue, &num); err == nil {
			if i, err := num.Int64(); err == nil {
				return i
			}
		}
		var str string
		if err := json.Unmarshal(v.IntValue, &str); err == nil {
			if i, err := strconv.ParseInt(str, 10, 64); err == nil {
				return i
			}
		}
		return nil
	case v.DoubleValue != nil:
		return *v.DoubleValue
	case v.BoolValue != nil:
		return *v.BoolValue
	case v.ArrayValue != nil:
		values := make([]any, 0, len(v.ArrayValue.Values))
		for _, item := range v.ArrayValue.Values {
			values = append(values, item.toGo())
		}
		return values
	case v.KvlistValue != nil:
		return kvToMap(v.KvlistValue.Values)
	}
	return nil
}

func kvToMap(kvs []otlpKV) map[string]any {
	m := make(map[string]any, len(kvs))
	for _, kv := range kvs {
		if kv.Key == "" {
			continue
		}
		m[kv.Key] = kv.Value.toGo()
	}
	return m
}
