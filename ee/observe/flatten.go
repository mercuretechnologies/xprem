// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"encoding/json"
	"expo-open-ota/ee/identity"
	"hash/fnv"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// The flattener turns decoded OTLP batches into rows matching the ClickHouse
// schema (observe_metrics / observe_logs) column for column. It is pure:
// Branch stays empty here and is filled by the caller from the update->branch
// cache (a database concern), everything else comes from the wire. Resource
// attributes are denormalized onto every row so queries only ever touch plain
// columns.

// ZeroUpdateID is the update_id sentinel for "running the embedded bundle":
// the sorting key forbids Nullable, and a missing/invalid wire update id must
// still land somewhere queryable.
const ZeroUpdateID = "00000000-0000-0000-0000-000000000000"

// Envelope is the context every telemetry row carries whatever the signal is:
// which install, which release, which device, when. Shared by both row types
// rather than repeated in each, because it is what the check-in recorder, the
// geo enrichment and the origin resolver all read, and because a field added to
// one and forgotten in the other is the failure this prevents.
type Envelope struct {
	AppID       string
	EASClientID string
	UpdateID    string
	// UpdateGroupID is the publish an update came from, resolved from Postgres
	// at ingestion. Empty for updates published before the CLI minted groups,
	// and for rollback markers.
	UpdateGroupID  string
	Branch         string
	Channel        string
	RuntimeVersion string
	Platform       string
	SessionID      string
	OSName         string
	OSVersion      string
	DeviceModel    string
	// CountryCode and the coordinates are resolved from the request IP at
	// ingestion, not carried by the payload: the SDK never sends a location.
	// The coordinates are the GeoLite2 city centroid, nil when the block
	// resolved to a country but no finer, and nothing reads them yet.
	CountryCode    string
	Lat            *float64
	Lng            *float64
	AppVersion     string
	AppBuildNumber string
	EASBuildID     string
	Environment    string
	SDKVersion     string
	Timestamp      time.Time
	// Attributes carries the leftover point attributes as sorted JSON:
	// setGlobalAttributes merges arbitrary user keys into every row.
	Attributes  string
	ContentHash uint64
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
	pointUpdateIDKey  = "expo.update_id" // point level; see FlattenMetrics
	isFatalKey        = "expo.error.is_fatal"
)

// maxTimestampAge and maxTimestampSkew bound accepted wire timestamps. Out of
// range (a device clock set to 2093, an unparseable stored date arriving as 0)
// maps to the ingestion time: bogus values would scatter junk partitions, and
// an insert block spanning more than 100 distinct months is rejected whole by
// ClickHouse.
const (
	maxTimestampAge  = 396 * 24 * time.Hour // ~13 months, matching one partition of slack past a year
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

// maxResourceValueRunes bounds the resource attributes that become dimensions.
// Every one of them is client-supplied and unauthenticated, and they land in
// LowCardinality columns (whose dictionary grows per part) and, for the
// hardware trio, in TEXT columns of the device registry. A device that varies
// one of these per batch would otherwise inflate both without limit. The
// identity metadata path already bounds its values for exactly this reason; a
// real model name or app version is far below this.
const maxResourceValueRunes = 128

// The content fields, bounded to the SAME limits the client already enforces
// before it stores anything (expo-observe validation limits, section 6.4 of
// the protocol). Mirroring them rather than inventing numbers means an honest
// client is never truncated: it truncated itself first. What they stop is the
// forged batch, which is where the danger was, because none of these columns
// has a length of its own. ClickHouse String is unbounded, so a single record
// could carry megabytes up to the 16MB body limit, and the per-batch record
// ceiling counts records rather than bytes.
//
// The two LowCardinality columns matter most: that type builds a dictionary
// per part and assumes a small set of distinct values, so arbitrary names are
// exactly its pathological input.
const (
	// The client drops an event whose name exceeds this; the server truncates
	// instead, keeping a record that is merely mislabeled over losing it.
	maxEventNameRunes = 256
	// The client truncates a body here, ellipsis included.
	maxBodyRunes = 4096
	// A route is a display name in the client's vocabulary.
	maxRouteNameRunes = 128
	// Not in the client's table: OTLP severity text is a word ("ERROR"), and
	// it rides in a column shared with every other row.
	maxSeverityTextRunes = 128
	// Metric names come from a fixed vocabulary the SDK maps, so anything long
	// is forged by construction.
	maxMetricNameRunes = 256
	// customParams is the user's params map serialized to a JSON string. Same
	// budget as a body: it is free text as far as this server knows.
	maxCustomParamsRunes = 4096
)

// The leftover record attributes, which land in one JSON string column. The
// client keeps at most 128 per record, alphabetically, and counts the rest in
// droppedAttributesCount; the count is mirrored here so the two agree on WHICH
// survive. The byte ceiling is the server's own: 128 attributes say nothing
// about their size, and the column would otherwise take whatever fits in the
// request.
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

// newEnvelope reads the half of the envelope a whole resource block shares,
// once per block. The per-row half (session, timestamp, attributes, hash) is
// filled by the flatteners below, and the geo and origin half by the ingest
// handler, which is why those fields are absent here rather than zeroed.
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

// normalizeUpdateID lowercases and validates the wire update id; anything that
// is not a UUID becomes the embedded-bundle sentinel rather than poisoning the
// sorting key with garbage.
func normalizeUpdateID(raw string) string {
	parsed, err := uuid.Parse(raw)
	if err != nil {
		return ZeroUpdateID
	}
	return parsed.String()
}

// normalizeSessionID guards the UUID column: a forged non-UUID session id
// must degrade to the zero UUID, not fail the whole ClickHouse batch (a
// failed batch answers 503 and the device would retry the poison forever).
func normalizeSessionID(raw string) string {
	parsed, err := uuid.Parse(raw)
	if err != nil {
		return ZeroUpdateID
	}
	return parsed.String()
}

// normalizePlatform folds os.name into the two-value platform column the rest
// of the server uses ("ios" / "android"). os.name is only "present when
// available" on the wire; telemetry.sdk.language (swift/kotlin, always sent)
// is the fallback. Anything unrecognized keeps its lowercased name so a
// future platform is visible instead of silently bucketed.
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

// FlattenMetrics turns a decoded metrics batch into rows. Resources whose
// client id is not a UUID are dropped whole (unattributable, same rule as the
// identity path) and counted.
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
			// A point-level expo.update_id overrides the resource's: on
			// expo.updates.download_time it names the update that was just
			// DOWNLOADED, not the one running, and that is the update the
			// metric is about.
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
			// The raw nano (not the clamped time) goes into the hash so a
			// retried batch hashes identically whenever it re-arrives.
			row.ContentHash = contentHash(
				row.EASClientID, row.SessionID, row.UpdateID, row.MetricName,
				strconv.FormatUint(point.TimeUnixNano, 10),
				strconv.FormatFloat(point.Value, 'g', -1, 64),
				row.RouteName, row.CustomParams, row.Attributes,
			)
			rows = append(rows, row)
		}
	}
	return rows
}

