// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"fmt"
	"time"

	"expo-open-ota/internal/database"
	"expo-open-ota/internal/database/postgres/pgdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// StateHistoryPoint is one bucket of what PostgreSQL alone can reconstruct.
//
// Two counts rather than the four the projected history carries, and they are
// not equally trustworthy. The field names say which is which, and so does
// everything the dashboard prints next to them: this series exists for
// deployments with no ClickHouse, and it would be worse than no chart at all if
// it were read as the same measurement under a different name.
type StateHistoryPoint struct {
	Timestamp time.Time `json:"timestamp"`
	// Devices RUNNING this update today, placed at the moment each of them
	// arrived on it. Rises only. A device that has since moved away is not in
	// this count at any point of the curve, including the points where it was
	// genuinely running the update.
	ArrivedDevices uint64 `json:"arrivedDevices"`
	// Devices with an unresolved fault on this update at that instant. Exact:
	// faults carry both ends, so this rises when they appear and falls when
	// they clear.
	FailingDevices uint64 `json:"failingDevices"`
}

// StateHistory reconstructs a series from PostgreSQL's live state, for
// deployments that run no ClickHouse.
//
// It is deliberately NOT called a health history. The projected one measures a
// population at each instant; this one measures a population TODAY and dates
// its members. The two agree at the right edge of the window and diverge going
// back, by exactly as much as the fleet has churned.
type StateHistory struct {
	postgres *database.Engine
}

func NewStateHistory(postgresEngine *database.Engine) *StateHistory {
	return &StateHistory{postgres: postgresEngine}
}

// Read answers one series per update, on the same bucket grid as every other
// chart on the page so the two can be read side by side.
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

	// One slot per requested update, present even when nothing landed in it: a
	// chart that silently drops an update looks like an update with no devices.
	series := make(map[string][]StateHistoryPoint, len(updateIDs))
	for _, id := range updateIDs {
		series[id] = make([]StateHistoryPoint, buckets)
		for i := range series[id] {
			series[id][i].Timestamp = from.UTC().Add(time.Duration(int64(i)*step) * time.Second)
		}
	}

	// The query returns what CHANGED in each bucket; the curve is the running
	// total. Signed, because a fault that clears takes its device back out,
	// which is the whole reason the failure series can fall.
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
		// Carry the running total forward over every bucket the deltas skipped,
		// so a quiet stretch holds its level instead of dropping to zero.
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

// clampCount refuses to report a negative population. It can only come from a
// fault whose resolution is inside the window while its start is not, which the
// query already folds into bucket zero, so this is a floor rather than a fix.
func clampCount(value int64) uint64 {
	if value < 0 {
		return 0
	}
	return uint64(value)
}
