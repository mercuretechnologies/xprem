// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"expo-open-ota/ee/identity"
	"expo-open-ota/internal/database"
	"expo-open-ota/internal/database/postgres"
	"expo-open-ota/internal/database/postgres/pgdb"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// serveIngest routes a request through the handler only, without middleware.
func serveIngest(handler *IngestHandler, method, path string, body []byte) *httptest.ResponseRecorder {
	router := mux.NewRouter()
	router.HandleFunc("/observe/{APP_ID}/{PROJECT_ID}/v1/logs", handler.HandleLogs).Methods(http.MethodPost)
	router.HandleFunc("/observe/{APP_ID}/{PROJECT_ID}/v1/metrics", handler.HandleMetrics).Methods(http.MethodPost)
	req := httptest.NewRequestWithContext(context.Background(), method, path, bytes.NewReader(body))
	req.RemoteAddr = "203.0.113.9:40000"
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	return recorder
}

type recordedFailure struct {
	device      string
	updateIDs   []string
	fatal       string
	failureType identity.FailureType
}

type recordedRuntimeSignal struct {
	kind       string
	device     string
	updateID   string
	occurredAt time.Time
}

type recordingMutator struct {
	identity.Store
	sets         []map[string]any
	unsets       [][]string
	failures     []recordedFailure
	runtime      []recordedRuntimeSignal
	fail         bool
	failFailures bool
	hadDeadline  bool
}

func (m *recordingMutator) RecordUpdateFailures(_ context.Context, _ string, easClientID string, updateIDs []string, fatalError string, failureType identity.FailureType) error {
	if m.failFailures {
		return fmt.Errorf("database is down")
	}
	m.failures = append(m.failures, recordedFailure{device: easClientID, updateIDs: updateIDs, fatal: fatalError, failureType: failureType})
	return nil
}

func (m *recordingMutator) RecordRuntimeFailure(_ context.Context, _ string, easClientID string, updateID string, _ string, occurredAt time.Time) error {
	if m.failFailures {
		return fmt.Errorf("database is down")
	}
	m.runtime = append(m.runtime, recordedRuntimeSignal{
		kind: "failure", device: easClientID, updateID: updateID, occurredAt: occurredAt,
	})
	return nil
}

func (m *recordingMutator) ResolveRuntimeFailure(_ context.Context, _ string, easClientID string, updateID string, occurredAt time.Time) error {
	if m.failFailures {
		return fmt.Errorf("database is down")
	}
	m.runtime = append(m.runtime, recordedRuntimeSignal{
		kind: "recovered", device: easClientID, updateID: updateID, occurredAt: occurredAt,
	})
	return nil
}

func (m *recordingMutator) ApplySet(ctx context.Context, _ string, _ string, raw map[string]any, _ *identity.Geo) (identity.ApplyResult, error) {
	_, m.hadDeadline = ctx.Deadline()
	if m.fail {
		return identity.ApplyResult{}, fmt.Errorf("database is down")
	}
	m.sets = append(m.sets, raw)
	return identity.ApplyResult{}, nil
}

func (m *recordingMutator) ApplySetOnce(_ context.Context, _ string, _ string, raw map[string]any, _ *identity.Geo) (identity.ApplyResult, error) {
	return identity.ApplyResult{}, nil
}

func (m *recordingMutator) ApplyUnset(_ context.Context, _ string, _ string, keys []string, _ *identity.Geo) (identity.ApplyResult, error) {
	if m.fail {
		return identity.ApplyResult{}, fmt.Errorf("database is down")
	}
	m.unsets = append(m.unsets, keys)
	return identity.ApplyResult{}, nil
}

const logsPath = "/observe/app-1/ignored-project/v1/logs"

