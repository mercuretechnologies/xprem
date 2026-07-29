// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Android wire shape: intValue as an OTLP-conformant string.
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

// iOS wire shape: intValue as a raw JSON number.
const iosLogsFixture = `{"resourceLogs":[{"resource":{"attributes":[{"key":"expo.eas_client.id","value":{"stringValue":"7A6B5C4D-3E2F-1A0B-9C8D-7E6F5A4B3C2D"}}]},"scopeLogs":[{"scope":{"name":"expo-observe","version":"56.0.16"},"logRecords":[{"timeUnixNano":1767960489000000000,"severityNumber":9,"severityText":"INFO","body":{"stringValue":"user logged in"},"attributes":[{"key":"event.name","value":{"stringValue":"$unset"}},{"key":"keys","value":{"arrayValue":{"values":[{"stringValue":"userId"},{"stringValue":"tenant"}]}}},{"key":"seats","value":{"intValue":42}}]}]}],"schemaUrl":"https://opentelemetry.io/schemas/1.27.0"}]}`

func TestDecodeLogsAndroidShape(t *testing.T) {
	batch, err := DecodeLogs(bytes.NewReader([]byte(androidLogsFixture)))
	require.NoError(t, err)
	require.Len(t, batch.Resources, 1)

	resource := batch.Resources[0]
	require.Equal(t, "8b9c1fe0-93b3-4b3a-8c1d-2f4a5e6b7c8d", resource.Attributes[EASClientIDKey])
	require.Len(t, resource.Records, 1)

	attrs := resource.Records[0].Attributes
	require.Equal(t, "$set", attrs[EventNameKey])
	require.Equal(t, "user_42", attrs["userId"])
	require.Equal(t, int64(12), attrs["seats"])
	require.Equal(t, true, attrs["isInternal"])
}

func TestDecodeLogsIOSShape(t *testing.T) {
	batch, err := DecodeLogs(bytes.NewReader([]byte(iosLogsFixture)))
	require.NoError(t, err)
	require.Len(t, batch.Resources, 1)

	attrs := batch.Resources[0].Records[0].Attributes
	require.Equal(t, "$unset", attrs[EventNameKey])
	require.Equal(t, int64(42), attrs["seats"])
	require.Equal(t, []any{"userId", "tenant"}, attrs["keys"])
}

func TestDecodeLogsTolerance(t *testing.T) {
	batch, err := DecodeLogs(bytes.NewReader([]byte(`{"resourceLogs":[],"partialSuccess":{"whatever":1}}`)))
	require.NoError(t, err)
	require.Empty(t, batch.Resources)

	batch, err = DecodeLogs(bytes.NewReader([]byte(`{}`)))
	require.NoError(t, err)
	require.Empty(t, batch.Resources)

	_, err = DecodeLogs(bytes.NewReader([]byte(`not json at all`)))
	require.Error(t, err)

	batch, err = DecodeLogs(bytes.NewReader([]byte(`{"resourceLogs":[{"resource":{"attributes":[{"key":"x","value":{"intValue":"not-a-number"}}]},"scopeLogs":[]}]}`)))
	require.NoError(t, err)
	require.Nil(t, batch.Resources[0].Attributes["x"])

	batch, err = DecodeLogs(bytes.NewReader([]byte(`{"resourceLogs":[{"resource":{"attributes":[{"key":"nested","value":{"kvlistValue":{"values":[{"key":"a","value":{"doubleValue":1.5}}]}}}]},"scopeLogs":[]}]}`)))
	require.NoError(t, err)
	require.Equal(t, map[string]any{"a": 1.5}, batch.Resources[0].Attributes["nested"])
}

// logsBodyWithRecords builds a logs body of one resource per client id, each
// carrying `records` numbered records.
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

func TestDecodeLogsCapsRecordsPerBatch(t *testing.T) {
	const surplus = 5
	first := "8b9c1fe0-93b3-4b3a-8c1d-2f4a5e6b7c8d"
	batch, err := DecodeLogs(bytes.NewReader(logsBodyWithRecords([]string{first}, maxRecordsPerBatch+surplus)))
	require.NoError(t, err)

	records := decodedLogRecords(batch)
	require.Len(t, records, maxRecordsPerBatch)
	require.Equal(t, surplus, batch.DroppedRecords)
	require.Equal(t, first+"-"+strconv.Itoa(surplus), records[0].Body)
	require.Equal(t, first+"-"+strconv.Itoa(maxRecordsPerBatch+surplus-1), records[len(records)-1].Body)
}

func TestDecodeLogsSkipsResourcesEntirelyOverTheCap(t *testing.T) {
	older := "8b9c1fe0-93b3-4b3a-8c1d-2f4a5e6b7c8d"
	newer := "7a6b5c4d-3e2f-1a0b-9c8d-7e6f5a4b3c2d"
	batch, err := DecodeLogs(bytes.NewReader(logsBodyWithRecords([]string{older, newer}, maxRecordsPerBatch)))
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

	batch, err := DecodeMetrics(bytes.NewReader(body))
	require.NoError(t, err)
	require.Len(t, batch.Resources, 1)
	require.Len(t, batch.Resources[0].Points, maxRecordsPerBatch)
	require.Equal(t, surplus, batch.DroppedRecords)
	require.Equal(t, float64(surplus), batch.Resources[0].Points[0].Value)
}

func TestDecodeLogsLeavesNormalBatchesAlone(t *testing.T) {
	batch, err := DecodeLogs(bytes.NewReader([]byte(androidLogsFixture)))
	require.NoError(t, err)
	require.Zero(t, batch.DroppedRecords)
	require.Len(t, decodedLogRecords(batch), 1)
}
