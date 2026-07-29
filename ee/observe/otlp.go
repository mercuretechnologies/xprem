// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

// Package observe receives expo-observe telemetry: OTLP/JSON ingestion,
// decoding, flattening and dispatch.
package observe

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
)

// maxRecordsPerBatch caps how many log records (or metric points) one POST
// can turn into work.
const maxRecordsPerBatch = 10_000

// LogBatch is one decoded /v1/logs body.
type LogBatch struct {
	Resources []ResourceLogs
	// DroppedRecords is how many records maxRecordsPerBatch cut.
	DroppedRecords int
}

// ResourceLogs is one device session's worth of records: the resource
// attributes (device, app, update...) and the log records they scope.
type ResourceLogs struct {
	Attributes map[string]any
	Records    []LogRecord
}

// LogRecord is one decoded log record.
type LogRecord struct {
	// TimeUnixNano is nanoseconds since epoch; 0 when the client omitted it or
	// could not parse its stored timestamp.
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

// MetricPoint is one gauge data point.
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
// what identity operations are recognized by.
const EventNameKey = "event.name"

// leadingSkip is how many records to pass over so that at most
// maxRecordsPerBatch survive. Skips the head so the newest records are kept.
func leadingSkip(total int) int {
	if total <= maxRecordsPerBatch {
		return 0
	}
	return total - maxRecordsPerBatch
}

// DecodeLogs parses an OTLP/JSON logs body, keeping only the newest
// maxRecordsPerBatch records.
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
// DecodeLogs. Only gauges are read.
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

// otlpUint64 tolerates both wire forms of a 64-bit value: a raw JSON number or
// a string. Unparseable input reads as 0.
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

// toGo unwraps the single-key AnyValue union into plain Go values.
// intValue accepts both a string and a raw JSON number.
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
