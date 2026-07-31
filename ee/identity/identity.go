// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

// Package identity maps expo-eas-client install UUIDs to operator-defined metadata, fed by the
// $set / $set_once / $unset log events. Without a valid license the device registry keeps
// working but operator-defined attributes are not stored.
package identity

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"xprem/ee/geoip"
)

// ErrTooManySchemaKeys is returned when declaring a new allowlist key would exceed MaxSchemaKeys.
var ErrTooManySchemaKeys = errors.New("identity schema key limit reached")

// ErrRequiresValidLicense is returned when declaring an attribute without a valid enterprise license.
var ErrRequiresValidLicense = errors.New("custom attributes require an active enterprise license")

type ValueType string

const (
	ValueTypeString  ValueType = "string"
	ValueTypeNumber  ValueType = "number"
	ValueTypeBoolean ValueType = "boolean"
)

const (
	// DefaultMaxLength bounds string values when the operator does not pick a limit.
	DefaultMaxLength = 256
	// MaxLengthCeiling bounds what the operator may pick.
	MaxLengthCeiling = 1024
	// MaxSchemaKeys caps the allowlist size, bounding the worst-case device row size.
	MaxSchemaKeys = 100
)

// keyPattern keeps key names path-friendly: they end up in API routes and JSONB lookups.
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

// Sanitize filters raw wire metadata down to the allowlist. A value survives only when its key
// is declared and its JSON type matches; violations drop the entry, never the whole operation.
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

// coerceValue validates one raw value against its spec, normalizing numbers to float64.
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

// ParseFilterValue turns the text a filter travels as (a URL parameter) into the value its
// declared type is stored as, since JSONB containment matching is type-aware.
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

// ErrInvalidFilterPair reports a pair that is not `key:value`, names an undeclared key, or
// carries a value that key's type cannot hold.
var ErrInvalidFilterPair = errors.New("invalid attribute filter")

// ParseFilterPairs reads the `key:value` pairs a filter travels as. Repeating a key adds a
// value to it rather than replacing it.
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
		// Supported in case a future ingest path decodes with UseNumber().
		f, err := v.Float64()
		return f, err == nil
	}
	return 0, false
}

// RenderValue is the canonical text form of a metadata value, used as the identity_value_stats key.
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

// Device is one install of an app and what we know about it.
type Device struct {
	AppID       string
	EASClientID string
	Metadata    map[string]any
	CountryCode *string
	City        *string
	Lat         *float64
	Lng         *float64
	// Hardware and OS as last reported by telemetry; nil means it never reported any.
	DeviceModel *string
	OSName      *string
	OSVersion   *string
	// Branch, RuntimeVersion and Platform are nil when CurrentUpdateID is unknown to this server.
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

// EncodeDeviceCursor makes the cursor opaque on the wire: base64 of
// "RFC3339Nano|uuid". Every surface paginating the inventory shares this
// codec so a cursor handed out by one is readable by the other.
func EncodeDeviceCursor(cursor *DeviceCursor) string {
	if cursor == nil {
		return ""
	}
	raw := cursor.LastSeenAt.UTC().Format(time.RFC3339Nano) + "|" + cursor.EASClientID
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeDeviceCursor rejects a tampered cursor here rather than letting the
// store fail on the parse.
func DecodeDeviceCursor(encoded string) (*DeviceCursor, error) {
	if encoded == "" {
		return nil, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	parts := strings.SplitN(string(decoded), "|", 2)
	if len(parts) != 2 {
		return nil, errors.New("malformed cursor")
	}
	lastSeenAt, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return nil, err
	}
	if _, err := uuid.Parse(parts[1]); err != nil {
		return nil, err
	}
	return &DeviceCursor{LastSeenAt: lastSeenAt, EASClientID: parts[1]}, nil
}

// MetadataFilter narrows the device inventory to installs whose metadata contains the key
// with one of the given values. A device matches if any of the values matches.
type MetadataFilter struct {
	Key    string
	Values []any
}

// MetadataFilters is a conjunction: a device matches when every key matches one of its values.
type MetadataFilters []MetadataFilter

// MaxContainmentDocs bounds the product of the per-key value counts.
const MaxContainmentDocs = 128

// ErrTooManyCombinations reports a filter whose keys and values multiply out
// past MaxContainmentDocs.
var ErrTooManyCombinations = errors.New("too many attribute combinations")

// ContainmentDocs renders the conjunction as the set of JSONB documents that
// `metadata @> ANY(...)` accepts: one document per combination.
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

	// Grown one key at a time: each combination is extended with every value of the next key.
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

// DeviceQuery narrows the device inventory. Every field is optional; an empty one means
// "do not filter". Channel, app version, build number, EAS build id and environment are
// deliberately absent since the registry never learns them.
type DeviceQuery struct {
	Metadata MetadataFilters
	// Empty means "do not filter"; several values compare populations side by side.
	EASClientIDs     []string
	CurrentUpdateIDs []string
	// Matched through the device's current update, since a device stores no publish id.
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
	// DefaultOnlineWindow is how recently a device must have pinged the server to count as online.
	DefaultOnlineWindow = 20 * time.Minute
	MaxOnlineWindow     = 24 * time.Hour

	// DefaultDevicesPageSize and MaxDevicesPageSize bound the inventory page.
	DefaultDevicesPageSize = 50
	MaxDevicesPageSize     = 200
)

// Geo is the identity vocabulary for a resolved location; the resolvers and
// their middleware live in ee/geoip.
type Geo = geoip.Location

// Place is what telemetry ingestion keeps of a Geo: the country and coordinates, not the city.
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
