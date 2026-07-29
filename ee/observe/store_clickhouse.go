// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"expo-open-ota/internal/database/clickhouse"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// TelemetrySink persists flattened telemetry rows. nil in wire when no
// ClickHouse is configured: the ingest routes then acknowledge and drop, the
// pre-Observe behavior.
type TelemetrySink interface {
	InsertMetrics(ctx context.Context, rows []MetricRow) error
	InsertLogs(ctx context.Context, rows []LogRow) error
}

// ClickHouseTelemetrySink writes through the native-protocol batch API: one
// POST body becomes one insert block per signal.
type ClickHouseTelemetrySink struct {
	conn driver.Conn
}

func NewClickHouseTelemetrySink(engine *clickhouse.Engine) *ClickHouseTelemetrySink {
	return &ClickHouseTelemetrySink{conn: engine.Conn}
}

// ClickHouse rejects an empty string for a UUID column, and the zero uuid is
// how this schema already spells "none" for update_id.
func orZeroUUID(value string) string {
	if value == "" {
		return ZeroUpdateID
	}
	return value
}

// ingested_at is absent from the column list: the server-side DEFAULT now()
// stamps it so query-time dedup on content_key stays honest across retries.
func (s *ClickHouseTelemetrySink) InsertMetrics(ctx context.Context, rows []MetricRow) error {
	if len(rows) == 0 {
		return nil
	}
	batch, err := s.conn.PrepareBatch(ctx, `INSERT INTO observe_metrics
		(app_id, eas_client_id, update_id, update_group_id, branch, channel, runtime_version, platform,
		 session_id, metric_name, value, route_name, custom_params, attributes,
		 os_name, os_version, device_model, country_code, lat, lng, app_version, app_build_number,
		 eas_build_id, environment, sdk_version, timestamp, content_key)`)
	if err != nil {
		return err
	}
	defer batch.Close()
	for _, row := range rows {
		if err := batch.Append(
			row.AppID, row.EASClientID, row.UpdateID, orZeroUUID(row.UpdateGroupID), row.Branch, row.Channel,
			row.RuntimeVersion, row.Platform, row.SessionID, row.MetricName,
			row.Value, row.RouteName, row.CustomParams, row.Attributes,
			row.OSName, row.OSVersion, row.DeviceModel, row.CountryCode, row.Lat, row.Lng, row.AppVersion,
			row.AppBuildNumber, row.EASBuildID, row.Environment, row.SDKVersion,
			row.Timestamp, row.ContentKey,
		); err != nil {
			return err
		}
	}
	return batch.Send()
}

func (s *ClickHouseTelemetrySink) InsertLogs(ctx context.Context, rows []LogRow) error {
	if len(rows) == 0 {
		return nil
	}
	batch, err := s.conn.PrepareBatch(ctx, `INSERT INTO observe_logs
		(app_id, eas_client_id, update_id, update_group_id, branch, channel, runtime_version, platform,
		 session_id, event_name, severity_number, severity_text, is_fatal, body, attributes,
		 os_name, os_version, device_model, country_code, lat, lng, app_version, app_build_number,
		 eas_build_id, environment, sdk_version, timestamp, content_key)`)
	if err != nil {
		return err
	}
	defer batch.Close()
	for _, row := range rows {
		isFatal := uint8(0)
		if row.IsFatal {
			isFatal = 1
		}
		if err := batch.Append(
			row.AppID, row.EASClientID, row.UpdateID, orZeroUUID(row.UpdateGroupID), row.Branch, row.Channel,
			row.RuntimeVersion, row.Platform, row.SessionID, row.EventName,
			row.SeverityNumber, row.SeverityText, isFatal, row.Body, row.Attributes,
			row.OSName, row.OSVersion, row.DeviceModel, row.CountryCode, row.Lat, row.Lng, row.AppVersion,
			row.AppBuildNumber, row.EASBuildID, row.Environment, row.SDKVersion,
			row.Timestamp, row.ContentKey,
		); err != nil {
			return err
		}
	}
	return batch.Send()
}
