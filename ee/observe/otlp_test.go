// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Android wire shape: pretty-printed, intValue as OTLP-conformant string.
const androidLogsFixture = `{
  "resourceLogs": [
    {
      "resource": {
        "attributes": [
          { "key": "expo.eas_client.id", "value": { "stringValue": "8b9c1fe0-93b3-4b3a-8c1d-2f4a5e6b7c8d" } },
          { "key": "os.type", "value": { "stringValue": "linux" } },
          { "key": "service.name", "value": { "stringValue": "fr.skeat.myapp" } }
        ]
      },
      "scopeLogs": [
        {
          "scope": { "name": "expo-observe", "version": "56.0.16" },
          "logRecords": [
            {
              "timeUnixNano": 1767960489000000000,
              "severityNumber": 9,
              "severityText": "INFO",
              "body": { "stringValue": "" },
              "attributes": [
                { "key": "session.id", "value": { "stringValue": "aaaa-1111" } },
                { "key": "event.name", "value": { "stringValue": "$set" } },
                { "key": "userId", "value": { "stringValue": "user_42" } },
                { "key": "seats", "value": { "intValue": "12" } },
                { "key": "isInternal", "value": { "boolValue": true } }
              ]
            }
          ]
        }
      ],
      "schemaUrl": "https://opentelemetry.io/schemas/1.27.0"
    }
  ]
}`

// iOS wire shape: compact, intValue as a raw JSON number (deliberate OTLP
// deviation of the Swift client).
const iosLogsFixture = `{"resourceLogs":[{"resource":{"attributes":[{"key":"expo.eas_client.id","value":{"stringValue":"7A6B5C4D-3E2F-1A0B-9C8D-7E6F5A4B3C2D"}}]},"scopeLogs":[{"scope":{"name":"expo-observe","version":"56.0.16"},"logRecords":[{"timeUnixNano":1767960489000000000,"severityNumber":9,"severityText":"INFO","body":{"stringValue":"user logged in"},"attributes":[{"key":"event.name","value":{"stringValue":"$unset"}},{"key":"keys","value":{"arrayValue":{"values":[{"stringValue":"userId"},{"stringValue":"tenant"}]}}},{"key":"seats","value":{"intValue":42}}]}]}],"schemaUrl":"https://opentelemetry.io/schemas/1.27.0"}]}`

func TestDecodeLogsAndroidShape(t *testing.T) {
	batch, err := DecodeLogs([]byte(androidLogsFixture))
	require.NoError(t, err)
	require.Len(t, batch.Resources, 1)

	resource := batch.Resources[0]
	require.Equal(t, "8b9c1fe0-93b3-4b3a-8c1d-2f4a5e6b7c8d", resource.Attributes[EASClientIDKey])
	require.Len(t, resource.Records, 1)

	attrs := resource.Records[0].Attributes
	require.Equal(t, "$set", attrs[EventNameKey])
	require.Equal(t, "user_42", attrs["userId"])
	// Android string-form intValue decodes to a number.
	require.Equal(t, int64(12), attrs["seats"])
	require.Equal(t, true, attrs["isInternal"])
}

func TestDecodeLogsIOSShape(t *testing.T) {
	batch, err := DecodeLogs([]byte(iosLogsFixture))
	require.NoError(t, err)
	require.Len(t, batch.Resources, 1)

	attrs := batch.Resources[0].Records[0].Attributes
	require.Equal(t, "$unset", attrs[EventNameKey])
	// iOS raw-number intValue decodes to the same Go value as Android's.
	require.Equal(t, int64(42), attrs["seats"])
	// Nested arrayValue unwraps to []any of plain values.
	require.Equal(t, []any{"userId", "tenant"}, attrs["keys"])
}

func TestDecodeLogsTolerance(t *testing.T) {
	// Unknown fields and empty bodies are tolerated: rejecting destroys the
	// batch on the device.
	batch, err := DecodeLogs([]byte(`{"resourceLogs":[],"partialSuccess":{"whatever":1}}`))
	require.NoError(t, err)
	require.Empty(t, batch.Resources)

	batch, err = DecodeLogs([]byte(`{}`))
	require.NoError(t, err)
	require.Empty(t, batch.Resources)

	// Structural garbage is a hard error (the 400 arm).
	_, err = DecodeLogs([]byte(`not json at all`))
	require.Error(t, err)

	// Unrepresentable values decode to nil instead of failing the batch.
	batch, err = DecodeLogs([]byte(`{"resourceLogs":[{"resource":{"attributes":[{"key":"x","value":{"intValue":"not-a-number"}}]},"scopeLogs":[]}]}`))
	require.NoError(t, err)
	require.Nil(t, batch.Resources[0].Attributes["x"])

	// kvlistValue unwraps to a nested map.
	batch, err = DecodeLogs([]byte(`{"resourceLogs":[{"resource":{"attributes":[{"key":"nested","value":{"kvlistValue":{"values":[{"key":"a","value":{"doubleValue":1.5}}]}}}]},"scopeLogs":[]}]}`))
	require.NoError(t, err)
	require.Equal(t, map[string]any{"a": 1.5}, batch.Resources[0].Attributes["nested"])
}