func TestHandleLogsResponseContract(t *testing.T) {
	t.Run("nil service acknowledges and drops", func(t *testing.T) {
		recorder := serveIngest(NewIngestHandler(nil, nil, nil, nil), http.MethodPost, logsPath, []byte(androidLogsFixture))
		require.Equal(t, http.StatusNoContent, recorder.Code)
	})

	t.Run("unreadable body is a permanent 400", func(t *testing.T) {
		handler := NewIngestHandler(identity.NewService(&recordingMutator{}, nil), nil, nil, nil)
		recorder := serveIngest(handler, http.MethodPost, logsPath, []byte("not json"))
		require.Equal(t, http.StatusBadRequest, recorder.Code)
	})

	t.Run("oversized body is acknowledged so the device can move on", func(t *testing.T) {
		handler := NewIngestHandler(identity.NewService(&recordingMutator{}, nil), nil, nil, nil)
		big := append([]byte(`{"resourceLogs":[{"resource":{"attributes":[{"key":"pad","value":{"stringValue":"`),
			bytes.Repeat([]byte("x"), maxBatchBodyBytes+1)...)
		big = append(big, []byte(`"}}]}}]}`)...)
		recorder := serveIngest(handler, http.MethodPost, logsPath, big)
		require.Equal(t, http.StatusOK, recorder.Code)

		var response struct {
			PartialSuccess struct {
				ErrorMessage string `json:"errorMessage"`
			} `json:"partialSuccess"`
		}
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
		require.Contains(t, response.PartialSuccess.ErrorMessage, "exceeded",
			"a client that reads the body must learn why nothing landed")
	})

	t.Run("over the record cap is a partial success, never a rejection", func(t *testing.T) {
		const surplus = 4
		sink := &capturingSink{}
		handler := NewIngestHandler(identity.NewService(&recordingMutator{}, nil), sink, nil, nil)
		body := logsBodyWithRecords([]string{"8b9c1fe0-93b3-4b3a-8c1d-2f4a5e6b7c8d"}, maxRecordsPerBatch+surplus)

		recorder := serveIngest(handler, http.MethodPost, logsPath, body)
		require.Equal(t, http.StatusOK, recorder.Code)

		var answer struct {
			PartialSuccess struct {
				RejectedLogRecords int    `json:"rejectedLogRecords"`
				ErrorMessage       string `json:"errorMessage"`
			} `json:"partialSuccess"`
		}
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &answer))
		require.Equal(t, surplus, answer.PartialSuccess.RejectedLogRecords)
		require.NotEmpty(t, answer.PartialSuccess.ErrorMessage)
		require.Len(t, sink.logs, maxRecordsPerBatch)
	})

	t.Run("identity operations are capped per batch", func(t *testing.T) {
		mutator := &recordingMutator{}
		handler := NewIngestHandler(identity.NewService(mutator, nil), nil, nil, nil)

		var records []string
		for i := 0; i < maxIdentityOpsPerBatch*3; i++ {
			op := "$set"
			if i%2 == 1 {
				op = "$set_once"
			}
			records = append(records, fmt.Sprintf(
				`{"timeUnixNano":1,"attributes":[{"key":"event.name","value":{"stringValue":"%s"}},{"key":"userId","value":{"stringValue":"u%d"}}]}`,
				op, i))
		}
		body := []byte(`{"resourceLogs":[{"resource":{"attributes":[{"key":"expo.eas_client.id","value":{"stringValue":"8b9c1fe0-93b3-4b3a-8c1d-2f4a5e6b7c8d"}}]},"scopeLogs":[{"logRecords":[` +
			strings.Join(records, ",") + `]}]}]}`)

		recorder := serveIngest(handler, http.MethodPost, logsPath, body)
		require.Equal(t, http.StatusNoContent, recorder.Code, "a capped batch is still accepted")
		require.LessOrEqual(t, len(mutator.sets), maxIdentityOpsPerBatch)
		require.NotEmpty(t, mutator.sets, "the ceiling bounds the work, it does not refuse it")
	})

	t.Run("two installations in one body is a permanent 400", func(t *testing.T) {
		handler := NewIngestHandler(identity.NewService(&recordingMutator{}, nil), &capturingSink{}, nil, nil)
		body := logsBodyWithRecords([]string{
			"8b9c1fe0-93b3-4b3a-8c1d-2f4a5e6b7c8d",
			"7a6b5c4d-3e2f-1a0b-9c8d-7e6f5a4b3c2d",
		}, 1)

		recorder := serveIngest(handler, http.MethodPost, logsPath, body)
		require.Equal(t, http.StatusBadRequest, recorder.Code)
	})

	t.Run("one installation spelled two ways is still one installation", func(t *testing.T) {
		handler := NewIngestHandler(identity.NewService(&recordingMutator{}, nil), &capturingSink{}, nil, nil)
		body := logsBodyWithRecords([]string{
			"8b9c1fe0-93b3-4b3a-8c1d-2f4a5e6b7c8d",
			"8B9C1FE0-93B3-4B3A-8C1D-2F4A5E6B7C8D",
		}, 1)

		recorder := serveIngest(handler, http.MethodPost, logsPath, body)
		require.Equal(t, http.StatusNoContent, recorder.Code)
	})

	t.Run("store failure is a retryable 503, never 500", func(t *testing.T) {
		handler := NewIngestHandler(identity.NewService(&recordingMutator{fail: true}, nil), nil, nil, nil)
		recorder := serveIngest(handler, http.MethodPost, logsPath, []byte(androidLogsFixture))
		require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	})

	t.Run("telemetry-only batch is acknowledged untouched", func(t *testing.T) {
		mutator := &recordingMutator{}
		handler := NewIngestHandler(identity.NewService(mutator, nil), nil, nil, nil)
		body := strings.ReplaceAll(androidLogsFixture, "$set", "exception")
		recorder := serveIngest(handler, http.MethodPost, logsPath, []byte(body))
		require.Equal(t, http.StatusNoContent, recorder.Code)
		require.Empty(t, mutator.sets)
	})

	t.Run("forged client id skips records without failing the batch", func(t *testing.T) {
		mutator := &recordingMutator{}
		handler := NewIngestHandler(identity.NewService(mutator, nil), nil, nil, nil)
		body := strings.ReplaceAll(androidLogsFixture, "8b9c1fe0-93b3-4b3a-8c1d-2f4a5e6b7c8d", "not-a-uuid")
		recorder := serveIngest(handler, http.MethodPost, logsPath, []byte(body))
		require.Equal(t, http.StatusNoContent, recorder.Code)
		require.Empty(t, mutator.sets)
	})

	t.Run("identity ops reach the service", func(t *testing.T) {
		mutator := &recordingMutator{}
		handler := NewIngestHandler(identity.NewService(mutator, nil), nil, nil, nil)
		recorder := serveIngest(handler, http.MethodPost, logsPath, []byte(androidLogsFixture))
		require.Equal(t, http.StatusNoContent, recorder.Code)
		require.True(t, mutator.hadDeadline, "each identity apply must have a request-scoped deadline")
		require.Len(t, mutator.sets, 1)

		recorder = serveIngest(handler, http.MethodPost, logsPath, []byte(iosLogsFixture))
		require.Equal(t, http.StatusNoContent, recorder.Code)
		require.Equal(t, [][]string{{"userId", "tenant"}}, mutator.unsets)
	})

	t.Run("metrics stub acknowledges and drops", func(t *testing.T) {
		recorder := serveIngest(NewIngestHandler(nil, nil, nil, nil), http.MethodPost, "/observe/app-1/p/v1/metrics", []byte(`{"resourceMetrics":[]}`))
		require.Equal(t, http.StatusNoContent, recorder.Code)
	})
}

