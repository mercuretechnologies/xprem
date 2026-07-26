// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

// Package identity maps expo-eas-client install UUIDs to operator-defined
// metadata (userId, tenant, ...), fed by the $set / $set_once / $unset log
// events on the observe ingestion route. The dashboard "Identity" section
// declares which metadata keys are accepted and with which type; everything
// else coming from the wire is dropped, so hostile payloads are bounded by
// construction.
//
// The package is EE-licensed AND license-gated: without a valid license the
// device registry keeps working (it is what update health is built on) but
// operator-defined attributes are not stored, and the ops report back which
// keys they dropped.
package identity

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ErrTooManySchemaKeys is returned when declaring a new allowlist key would
// exceed MaxSchemaKeys. The dashboard surfaces it as a 409.
var ErrTooManySchemaKeys = errors.New("identity schema key limit reached")

// ErrRequiresValidLicense is returned when declaring an attribute without a
// valid enterprise license. The device registry stays community; the
// operator-defined metadata on top of it is what the license buys.
var ErrRequiresValidLicense = errors.New("custom attributes require an active enterprise license")

type ValueType string

const (
	ValueTypeString  ValueType = "string"
	ValueTypeNumber  ValueType = "number"
	ValueTypeBoolean ValueType = "boolean"
)

const (
	// DefaultMaxLength bounds string values when the operator does not pick a
	// limit; MaxLengthCeiling bounds what the operator may pick. Identity
	// metadata is lookup keys, not payloads.
	DefaultMaxLength = 256
	MaxLengthCeiling = 1024
	// MaxSchemaKeys caps the allowlist size. Every declared key multiplies the
	// worst-case device row that the unauthenticated wire can fill, so the
	// operator-side bound keeps hostile payloads at ~tens of KB per row
	// instead of megabytes.
	MaxSchemaKeys = 100
)

// Key names stay path-friendly and unambiguous: they end up in API routes,
// autocomplete queries and JSONB lookups.
var keyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// KeySpec is one allowlisted metadata key as declared in the dashboard.
type KeySpec struct {
	Key       string    `json:"key"`
	Type      ValueType `json:"type"`
	MaxLength int       `json:"maxLength"`
}

// Schema is an app's full allowlist, keyed by metadata key.
type Schema map[string]KeySpec

func ValidateKeySpec(spec KeySpec) error {
	if !keyPattern.MatchString(spec.Key) {
		return fmt.Errorf("invalid metadata key %q: must match %s", spec.Key, keyPattern.String())
	}
	switch spec.Type {
	case ValueTypeString, ValueTypeNumber, ValueTypeBoolean:
	default:
		return fmt.Errorf("invalid value type %q for key %q: must be string, number or boolean", spec.Type, spec.Key)
	}
	if spec.MaxLength < 1 || spec.MaxLength > MaxLengthCeiling {
		return fmt.Errorf("invalid max length %d for key %q: must be in [1, %d]", spec.MaxLength, spec.Key, MaxLengthCeiling)
	}
	return nil
}

// Sanitize filters raw wire metadata down to the allowlist. A value survives
// only when its key is declared and its JSON type matches the declared type;
// violations drop the single entry, never the whole operation. Oversized
// strings are dropped rather than truncated (a truncated userId would corrupt
// the mapping silently). The dropped keys come back for counters and logs.
//
// Client-side validation (expo-app-metrics caps) is irrelevant here: anything
// can be forged with a plain HTTP request, this is the enforcement point.
func (s Schema) Sanitize(raw map[string]any) (map[string]any, []string) {
	sanitized := make(map[string]any, len(raw))
	var dropped []string
	for key, value := range raw {
		spec, declared := s[key]
		if !declared {
			dropped = append(dropped, key)
			continue
		}
		coerced, ok := coerceValue(spec, value)
		if !ok {
			dropped = append(dropped, key)
			continue
		}
		sanitized[key] = coerced
	}
	return sanitized, dropped
}

// coerceValue validates one raw value against its spec. Numbers normalize to
// float64 (what JSON decoding produces anyway); NaN and infinities are
// dropped because they do not survive json.Marshal into JSONB.
func coerceValue(spec KeySpec, value any) (any, bool) {
	switch spec.Type {
	case ValueTypeString:
		str, ok := value.(string)
		if !ok || utf8.RuneCountInString(str) > spec.MaxLength {
			return nil, false
		}
		return str, true
	case ValueTypeNumber:
		num, ok := toFloat(value)
		if !ok || math.IsNaN(num) || math.IsInf(num, 0) {
			return nil, false
		}
		return num, true
	case ValueTypeBoolean:
		b, ok := value.(bool)
		if !ok {
			return nil, false
		}
		return b, true
	}
	return nil, false
}