// FlattenLogs turns a decoded logs batch into rows, skipping identity
// operations: those are applied by ee/identity, not stored as telemetry.
// Unattributable resources are dropped and counted by the identity pass that
// runs before this one, so they are skipped silently here.
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
			row.ContentHash = contentHash(
				row.EASClientID, row.SessionID, row.UpdateID, row.EventName,
				strconv.FormatUint(record.TimeUnixNano, 10),
				strconv.Itoa(int(record.SeverityNumber)), row.SeverityText,
				strconv.FormatBool(row.IsFatal),
				row.Body, row.Attributes,
			)
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
	// Alphabetical, which is the order the client retains in too: both ends
	// then keep the same attributes when there are more than the ceiling.
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
		// Approximate, on purpose: this is a ceiling, not an accounting, and
		// stopping one attribute early costs nothing next to marshalling the
		// whole map again for every candidate.
		cost := len(key) + 8
		if text, isText := value.(string); isText {
			cost += len(text)
		} else {
			cost += 64
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

// contentHash fingerprints one row for retry deduplication: published SDKs
// re-send a whole batch after ANY non-2xx, so duplicate rows are a certainty,
// not an edge case. iOS only sends whole-second timestamps, so (session,
// name, nano) alone could collide across genuinely distinct records; the
// value/body and serialized attributes disambiguate. FNV-1a over the parts in
// fixed order, deterministic across processes.
// contentHash identifies ONE record as the client wrote it, so a batch the SDK
// re-sends after a failed dispatch collapses at read time instead of counting
// twice. Every stored field the client authored goes in, and nothing the server
// resolved does: the branch, the publish group and the place are looked up here
// and can legitimately differ between a batch and its retry (a database blip
// resolves to empty once and to "main" the next time), which would make the two
// hash apart and defeat the whole point.
//
// The device id is in there for a reason that is easy to miss: normalizeSessionID
// maps a missing or unparseable session onto the ZERO uuid, so without it every
// session-less record of every device shares one identity, and two phones logging
// the same line at the same instant would merge. is_fatal and severity_text are in
// for the plainer reason that they are stored columns stripped out of the
// attributes, so leaving them out means two rows that differ on screen can share
// an identity. The update id is in for a reason that reads as redundant and is
// not: a metric point can carry its OWN expo.update_id, which overrides the
// session's because on expo.updates.download_time it names the update that was
// just DOWNLOADED rather than the one running, and that key is stripped out of
// the attributes as an envelope field. Two downloads in one session would
// otherwise share an identity. It is safe to include because it is read from
// the wire and never resolved here.
func contentHash(parts ...string) uint64 {
	h := fnv.New64a()
	for _, part := range parts {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return h.Sum64()
}