const jsCrashLogsFixture = `{
  "resourceLogs": [
    {
      "resource": {
        "attributes": [
          { "key": "expo.eas_client.id", "value": { "stringValue": "8b9c1fe0-93b3-4b3a-8c1d-2f4a5e6b7c8d" } },
          { "key": "expo.app.updates.id", "value": { "stringValue": "B16FA250-1B5F-42E9-A012-3F4A5E6B7C8D" } }
        ]
      },
      "scopeLogs": [
        {
          "scope": { "name": "expo-observe", "version": "56.0.16" },
          "logRecords": [
            {
              "timeUnixNano": 1767960489000000000,
              "severityNumber": 17,
              "severityText": "ERROR",
              "body": { "stringValue": "" },
              "attributes": [
                { "key": "session.id", "value": { "stringValue": "aaaa-1111" } },
                { "key": "event.name", "value": { "stringValue": "expo_open_ota_js_crash" } },
                { "key": "message", "value": { "stringValue": "TypeError: undefined is not a function" } }
              ]
            },
            {
              "timeUnixNano": 1767960490000000000,
              "severityNumber": 17,
              "severityText": "ERROR",
              "body": { "stringValue": "" },
              "attributes": [
                { "key": "session.id", "value": { "stringValue": "bbbb-2222" } },
                { "key": "event.name", "value": { "stringValue": "expo_open_ota_js_crash" } }
              ]
            }
          ]
        }
      ]
    },
    {
      "resource": {
        "attributes": [
          { "key": "expo.eas_client.id", "value": { "stringValue": "8b9c1fe0-93b3-4b3a-8c1d-2f4a5e6b7c8d" } }
        ]
      },
      "scopeLogs": [
        {
          "scope": { "name": "expo-observe", "version": "56.0.16" },
          "logRecords": [
            {
              "timeUnixNano": 1767960489000000000,
              "severityNumber": 17,
              "severityText": "ERROR",
              "body": { "stringValue": "" },
              "attributes": [
                { "key": "event.name", "value": { "stringValue": "expo_open_ota_js_crash" } },
                { "key": "message", "value": { "stringValue": "embedded bundle crash" } }
              ]
            }
          ]
        }
      ]
    }
  ]
}`

