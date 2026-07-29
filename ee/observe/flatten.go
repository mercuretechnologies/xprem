// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"expo-open-ota/ee/identity"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// ZeroUpdateID is the update_id sentinel for the embedded bundle, used when the wire update id is missing or invalid.
const ZeroUpdateID = "00000000-0000-0000-0000-000000000000"

// Envelope is the context every telemetry row carries: which install, which release, which device, when.
type Envelope struct {
	AppID       string
	EASClientID string
	UpdateID    string
	// UpdateGroupID is empty for updates published before the CLI minted groups, and for rollback markers.
	UpdateGroupID  string
	Branch         string
	Channel        string
	RuntimeVersion string
	Platform       string
	SessionID      string
	OSName         string
	OSVersion      string
	DeviceModel    string
	// CountryCode and the coordinates are resolved from the request IP at ingestion, not carried by the payload.
	CountryCode    string
	Lat            *float64
	Lng            *float64
	AppVersion     string
	AppBuildNumber string
	EASBuildID     string
	Environment    string
	SDKVersion     string
	Timestamp      time.Time
	// Attributes carries the leftover point attributes as sorted JSON.
	Attributes string
	// ContentKey folds a retried row onto the one already stored. See contentKey.
	ContentKey uuid.UUID
}

// MetricRow mirrors the observe_metrics table.
type MetricRow struct {
	Envelope
	MetricName   string
	Value        float64
	RouteName    string
	CustomParams string
}

// LogRow mirrors the observe_logs table.
type LogRow struct {
	Envelope
	EventName      string
	SeverityNumber uint8
	SeverityText   string
	IsFatal        bool
	Body           string
}

// Wire attribute keys (resource level unless noted).
const (
	updateIDKey       = "expo.app.updates.id"
	legacyUpdateIDKey = "expo.app.update_id"
	channelKey        = "expo.app.updates.channel"
	runtimeVersionKey = "expo.app.updates.runtime_version"
	osNameKey         = "os.name"
	osVersionKey      = "os.version"
	deviceModelKey    = "device.model.identifier"
	deviceModelAltKey = "device.model.name"
	appVersionKey     = "service.version"
	appBuildNumberKey = "expo.app.build_number"
	easBuildIDKey     = "expo.eas_build.id"
	environmentKey    = "expo.environment"
	sdkVersionKey     = "telemetry.sdk.version"
	sdkLanguageKey    = "telemetry.sdk.language"
	sessionIDKey      = "session.id" // record/point level
	routeNameKey      = "expo.route_name"
	customParamsKey   = "expo.custom_params"
	pointUpdateIDKey  = "expo.update_id" // point level
	isFatalKey        = "expo.error.is_fatal"
)

// maxTimestampAge and maxTimestampSkew bound accepted wire timestamps; out-of-range values map to the ingestion time.
const (
	maxTimestampAge  = 396 * 24 * time.Hour
	maxTimestampSkew = 24 * time.Hour
)

func clampTimestamp(nano uint64, now time.Time) time.Time {
	if nano == 0 || nano > uint64(1<<63-1) {
		return now
	}
	ts := time.Unix(0, int64(nano)).UTC()
	if ts.Before(now.Add(-maxTimestampAge)) || ts.After(now.Add(maxTimestampSkew)) {
		return now
	}
	return ts
}

// maxResourceValueRunes bounds client-supplied resource attributes that land in LowCardinality columns.
const maxResourceValueRunes = 128

// Content field limits, mirroring the client's own validation limits so an honest client is never truncated.
const (
	// The client drops an event whose name exceeds this; the server truncates instead.
	maxEventNameRunes    = 256
	maxBodyRunes         = 4096
	maxRouteNameRunes    = 128
	maxSeverityTextRunes = 128
	maxMetricNameRunes   = 256
	maxCustomParamsRunes = 4096
)

// Leftover record attributes land in one JSON string column, capped to match what the client itself keeps.
const (
	maxAttributesPerRecord = 128
	maxAttributeValueRunes = 1024
	maxAttributesBytes     = 16 * 1024
)

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