// logsBodyWithRecords builds a logs body of one resource per client id, each
// carrying `records` numbered records, so a test can tell which end of the
// batch survived the cap.
func logsBodyWithRecords(clientIDs []string, records int) []byte {
	var resources []string
	for _, clientID := range clientIDs {
		var lines []string
		for i := 0; i < records; i++ {
			lines = append(lines, fmt.Sprintf(
				`{"timeUnixNano":1767960489000000000,"severityNumber":9,"body":{"stringValue":"%s-%d"},"attributes":[{"key":"event.name","value":{"stringValue":"exception"}}]}`,
				clientID, i))
		}
		resources = append(resources, fmt.Sprintf(
			`{"resource":{"attributes":[{"key":"expo.eas_client.id","value":{"stringValue":"%s"}}]},"scopeLogs":[{"logRecords":[%s]}]}`,
			clientID, strings.Join(lines, ",")))
	}
	return []byte(`{"resourceLogs":[` + strings.Join(resources, ",") + `]}`)
}

func decodedLogRecords(batch LogBatch) []LogRecord {
	var records []LogRecord
	for _, resource := range batch.Resources {
		records = append(records, resource.Records...)
	}
	return records
}

// The body cap is not a work cap: a record is what costs a database round trip,
// and a hostile body packs them small. Everything past maxRecordsPerBatch is
// cut at decode, before a single record can become an identity transaction, a
// registry write or an origin lookup.
func TestDecodeLogsCapsRecordsPerBatch(t *testing.T) {
	const surplus = 5
	first := "8b9c1fe0-93b3-4b3a-8c1d-2f4a5e6b7c8d"
	batch, err := DecodeLogs(logsBodyWithRecords([]string{first}, maxRecordsPerBatch+surplus))
	require.NoError(t, err)

	records := decodedLogRecords(batch)
	require.Len(t, records, maxRecordsPerBatch)
	require.Equal(t, surplus, batch.DroppedRecords)
	// The TAIL survives: a backlog arrives oldest first, and the newest records
	// are what the dashboards read and what the check-in derives the device's
	// current update from.
	require.Equal(t, first+"-"+strconv.Itoa(surplus), records[0].Body)
	require.Equal(t, first+"-"+strconv.Itoa(maxRecordsPerBatch+surplus-1), records[len(records)-1].Body)
}

// A resource entirely behind the cap must not be materialized at all: building
// its attribute maps only to drop them would hand back the cost the cap exists
// to avoid.
func TestDecodeLogsSkipsResourcesEntirelyOverTheCap(t *testing.T) {
	older := "8b9c1fe0-93b3-4b3a-8c1d-2f4a5e6b7c8d"
	newer := "7a6b5c4d-3e2f-1a0b-9c8d-7e6f5a4b3c2d"
	batch, err := DecodeLogs(logsBodyWithRecords([]string{older, newer}, maxRecordsPerBatch))
	require.NoError(t, err)

	require.Len(t, batch.Resources, 1, "the fully dropped resource must not survive decoding")
	require.Equal(t, newer, batch.Resources[0].Attributes[EASClientIDKey])
	require.Equal(t, maxRecordsPerBatch, batch.DroppedRecords)
	require.Len(t, batch.Resources[0].Records, maxRecordsPerBatch)
}

func TestDecodeMetricsCapsPointsPerBatch(t *testing.T) {
	const surplus = 3
	var points []string
	for i := 0; i < maxRecordsPerBatch+surplus; i++ {
		points = append(points, fmt.Sprintf(
			`{"name":"expo.startup.time","gauge":{"dataPoints":[{"timeUnixNano":1767960489000000000,"asDouble":%d}]}}`, i))
	}
	body := []byte(`{"resourceMetrics":[{"resource":{"attributes":[{"key":"expo.eas_client.id","value":{"stringValue":"8b9c1fe0-93b3-4b3a-8c1d-2f4a5e6b7c8d"}}]},"scopeMetrics":[{"metrics":[` +
		strings.Join(points, ",") + `]}]}]}`)

	batch, err := DecodeMetrics(body)
	require.NoError(t, err)
	require.Len(t, batch.Resources, 1)
	require.Len(t, batch.Resources[0].Points, maxRecordsPerBatch)
	require.Equal(t, surplus, batch.DroppedRecords)
	require.Equal(t, float64(surplus), batch.Resources[0].Points[0].Value)
}

// A batch under the cap must decode exactly as before, dropped count included:
// the guard is invisible to every real device.
func TestDecodeLogsLeavesNormalBatchesAlone(t *testing.T) {
	batch, err := DecodeLogs([]byte(androidLogsFixture))
	require.NoError(t, err)
	require.Zero(t, batch.DroppedRecords)
	require.Len(t, decodedLogRecords(batch), 1)
}