func TestHandleLogsJSCrashProjection(t *testing.T) {
	t.Run("crash events land as runtime failures, deduped per device+update", func(t *testing.T) {
		mutator := &recordingMutator{}
		handler := NewIngestHandler(identity.NewService(mutator, nil), nil, nil, nil)
		recorder := serveIngest(handler, http.MethodPost, logsPath, []byte(jsCrashLogsFixture))
		require.Equal(t, http.StatusNoContent, recorder.Code)
		require.Len(t, mutator.runtime, 1)
		failure := mutator.runtime[0]
		require.Equal(t, "failure", failure.kind)
		require.Equal(t, "8b9c1fe0-93b3-4b3a-8c1d-2f4a5e6b7c8d", failure.device)
		require.Equal(t, "b16fa250-1b5f-42e9-a012-3f4a5e6b7c8d", failure.updateID)
	})

	t.Run("failure-store outage is a retryable 503", func(t *testing.T) {
		mutator := &recordingMutator{failFailures: true}
		handler := NewIngestHandler(identity.NewService(mutator, nil), nil, nil, nil)
		recorder := serveIngest(handler, http.MethodPost, logsPath, []byte(jsCrashLogsFixture))
		require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	})

	t.Run("nil identity service skips the projection", func(t *testing.T) {
		recorder := serveIngest(NewIngestHandler(nil, nil, nil, nil), http.MethodPost, logsPath, []byte(jsCrashLogsFixture))
		require.Equal(t, http.StatusNoContent, recorder.Code)
	})
}

const runtimeRecoveryLogsFixture = `{
  "resourceLogs": [{
    "resource": {"attributes": [
      {"key": "expo.eas_client.id", "value": {"stringValue": "8b9c1fe0-93b3-4b3a-8c1d-2f4a5e6b7c8d"}},
      {"key": "expo.app.updates.id", "value": {"stringValue": "b16fa250-1b5f-42e9-a012-3f4a5e6b7c8d"}},
      {"key": "os.name", "value": {"stringValue": "ios"}}
    ]},
    "scopeLogs": [{"scope": {"name": "expo-observe"}, "logRecords": [
      {
        "timeUnixNano": 1767960490000000000,
        "attributes": [{"key": "event.name", "value": {"stringValue": "app_started"}}]
      },
      {
        "timeUnixNano": 1767960489000000000,
        "attributes": [{"key": "event.name", "value": {"stringValue": "expo_open_ota_js_crash"}}]
      }
    ]}]
  }]
}`