// newEnvelope reads the half of the envelope a whole resource block shares, once per block.
func newEnvelope(appID string, attrs map[string]any) Envelope {
	str := func(key string) string {
		s, _ := attrs[key].(string)
		return truncateRunes(s, maxResourceValueRunes)
	}
	osName := str(osNameKey)
	return Envelope{
		AppID:          appID,
		EASClientID:    str(EASClientIDKey),
		UpdateID:       normalizeUpdateID(firstNonEmpty(str(updateIDKey), str(legacyUpdateIDKey))),
		Channel:        str(channelKey),
		RuntimeVersion: str(runtimeVersionKey),
		Platform:       normalizePlatform(osName, str(sdkLanguageKey)),
		OSName:         osName,
		OSVersion:      str(osVersionKey),
		DeviceModel:    firstNonEmpty(str(deviceModelKey), str(deviceModelAltKey)),
		AppVersion:     str(appVersionKey),
		AppBuildNumber: str(appBuildNumberKey),
		EASBuildID:     str(easBuildIDKey),
		Environment:    str(environmentKey),
		SDKVersion:     str(sdkVersionKey),
	}
}

// normalizeUpdateID validates the wire update id; anything that is not a UUID becomes the embedded-bundle sentinel.
func normalizeUpdateID(raw string) string {
	parsed, err := uuid.Parse(raw)
	if err != nil {
		return ZeroUpdateID
	}
	return parsed.String()
}

// normalizeSessionID degrades a non-UUID session id to the zero UUID rather than failing the whole batch.
func normalizeSessionID(raw string) string {
	parsed, err := uuid.Parse(raw)
	if err != nil {
		return ZeroUpdateID
	}
	return parsed.String()
}

// normalizePlatform folds os.name into the two-value platform column ("ios" / "android"), falling back to sdkLanguage.
func normalizePlatform(osName, sdkLanguage string) string {
	switch osName {
	case "iOS", "iPadOS", "tvOS":
		return "ios"
	case "Android":
		return "android"
	}
	switch sdkLanguage {
	case "swift":
		return "ios"
	case "kotlin":
		return "android"
	}
	return lowerASCII(osName)
}

func lowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// FlattenMetrics turns a decoded metrics batch into rows. Resources whose client id is not a UUID are dropped and counted.
func FlattenMetrics(appID string, batch MetricBatch, now time.Time) []MetricRow {
	var rows []MetricRow
	for _, resource := range batch.Resources {
		resourceEnvelope := newEnvelope(appID, resource.Attributes)
		if _, err := uuid.Parse(resourceEnvelope.EASClientID); err != nil {
			observeRecordsDropped(reasonForgedClientID, len(resource.Points))
			continue
		}
		for _, point := range resource.Points {
			str := func(key string) string {
				s, _ := point.Attributes[key].(string)
				return s
			}
			envelope := resourceEnvelope
			// A point-level expo.update_id overrides the resource's: it names the update just downloaded, not the one running.
			if pointUpdate := str(pointUpdateIDKey); pointUpdate != "" {
				if parsed, err := uuid.Parse(pointUpdate); err == nil {
					envelope.UpdateID = parsed.String()
				}
			}
			envelope.SessionID = normalizeSessionID(str(sessionIDKey))
			envelope.Attributes = marshalAttributes(point.Attributes, metricEnvelopeKeys)
			envelope.Timestamp = clampTimestamp(point.TimeUnixNano, now)
			row := MetricRow{
				Envelope:     envelope,
				MetricName:   truncateRunes(point.MetricName, maxMetricNameRunes),
				Value:        point.Value,
				RouteName:    truncateRunes(str(routeNameKey), maxRouteNameRunes),
				CustomParams: truncateRunes(str(customParamsKey), maxCustomParamsRunes),
			}
			// The raw nano, not the clamped time, goes into the hash so a retried batch hashes identically.
			hashParts := []string{
				row.EASClientID, row.SessionID, row.UpdateID, row.MetricName,
				strconv.FormatUint(point.TimeUnixNano, 10),
				strconv.FormatFloat(point.Value, 'g', -1, 64),
				row.RouteName, row.CustomParams, row.Attributes,
			}
			row.ContentKey = contentKey(hashParts...)
			rows = append(rows, row)
		}
	}
	return rows
}

