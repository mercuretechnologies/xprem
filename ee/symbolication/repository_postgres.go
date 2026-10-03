// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package symbolication

import (
	"context"
	"fmt"
	"strconv"
	"time"
	"xprem/internal/database"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/repository"
	"xprem/internal/types"
)

// IndexRepository is the durable record of every index job, the one the
// dashboard reads; River's own rows are purged within days.
type IndexRepository interface {
	MarkPending(ctx context.Context, update types.Update, hash string) error
	// MarkRunning records the attempt about to run, creating the record when
	// the job was scheduled without one.
	MarkRunning(ctx context.Context, update types.Update, hash string, attempt int) error
	Finish(ctx context.Context, update types.Update, status types.SourcemapIndexStatus, reason string, segments *int, indexSize *int64) error
	// GetUpdateSourcemap answers nil when the update does not exist.
	GetUpdateSourcemap(ctx context.Context, appId, branch, runtimeVersion, updateId string) (*UpdateSourcemap, error)
	// GetUpdateSourcemapByUUID is GetUpdateSourcemap for the UUID a device
	// reports; the internal update identity is included for index repair.
	GetUpdateSourcemapByUUID(ctx context.Context, appId, updateUUID string) (*UpdateSourcemap, error)
}

// UpdateSourcemap is what the dashboard shows about an update's source map:
// the map it carries, nil when it was published without one, and the index
// record once a job handled it.
type UpdateSourcemap struct {
	// Update is filled by the UUID lookup for jobs that rebuild a derived index.
	Update types.Update          `json:"-"`
	Hash   *string               `json:"hash"`
	Index  *types.SourcemapIndex `json:"index"`
}

type PostgresIndexRepository struct {
	engine *database.Engine
}

func NewPostgresIndexRepository(engine *database.Engine) *PostgresIndexRepository {
	return &PostgresIndexRepository{engine: engine}
}

// MarkPending records an index job about to be scheduled, resetting the row
// if the map was already handled.
func (r *PostgresIndexRepository) MarkPending(ctx context.Context, update types.Update, hash string) error {
	return r.upsert(ctx, update, hash, types.SourcemapIndexPending, 0)
}

func (r *PostgresIndexRepository) MarkRunning(ctx context.Context, update types.Update, hash string, attempt int) error {
	return r.upsert(ctx, update, hash, types.SourcemapIndexRunning, attempt)
}

func (r *PostgresIndexRepository) upsert(ctx context.Context, update types.Update, hash string, status types.SourcemapIndexStatus, attempts int) error {
	id, err := repository.ParseUpdateID("update id", update.UpdateId)
	if err != nil {
		return err
	}
	rows, err := r.engine.Queries.UpsertSourcemapIndex(ctx, pgdb.UpsertSourcemapIndexParams{
		UpdateID:   id,
		Hash:       hash,
		Status:     status,
		Attempts:   int32(attempts),
		AppID:      repository.ToPgUUID(update.AppId),
		BranchName: update.Branch,
	})
	if err != nil {
		return fmt.Errorf("failed to mark the sourcemap index %s in database: %w", status, err)
	}
	if rows == 0 {
		return fmt.Errorf("branch %q of app %s not found", update.Branch, update.AppId)
	}
	return nil
}

// Finish records how the job ended. reason is empty for a stored index.
func (r *PostgresIndexRepository) Finish(ctx context.Context, update types.Update, status types.SourcemapIndexStatus, reason string, segments *int, indexSize *int64) error {
	id, err := repository.ParseUpdateID("update id", update.UpdateId)
	if err != nil {
		return err
	}
	var reasonPtr *string
	if reason != "" {
		reasonPtr = &reason
	}
	var segmentsPtr *int32
	if segments != nil {
		count := int32(*segments)
		segmentsPtr = &count
	}
	rows, err := r.engine.Queries.FinishSourcemapIndex(ctx, pgdb.FinishSourcemapIndexParams{
		Status:     status,
		Reason:     reasonPtr,
		Segments:   segmentsPtr,
		IndexSize:  indexSize,
		AppID:      repository.ToPgUUID(update.AppId),
		BranchName: update.Branch,
		UpdateID:   id,
	})
	if err != nil {
		return fmt.Errorf("failed to finish the sourcemap index in database: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("no sourcemap index record for update %s on branch %s", update.UpdateId, update.Branch)
	}
	return nil
}

func (r *PostgresIndexRepository) GetUpdateSourcemap(ctx context.Context, appId, branch, runtimeVersion, updateId string) (*UpdateSourcemap, error) {
	id, err := repository.ParseUpdateID("update id", updateId)
	if err != nil {
		return nil, err
	}
	row, err := r.engine.Queries.GetUpdateSourcemap(ctx, pgdb.GetUpdateSourcemapParams{
		AppID:          repository.ToPgUUID(appId),
		BranchName:     branch,
		RuntimeVersion: runtimeVersion,
		UpdateID:       id,
	})
	if err != nil {
		if database.IsNoRows(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read the update sourcemap from database: %w", err)
	}
	sourcemap := &UpdateSourcemap{Hash: row.SourcemapHash}
	// Empty status: no index row joined.
	if row.IndexStatus == "" || row.SourcemapHash == nil {
		return sourcemap, nil
	}
	index := &types.SourcemapIndex{
		Hash:      *row.SourcemapHash,
		Status:    row.IndexStatus,
		IndexSize: row.IndexSize,
		UpdatedAt: row.IndexUpdatedAt.Time.UTC().Format(time.RFC3339),
	}
	if row.IndexAttempts != nil {
		index.Attempts = int(*row.IndexAttempts)
	}
	if row.IndexReason != nil {
		index.Reason = *row.IndexReason
	}
	if row.IndexSegments != nil {
		count := int(*row.IndexSegments)
		index.Segments = &count
	}
	sourcemap.Index = index
	return sourcemap, nil
}

func (r *PostgresIndexRepository) GetUpdateSourcemapByUUID(ctx context.Context, appId, updateUUID string) (*UpdateSourcemap, error) {
	row, err := r.engine.Queries.GetUpdateSourcemapByUUID(ctx, pgdb.GetUpdateSourcemapByUUIDParams{
		AppID:      repository.ToPgUUID(appId),
		UpdateUuid: repository.ToPgUUID(updateUUID),
	})
	if err != nil {
		if database.IsNoRows(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read the update sourcemap from database: %w", err)
	}
	sourcemap := &UpdateSourcemap{
		Hash:   row.SourcemapHash,
		Update: types.Update{AppId: appId, Branch: row.BranchName, RuntimeVersion: row.RuntimeVersion, UpdateId: strconv.FormatInt(row.ID, 10)},
	}
	if row.IndexStatus != "" && row.SourcemapHash != nil {
		sourcemap.Index = &types.SourcemapIndex{Hash: *row.SourcemapHash, Status: row.IndexStatus}
	}
	return sourcemap, nil
}