func TestHandleLogsRuntimeRecoveryUsesEventOrder(t *testing.T) {
	mutator := &recordingMutator{}
	handler := NewIngestHandler(identity.NewService(mutator, nil), nil, nil, nil)
	recorder := serveIngest(handler, http.MethodPost, logsPath, []byte(runtimeRecoveryLogsFixture))
	require.Equal(t, http.StatusNoContent, recorder.Code)
	require.Equal(t, []recordedRuntimeSignal{
		{
			kind:       "failure",
			device:     "8b9c1fe0-93b3-4b3a-8c1d-2f4a5e6b7c8d",
			updateID:   "b16fa250-1b5f-42e9-a012-3f4a5e6b7c8d",
			occurredAt: time.Unix(1767960489, 0).UTC(),
		},
		{
			kind:       "recovered",
			device:     "8b9c1fe0-93b3-4b3a-8c1d-2f4a5e6b7c8d",
			updateID:   "b16fa250-1b5f-42e9-a012-3f4a5e6b7c8d",
			occurredAt: time.Unix(1767960490, 0).UTC(),
		},
	}, mutator.runtime)
}

func TestNormalizeRuntimeHealthSignalsOrdersAndCompacts(t *testing.T) {
	firstCrash := time.Unix(1767960489, 0).UTC()
	tiedAt := firstCrash.Add(time.Second)
	signals := []runtimeHealthSignal{
		{state: runtimeFaulty, fatalError: "later crash", occurredAt: tiedAt},
		{state: runtimeHealthy, occurredAt: tiedAt},
		{state: runtimeHealthy, occurredAt: firstCrash.Add(500 * time.Millisecond)},
		{state: runtimeFaulty, fatalError: "first crash", occurredAt: firstCrash},
	}

	require.Equal(t, []runtimeHealthSignal{
		{state: runtimeFaulty, fatalError: "first crash", occurredAt: firstCrash},
		{state: runtimeHealthy, occurredAt: tiedAt},
		{state: runtimeFaulty, fatalError: "later crash", occurredAt: tiedAt},
	}, normalizeRuntimeHealthSignals(signals))
}

func TestIngestEndToEnd(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_DATABASE_URL must be set in CI")
		}
		t.Skip("TEST_DATABASE_URL not set")
	}
	t.Setenv("ADMIN_EMAIL", "seed-admin@example.com")
	t.Setenv("ADMIN_PASSWORD", "Sup3rSecret!")
	postgres.RunDBMigrations(dbURL)
	pool, err := pgxpool.New(context.Background(), dbURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	appID := uuid.NewString()
	_, err = pool.Exec(context.Background(), "INSERT INTO apps (id, name) VALUES ($1, $2)", appID, "observe-e2e")
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DELETE FROM apps WHERE id = $1", appID) })

	identityStore := identity.NewPostgresIdentityStore(&database.Engine{Queries: pgdb.New(pool), DB: pool})
	for _, spec := range []identity.KeySpec{
		{Key: "userId", Type: identity.ValueTypeString},
		{Key: "seats", Type: identity.ValueTypeNumber},
		{Key: "isInternal", Type: identity.ValueTypeBoolean},
	} {
		_, err := identityStore.UpsertSchemaKey(context.Background(), appID, spec)
		require.NoError(t, err)
	}

	handler := NewIngestHandler(identity.NewService(identityStore, nil), nil, nil, nil)
	path := "/observe/" + appID + "/whatever-project/v1/logs"
	recorder := serveIngest(handler, http.MethodPost, path, []byte(androidLogsFixture))
	require.Equal(t, http.StatusNoContent, recorder.Code)

	device, err := identityStore.GetDevice(context.Background(), appID, "8b9c1fe0-93b3-4b3a-8c1d-2f4a5e6b7c8d")
	require.NoError(t, err)
	require.NotNil(t, device, "the registry is community: an identify still registers the device")
	require.Empty(t, device.Metadata)
}

