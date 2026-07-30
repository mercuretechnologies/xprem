// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"fmt"
	"time"

	"xprem/internal/database"
	"xprem/internal/database/postgres/pgdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// StateHistoryPoint is one bucket of update-health state reconstructed from PostgreSQL alone.
type StateHistoryPoint struct {
	Timestamp time.Time `json:"timestamp"`
	// ArrivedDevices only rises: a device that has since moved away stays counted.
	ArrivedDevices uint64 `json:"arrivedDevices"`
	FailingDevices uint64 `json:"failingDevices"`
}

// StateHistory reconstructs a series from PostgreSQL's live state, for
// deployments that run no ClickHouse. Unlike the projected health history, it
// measures today's population dated by arrival, not the population at each instant.
type StateHistory struct {
	postgres *database.Engine
}

func NewStateHistory(postgresEngine *database.Engine) *StateHistory {
	return &StateHistory{postgres: postgresEngine}
}

// Read answers one series per update, on the same bucket grid as other charts on the page.
func (s *StateHistory) Read(
	ctx context.Context,
	appID string,
	updateIDs []string,
	from, to time.Time,
) (map[string][]StateHistoryPoint, error) {
	if s == nil || s.postgres == nil || len(updateIDs) == 0 || !to.After(from) {
		return map[string][]StateHistoryPoint{}, nil
	}

	appUUID, err := uuid.Parse(appID)
	if err != nil {
		return nil, fmt.Errorf("parsing app id: %w", err)
	}
	ids := make([]pgtype.UUID, 0, len(updateIDs))
	for _, id := range updateIDs {
		parsed, err := uuid.Parse(id)
		if err != nil {
			return nil, fmt.Errorf("parsing update id: %w", err)
		}
		ids = append(ids, pgtype.UUID{Bytes: parsed, Valid: true})
	}

	step := int64(observeBucket(to.Sub(from)).Seconds())
	buckets := int(to.Sub(from).Seconds()/float64(step)) + 1

	rows, err := s.postgres.ListUpdateHealthStateDeltas(ctx, pgdb.ListUpdateHealthStateDeltasParams{
		AppID:       pgtype.UUID{Bytes: appUUID, Valid: true},
		UpdateIds:   ids,
		FromTs:      pgtype.Timestamptz{Time: from.UTC(), Valid: true},
		ToTs:        pgtype.Timestamptz{Time: to.UTC(), Valid: true},
		StepSeconds: int32(step),
	})
	if err != nil {
		return nil, fmt.Errorf("reading update health from state: %w", err)
	}

	// One slot per requested update, even when nothing landed in it.
	series := make(map[string][]StateHistoryPoint, len(updateIDs))
	for _, id := range updateIDs {
		series[id] = make([]StateHistoryPoint, buckets)
		for i := range series[id] {
			series[id][i].Timestamp = from.UTC().Add(time.Duration(int64(i)*step) * time.Second)
		}
	}

	// The query returns what changed in each bucket; the curve is the running total.
	type running struct{ arrived, failing int64 }
	totals := make(map[string]*running, len(updateIDs))
	cursor := make(map[string]int, len(updateIDs))
	for _, row := range rows {
		key := uuid.UUID(row.UpdateUuid.Bytes).String()
		points, tracked := series[key]
		if !tracked {
			continue
		}
		total, seen := totals[key]
		if !seen {
			total = &running{}
			totals[key] = total
		}
		index := int(row.BucketIndex)
		if index < 0 {
			index = 0
		}
		if index >= buckets {
			continue
		}
		// Carry the running total forward over skipped buckets.
		for i := cursor[key]; i < index; i++ {
			points[i].ArrivedDevices = clampCount(total.arrived)
			points[i].FailingDevices = clampCount(total.failing)
		}
		total.arrived += row.AdoptedDelta
		total.failing += row.FailingDelta
		points[index].ArrivedDevices = clampCount(total.arrived)
		points[index].FailingDevices = clampCount(total.failing)
		cursor[key] = index + 1
	}
	for key, points := range series {
		total := totals[key]
		if total == nil {
			continue
		}
		for i := cursor[key]; i < buckets; i++ {
			points[i].ArrivedDevices = clampCount(total.arrived)
			points[i].FailingDevices = clampCount(total.failing)
		}
	}
	return series, nil
}

// clampCount floors a negative population at zero.
func clampCount(value int64) uint64 {
	if value < 0 {
		return 0
	}
	return uint64(value)
}