// ParseFilterValue turns the text a filter travels as (a URL parameter) into
// the value its declared type is stored as. Metadata lives in JSONB and is
// matched by containment, which is type-aware: a boolean stored as `true` is
// not found by `"true"`, so the schema is what decides how to read the text.
func ParseFilterValue(spec KeySpec, raw string) (any, bool) {
	switch spec.Type {
	case ValueTypeString:
		return coerceValue(spec, raw)
	case ValueTypeNumber:
		number, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, false
		}
		return coerceValue(spec, number)
	case ValueTypeBoolean:
		boolean, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, false
		}
		return coerceValue(spec, boolean)
	}
	return nil, false
}

// ErrInvalidFilterPair reports a pair that is not `key:value`, names a key the
// schema does not declare, or carries a value that key's type cannot hold.
var ErrInvalidFilterPair = errors.New("invalid attribute filter")

// ParseFilterPairs reads the `key:value` pairs a filter travels as. Pairs
// repeat, and repeating a key adds a value to it rather than replacing it, so
// `plan:pro`, `plan:enterprise`, `tenant:globex` reads as "plan is pro or
// enterprise, and tenant is globex".
//
// Keys never contain a colon (keyPattern forbids it), so the first one splits
// the pair and values keep theirs.
func ParseFilterPairs(schema Schema, pairs []string) (MetadataFilters, error) {
	filters := MetadataFilters{}
	index := map[string]int{}
	for _, pair := range pairs {
		key, raw, found := strings.Cut(strings.TrimSpace(pair), ":")
		if !found || key == "" || raw == "" {
			return nil, ErrInvalidFilterPair
		}
		spec, declared := schema[key]
		if !declared {
			return nil, ErrInvalidFilterPair
		}
		value, ok := ParseFilterValue(spec, raw)
		if !ok {
			return nil, ErrInvalidFilterPair
		}
		at, seen := index[key]
		if !seen {
			index[key] = len(filters)
			filters = append(filters, MetadataFilter{Key: key})
			at = index[key]
		}
		filters[at].Values = append(filters[at].Values, value)
	}
	if len(filters) == 0 {
		return nil, nil
	}
	return filters, nil
}

func toFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case uint32:
		return float64(v), true
	case uint64:
		return float64(v), true
	case json.Number:
		// The future ingest path may decode with UseNumber(); support it now
		// so numbers do not silently start dropping when it does.
		f, err := v.Float64()
		return f, err == nil
	}
	return 0, false
}

// RenderValue is the canonical text form of a metadata value, used as the
// identity_value_stats key. Integral floats render without a decimal part so
// 42 and 42.0 count as the same value.
func RenderValue(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case bool:
		return strconv.FormatBool(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		// Unreachable after Sanitize; fmt keeps a debuggable fallback.
		return fmt.Sprintf("%v", v)
	}
}

// Device is one install of an app and what we know about it. Timestamps stay
// time.Time here; formatting belongs to the handlers (same split as ee/audit).
type Device struct {
	AppID       string
	EASClientID string
	Metadata    map[string]any
	CountryCode *string
	City        *string
	Lat         *float64
	Lng         *float64
	// Hardware and OS as last reported by telemetry. nil means the device has
	// never sent any: the manifest path carries no such headers, so a fleet
	// without expo-observe reports nothing here.
	DeviceModel *string
	OSName      *string
	OSVersion   *string
	// The update the device is running, and the release dimensions derived
	// from it. Branch, RuntimeVersion and Platform are nil when the update is
	// unknown to this server: the embedded bundle reports its own id, which
	// matches no published update.
	CurrentUpdateID *string
	Branch          *string
	RuntimeVersion  *string
	Platform        *string
	FirstSeenAt     time.Time
	LastSeenAt      time.Time
}

// DeviceCursor is the keyset position for paginating the device inventory:
// the (last_seen_at, eas_client_id) of the last row returned. Newest-first.
type DeviceCursor struct {
	LastSeenAt  time.Time
	EASClientID string
}

// MetadataFilter narrows the device inventory to installs whose metadata
// contains the key with one of the given values. Values carry the declared
// type (string, float64 or bool), not the text they arrived as: metadata is
// matched by JSONB containment, which never matches `true` against `"true"`.
// ParseFilterValue is what turns one into the other.
//
// One entry is one key, and any of its values matches: "plan is pro or
// enterprise" is the comparison people actually ask for.
type MetadataFilter struct {
	Key    string
	Values []any
}

