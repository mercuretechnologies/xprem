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
	MarkRunning(ctx context.Context, update types.Update) error
	Finish(ctx context.Context, update types.Update, status types.SourcemapIndexStatus, reason string, segments *int, indexSize *int64) error
	// GetUpdateSourcemap answers nil when the update does not exist.
	GetUpdateSourcemap(ctx context.Context, appId, branch, runtimeVersion, updateId string) (*UpdateSourcemap, error)
	// GetUpdateSourcemapByUUID is GetUpdateSourcemap for the UUID a device
	// reports; only the index status is filled.
	GetUpdateSourcemapByUUID(ctx context.Context, appId, updateUUID string) (*UpdateSourcemap, error)
}

// UpdateSourcemap is what the dashboard shows about an update's source map:
// the map it carries, nil when it was published without one, and the index
// record once a job handled it.
type UpdateSourcemap struct {
	Hash  *string               `json:"hash"`
	Index *types.SourcemapIndex `json:"index"`
}

type PostgresIndexRepository struct {
	engine *database.Engine
}

// NewPostgresIndexRepository binds index records to the database engine.
func NewPostgresIndexRepository(engine *database.Engine) *PostgresIndexRepository {
	return &PostgresIndexRepository{engine: engine}
}

// updateID parses the decimal update id as an int64, wrapping syntax and
// range errors from strconv.ParseInt.
func updateID(update types.Update) (int64, error) {
	id, err := strconv.ParseInt(update.UpdateId, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse update id: %w", err)
	}
	return id, nil
}

// MarkPending records an index job about to be scheduled, resetting the row
// if the map was already handled. Invalid numeric update ids, database
// failures, and missing branches return errors.
func (r *PostgresIndexRepository) MarkPending(ctx context.Context, update types.Update, hash string) error {
	id, err := updateID(update)
	if err != nil {
		return err
	}
	rows, err := r.engine.Queries.UpsertSourcemapIndexPending(ctx, pgdb.UpsertSourcemapIndexPendingParams{
		UpdateID:   id,
		Hash:       hash,
		AppID:      repository.ToPgUUID(update.AppId),
		BranchName: update.Branch,
	})
	if err != nil {
		return fmt.Errorf("failed to record the pending sourcemap index in database: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("branch %q of app %s not found", update.Branch, update.AppId)
	}
	return nil
}

// MarkRunning marks the index job running and increments its attempt count.
// It returns errors for invalid numeric update ids, database failures, or a
// missing index record.
func (r *PostgresIndexRepository) MarkRunning(ctx context.Context, update types.Update) error {
	id, err := updateID(update)
	if err != nil {
		return err
	}
	rows, err := r.engine.Queries.SetSourcemapIndexRunning(ctx, pgdb.SetSourcemapIndexRunningParams{
		AppID:      repository.ToPgUUID(update.AppId),
		BranchName: update.Branch,
		UpdateID:   id,
	})
	if err != nil {
		return fmt.Errorf("failed to mark the sourcemap index running in database: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("no sourcemap index record for update %s on branch %s", update.UpdateId, update.Branch)
	}
	return nil
}

// Finish records how the job ended. reason is empty for a stored index;
// segments and indexSize (bytes) may be nil when unknown. Invalid numeric
// update ids, database failures, and missing index records return errors.
func (r *PostgresIndexRepository) Finish(ctx context.Context, update types.Update, status types.SourcemapIndexStatus, reason string, segments *int, indexSize *int64) error {
	id, err := updateID(update)
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

// GetUpdateSourcemap reads the map hash and optional index record. It returns
// nil, nil for a missing update, and a nil Hash for an update without a map.
// Invalid numeric update ids and database failures return errors.
func (r *PostgresIndexRepository) GetUpdateSourcemap(ctx context.Context, appId, branch, runtimeVersion, updateId string) (*UpdateSourcemap, error) {
	id, err := updateID(types.Update{UpdateId: updateId})
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
		Status:    types.SourcemapIndexStatus(row.IndexStatus),
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

// GetUpdateSourcemapByUUID reads the map for the UUID reported by a device,
// filling only Hash and Status on its index record. It returns nil, nil for
// a missing update and propagates database errors.
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
	sourcemap := &UpdateSourcemap{Hash: row.SourcemapHash}
	if row.IndexStatus != "" && row.SourcemapHash != nil {
		sourcemap.Index = &types.SourcemapIndex{Hash: *row.SourcemapHash, Status: types.SourcemapIndexStatus(row.IndexStatus)}
	}
	return sourcemap, nil
}