// FlattenLogs turns a decoded logs batch into rows, skipping identity operations applied by ee/identity.
func FlattenLogs(appID string, batch LogBatch, now time.Time) []LogRow {
	var rows []LogRow
	for _, resource := range batch.Resources {
		resourceEnvelope := newEnvelope(appID, resource.Attributes)
		if _, err := uuid.Parse(resourceEnvelope.EASClientID); err != nil {
			continue
		}
		for _, record := range resource.Records {
			eventName, _ := record.Attributes[EventNameKey].(string)
			if identity.IsIdentityOp(eventName) {
				continue
			}
			isFatal, _ := record.Attributes[isFatalKey].(bool)
			str := func(key string) string {
				s, _ := record.Attributes[key].(string)
				return s
			}
			envelope := resourceEnvelope
			envelope.SessionID = normalizeSessionID(str(sessionIDKey))
			envelope.Attributes = marshalAttributes(record.Attributes, logEnvelopeKeys)
			envelope.Timestamp = clampTimestamp(record.TimeUnixNano, now)
			row := LogRow{
				Envelope:       envelope,
				EventName:      truncateRunes(eventName, maxEventNameRunes),
				SeverityNumber: record.SeverityNumber,
				SeverityText:   truncateRunes(record.SeverityText, maxSeverityTextRunes),
				IsFatal:        isFatal,
				Body:           truncateRunes(record.Body, maxBodyRunes),
			}
			hashParts := []string{
				row.EASClientID, row.SessionID, row.UpdateID, row.EventName,
				strconv.FormatUint(record.TimeUnixNano, 10),
				strconv.Itoa(int(record.SeverityNumber)), row.SeverityText,
				strconv.FormatBool(row.IsFatal),
				row.Body, row.Attributes,
			}
			row.ContentKey = contentKey(hashParts...)
			rows = append(rows, row)
		}
	}
	return rows
}

// Envelope keys already stored as dedicated columns; everything else stays in
// the attributes JSON (exception.type/message/stacktrace, user attributes).
var (
	logEnvelopeKeys = map[string]bool{
		EventNameKey: true,
		sessionIDKey: true,
		isFatalKey:   true,
	}
	metricEnvelopeKeys = map[string]bool{
		sessionIDKey:     true,
		routeNameKey:     true,
		customParamsKey:  true,
		pointUpdateIDKey: true,
	}
)

// marshalAttributes serializes the non-envelope attributes as JSON.
// encoding/json sorts map keys, so the output (and therefore the content
// hash) is deterministic across retries of the same batch.
func marshalAttributes(attrs map[string]any, envelope map[string]bool) string {
	names := make([]string, 0, len(attrs))
	for key, value := range attrs {
		if envelope[key] || value == nil {
			continue
		}
		names = append(names, key)
	}
	if len(names) == 0 {
		return ""
	}
	// Alphabetical, matching the order the client retains, so both ends keep the same attributes past the ceiling.
	sort.Strings(names)
	if len(names) > maxAttributesPerRecord {
		names = names[:maxAttributesPerRecord]
	}

	kept := make(map[string]any, len(names))
	budget := maxAttributesBytes
	for _, key := range names {
		value := attrs[key]
		if text, isText := value.(string); isText {
			value = truncateRunes(text, maxAttributeValueRunes)
		}
		cost := len(key) + 8
		if text, isText := value.(string); isText {
			cost += len(text)
		} else {
			// Serialized to be measured, and kept serialized so the work is not done twice.
			encoded, err := json.Marshal(value)
			if err != nil {
				continue
			}
			cost += len(encoded)
			value = json.RawMessage(encoded)
		}
		if cost > budget {
			break
		}
		budget -= cost
		kept[key] = value
	}
	if len(kept) == 0 {
		return ""
	}
	out, err := json.Marshal(kept)
	if err != nil {
		return ""
	}
	return string(out)
}

// contentKey fingerprints a record's client-authored fields, so a batch the SDK re-sends collapses at read time
// instead of counting twice. Parts are length-prefixed rather than NUL-separated so two fields adjacent on the
// wire (routeName, customParams) can't be shifted into producing the same hash.
func contentKey(parts ...string) uuid.UUID {
	h := sha256.New()
	var length [8]byte
	for _, part := range parts {
		binary.LittleEndian.PutUint64(length[:], uint64(len(part)))
		_, _ = h.Write(length[:])
		_, _ = h.Write([]byte(part))
	}
	var key uuid.UUID
	copy(key[:], h.Sum(nil)[:16])
	return key
}