func TestIdentityRequestsFromBatch(t *testing.T) {
	batch, err := DecodeLogs(bytes.NewReader([]byte(androidLogsFixture)))
	require.NoError(t, err)
	requests := identityRequestsFromBatch(batch, "app-1", "203.0.113.7")
	require.Len(t, requests, 1)
	require.Equal(t, identity.OpSet, requests[0].Op)
	require.Equal(t, "app-1", requests[0].AppID)
	require.Equal(t, "203.0.113.7", requests[0].RemoteIP)
	require.Equal(t, "user_42", requests[0].Attributes["userId"])

	t.Run("forged client id yields no requests", func(t *testing.T) {
		body := strings.ReplaceAll(androidLogsFixture, "8b9c1fe0-93b3-4b3a-8c1d-2f4a5e6b7c8d", "not-a-uuid")
		b, err := DecodeLogs(bytes.NewReader([]byte(body)))
		require.NoError(t, err)
		require.Empty(t, identityRequestsFromBatch(b, "app-1", ""))
	})

	t.Run("telemetry records yield no requests", func(t *testing.T) {
		body := strings.ReplaceAll(androidLogsFixture, "$set", "exception")
		b, err := DecodeLogs(bytes.NewReader([]byte(body)))
		require.NoError(t, err)
		require.Empty(t, identityRequestsFromBatch(b, "app-1", ""))
	})
}

func TestIdentityPassLeavesTelemetryRecognizable(t *testing.T) {
	for _, op := range []string{"$set", "$set_once"} {
		t.Run(op, func(t *testing.T) {
			batch, err := DecodeLogs(bytes.NewReader([]byte(strings.ReplaceAll(androidLogsFixture, "$set", op))))
			require.NoError(t, err)

			requests := identityRequestsFromBatch(batch, "app-1", "203.0.113.7")
			require.Len(t, requests, 1)
			require.Equal(t, "user_42", requests[0].Attributes["userId"])

			rows := FlattenLogs("app-1", batch, time.Now().UTC())
			require.Empty(t, rows, "an identity record must never reach the telemetry store")

			attrs := batch.Resources[0].Records[0].Attributes
			require.Equal(t, op, attrs[EventNameKey], "the decoded record is the caller's, not the request's")
			require.Equal(t, "aaaa-1111", attrs[sessionIDKey])
		})
	}

	t.Run("coalescing does not write back into the records", func(t *testing.T) {
		batch, err := DecodeLogs(bytes.NewReader([]byte(androidLogsFixture)))
		require.NoError(t, err)
		requests := identityRequestsFromBatch(batch, "app-1", "")
		require.Len(t, requests, 1)

		folded := identity.CoalesceRequests(append(requests, identity.Request{
			AppID: "app-1", EASClientID: requests[0].EASClientID, Op: identity.OpSet,
			Attributes: map[string]any{"tenant": "acme"},
		}))
		require.Len(t, folded, 1)
		require.Equal(t, "acme", folded[0].Attributes["tenant"])
		require.NotContains(t, batch.Resources[0].Records[0].Attributes, "tenant")
	})
}

type capturingSink struct {
	metrics []MetricRow
	logs    []LogRow
}

func (s *capturingSink) InsertMetrics(_ context.Context, rows []MetricRow) error {
	s.metrics = append(s.metrics, rows...)
	return nil
}

func (s *capturingSink) InsertLogs(_ context.Context, rows []LogRow) error {
	s.logs = append(s.logs, rows...)
	return nil
}

