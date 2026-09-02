package store

import (
	"context"
	"fmt"
	"strconv"
	"time"
	"xprem/internal/database"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/types"
)

type PostgresBundlePatchStore struct {
	engine *database.Engine
}

func NewPostgresBundlePatchStore(engine *database.Engine) *PostgresBundlePatchStore {
	return &PostgresBundlePatchStore{engine: engine}
}

func parseUpdateID(name, value string) (int64, error) {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse %s: %w", name, err)
	}
	return id, nil
}

// MarkPending records a patch job about to be scheduled, resetting the pair
// if it was already handled.
func (s *PostgresBundlePatchStore) MarkPending(ctx context.Context, appId, branch, targetUpdateId, sourceUpdateId string) error {
	targetID, err := parseUpdateID("target update id", targetUpdateId)
	if err != nil {
		return err
	}
	sourceID, err := parseUpdateID("source update id", sourceUpdateId)
	if err != nil {
		return err
	}
	rows, err := s.engine.Queries.UpsertBundlePatchPending(ctx, pgdb.UpsertBundlePatchPendingParams{
		TargetUpdateID: targetID,
		SourceUpdateID: sourceID,
		AppID:          ToPgUUID(appId),
		BranchName:     branch,
	})
	if err != nil {
		return fmt.Errorf("failed to record the pending bundle patch in database: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("branch %q of app %s not found", branch, appId)
	}
	return nil
}

func (s *PostgresBundlePatchStore) MarkRunning(ctx context.Context, appId, branch, targetUpdateId, sourceUpdateId string) error {
	targetID, err := parseUpdateID("target update id", targetUpdateId)
	if err != nil {
		return err
	}
	sourceID, err := parseUpdateID("source update id", sourceUpdateId)
	if err != nil {
		return err
	}
	_, err = s.engine.Queries.SetBundlePatchRunning(ctx, pgdb.SetBundlePatchRunningParams{
		AppID:          ToPgUUID(appId),
		BranchName:     branch,
		TargetUpdateID: targetID,
		SourceUpdateID: sourceID,
	})
	if err != nil {
		return fmt.Errorf("failed to mark the bundle patch running in database: %w", err)
	}
	return nil
}

// Finish records how the job ended. reason is empty for a stored patch.
func (s *PostgresBundlePatchStore) Finish(ctx context.Context, appId, branch, targetUpdateId, sourceUpdateId string, status types.BundlePatchStatus, reason string, patchSize, fullDownloadSize *int64) error {
	targetID, err := parseUpdateID("target update id", targetUpdateId)
	if err != nil {
		return err
	}
	sourceID, err := parseUpdateID("source update id", sourceUpdateId)
	if err != nil {
		return err
	}
	var reasonPtr *string
	if reason != "" {
		reasonPtr = &reason
	}
	_, err = s.engine.Queries.FinishBundlePatch(ctx, pgdb.FinishBundlePatchParams{
		Status:           string(status),
		Reason:           reasonPtr,
		PatchSize:        patchSize,
		FullDownloadSize: fullDownloadSize,
		AppID:            ToPgUUID(appId),
		BranchName:       branch,
		TargetUpdateID:   targetID,
		SourceUpdateID:   sourceID,
	})
	if err != nil {
		return fmt.Errorf("failed to finish the bundle patch in database: %w", err)
	}
	return nil
}

// ListByTarget returns the patches toward one update, newest source first.
func (s *PostgresBundlePatchStore) ListByTarget(ctx context.Context, appId, branch, targetUpdateId string) ([]types.BundlePatch, error) {
	targetID, err := parseUpdateID("target update id", targetUpdateId)
	if err != nil {
		return nil, err
	}
	rows, err := s.engine.Queries.GetBundlePatchesByTarget(ctx, pgdb.GetBundlePatchesByTargetParams{
		AppID:          ToPgUUID(appId),
		BranchName:     branch,
		TargetUpdateID: targetID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list bundle patches from database: %w", err)
	}
	patches := make([]types.BundlePatch, 0, len(rows))
	for _, row := range rows {
		patch := types.BundlePatch{
			TargetUpdateId:   strconv.FormatInt(row.TargetUpdateID, 10),
			SourceUpdateId:   strconv.FormatInt(row.SourceUpdateID, 10),
			SourceCommitHash: row.SourceCommitHash,
			Status:           types.BundlePatchStatus(row.Status),
			PatchSize:        row.PatchSize,
			FullDownloadSize: row.FullDownloadSize,
			Attempts:         int(row.Attempts),
			UpdatedAt:        row.UpdatedAt.Time.UTC().Format(time.RFC3339),
			SourceCreatedAt:  row.SourceCreatedAt.Time.UTC().Format(time.RFC3339),
		}
		if row.TargetUpdateUuid.Valid {
			patch.TargetUpdateUUID = row.TargetUpdateUuid.String()
		}
		if row.SourceUpdateUuid.Valid {
			patch.SourceUpdateUUID = row.SourceUpdateUuid.String()
		}
		if row.SourceMessage != nil {
			patch.SourceMessage = *row.SourceMessage
		}
		if row.Reason != nil {
			patch.Reason = *row.Reason
		}
		patches = append(patches, patch)
	}
	return patches, nil
}