// MetadataFilters is a conjunction: a device matches when every key matches one
// of its values. "plan is pro or enterprise, and tenant is globex" narrows the
// two the way a reader expects, and each key can still be compared over
// several values.
type MetadataFilters []MetadataFilter

// A containment document spells one complete combination, so the count is the
// product of the value counts. Beyond this the question is better asked as
// fewer values than as thousands of index probes.
const MaxContainmentDocs = 128

// ErrTooManyCombinations reports a filter whose keys and values multiply out
// past MaxContainmentDocs.
var ErrTooManyCombinations = errors.New("too many attribute combinations")

// ContainmentDocs renders the conjunction as the set of JSONB documents that
// `metadata @> ANY(...)` accepts. Containment of `{"plan":"pro","tenant":"globex"}`
// already means "plan is pro AND tenant is globex", so the AND lives inside
// each document and the OR is the array: one document per combination.
func (f MetadataFilters) ContainmentDocs() ([][]byte, error) {
	if len(f) == 0 {
		return nil, nil
	}
	combinations := 1
	for _, entry := range f {
		if len(entry.Values) == 0 {
			return nil, nil
		}
		combinations *= len(entry.Values)
		if combinations > MaxContainmentDocs {
			return nil, ErrTooManyCombinations
		}
	}

	// Grown one key at a time: each existing combination is extended with every
	// value of the next key.
	combos := []map[string]any{{}}
	for _, entry := range f {
		next := make([]map[string]any, 0, len(combos)*len(entry.Values))
		for _, combo := range combos {
			for _, value := range entry.Values {
				grown := make(map[string]any, len(combo)+1)
				for key, existing := range combo {
					grown[key] = existing
				}
				grown[entry.Key] = value
				next = append(next, grown)
			}
		}
		combos = next
	}

	docs := make([][]byte, 0, len(combos))
	for _, combo := range combos {
		doc, err := json.Marshal(combo)
		if err != nil {
			return nil, err
		}
		docs = append(docs, doc)
	}
	return docs, nil
}

// DeviceQuery narrows the device inventory. Every field is optional and an
// empty one means "do not filter". The release dimensions (branch, runtime,
// platform) are matched against the update each device is currently running,
// so a device on the embedded bundle matches none of them.
//
// Channel, app version, build number, EAS build id and environment are
// deliberately absent: the registry never learns them. They live only in
// telemetry resource attributes, and inventing a column for them here would
// mean a value that is right for SDK users and silently wrong for everyone
// else.
type DeviceQuery struct {
	Metadata MetadataFilters
	// Every dimension is a set, like the telemetry filters: empty means "do
	// not filter", several values compare populations side by side.
	EASClientIDs     []string
	CurrentUpdateIDs []string
	// The publish a device's update belongs to. A device stores the update it
	// runs, never the publish it came from, so this is matched through that
	// update rather than on the device itself.
	UpdateGroupIDs  []string
	Branches        []string
	RuntimeVersions []string
	Platforms       []string
	DeviceModels    []string
	OSNames         []string
	OSVersions      []string
	CountryCodes    []string
}

const (
	// OnlineWindow is what "online" means here: a device that pinged the
	// server, by any route, within this window. Long enough to survive the
	// check-in debounce and a backgrounded app, short enough that the number
	// still reads as "right now".
	DefaultOnlineWindow = 20 * time.Minute
	MaxOnlineWindow     = 24 * time.Hour

	// DefaultDevicesPageSize and MaxDevicesPageSize bound the inventory page.
	DefaultDevicesPageSize = 50
	MaxDevicesPageSize     = 200
)

// Geo is an optional enrichment resolved from the request IP (GeoLite2,
// city-level accuracy: lat/lng is a city centroid, not a device position).
// Fields are per-field optional because partial resolutions are the norm
// (country without city is very common); a nil field never overwrites a
// previously known value, only a present one does.
type Geo struct {
	CountryCode *string
	City        *string
	Lat         *float64
	Lng         *float64
}

// Place is what telemetry ingestion keeps of a Geo: the country it filters on
// and the coordinates it stores for later. The city name is deliberately left
// behind, since nothing downstream groups or filters on it.
type Place struct {
	CountryCode string
	Lat         *float64
	Lng         *float64
}

// ValueCount is one autocomplete suggestion for a metadata key.
type ValueCount struct {
	Value       string `json:"value"`
	DeviceCount int64  `json:"deviceCount"`
}

// ApplyResult reports what an operation did: the device after merge, and which
// incoming keys were rejected by the allowlist.
type ApplyResult struct {
	Device      Device
	DroppedKeys []string
}