func TestHandleLogsEnrichesRowsWithPlace(t *testing.T) {
	sink := &capturingSink{}
	handler := NewIngestHandler(identity.NewService(&recordingMutator{}, nil), sink, nil, nil)
	body := strings.ReplaceAll(androidLogsFixture, "$set", "exception")
	recorder := serveIngest(handler, http.MethodPost, logsPath, []byte(body))
	require.Equal(t, http.StatusNoContent, recorder.Code)
	require.NotEmpty(t, sink.logs)

	for _, row := range sink.logs {
		require.Empty(t, row.CountryCode)
		require.Nil(t, row.Lat)
		require.Nil(t, row.Lng)
	}
}

func TestFlattenBoundsHostileResourceAttributes(t *testing.T) {
	huge := strings.Repeat("A", maxResourceValueRunes*4)
	envelope := newEnvelope(testAppID, map[string]any{
		deviceModelKey:    huge,
		osNameKey:         huge,
		osVersionKey:      huge,
		appVersionKey:     huge,
		appBuildNumberKey: huge,
		easBuildIDKey:     huge,
		environmentKey:    huge,
	})

	for name, value := range map[string]string{
		"deviceModel":    envelope.DeviceModel,
		"osName":         envelope.OSName,
		"osVersion":      envelope.OSVersion,
		"appVersion":     envelope.AppVersion,
		"appBuildNumber": envelope.AppBuildNumber,
		"easBuildID":     envelope.EASBuildID,
		"environment":    envelope.Environment,
	} {
		require.Len(t, []rune(value), maxResourceValueRunes, name)
	}

	short := newEnvelope(testAppID, map[string]any{deviceModelKey: "SM-A546B"})
	require.Equal(t, "SM-A546B", short.DeviceModel)
}

func TestRuntimeHealthBoundsTheNumberOfGroups(t *testing.T) {
	mutator := &recordingMutator{}
	handler := NewIngestHandler(identity.NewService(mutator, nil), nil, nil, nil)

	device := "8b9c1fe0-93b3-4b3a-8c1d-2f4a5e6b7c8d"
	var resources []string
	for i := 0; i < maxRuntimeGroupsPerBatch*10; i++ {
		resources = append(resources, fmt.Sprintf(
			`{"resource":{"attributes":[{"key":"expo.eas_client.id","value":{"stringValue":"%s"}},{"key":"expo.app.updates.id","value":{"stringValue":"%s"}}]},"scopeLogs":[{"logRecords":[{"timeUnixNano":1767960489000000000,"attributes":[{"key":"event.name","value":{"stringValue":"%s"}}]}]}]}`,
			device, uuid.NewString(), JSCrashEventName))
	}
	body := []byte(`{"resourceLogs":[` + strings.Join(resources, ",") + `]}`)

	recorder := serveIngest(handler, http.MethodPost, logsPath, body)
	require.Equal(t, http.StatusNoContent, recorder.Code)
	require.LessOrEqual(t, len(mutator.runtime), maxRuntimeGroupsPerBatch,
		"one request must not order a write per invented update id")
}

func TestKeepNewestIdentityWork(t *testing.T) {
	value := func(req identity.Request) string { return req.Attributes["level"].(string) }

	total := maxIdentityOpsPerBatch * 2
	requests := make([]identity.Request, 0, total)
	for i := 0; i < total; i++ {
		requests = append(requests, identity.Request{
			AppID: "app", EASClientID: "d1", Op: identity.OpSet,
			Attributes: map[string]any{"level": fmt.Sprintf("v%03d", i)},
		})
	}

	kept := keepNewestIdentityWork(requests)
	require.Len(t, kept, maxIdentityOpsPerBatch)
	require.Equal(t, fmt.Sprintf("v%03d", total-1), value(kept[len(kept)-1]),
		"the device's final value must be the one that lands")
	require.Equal(t, fmt.Sprintf("v%03d", total-maxIdentityOpsPerBatch), value(kept[0]))

	// Under the ceiling nothing is touched, not even reallocated.
	short := requests[:3]
	require.Equal(t, short, keepNewestIdentityWork(short))
}
