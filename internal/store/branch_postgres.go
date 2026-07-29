package store

import (
	"context"
	"expo-open-ota/internal/database"
	"expo-open-ota/internal/database/postgres/pgdb"
	"expo-open-ota/internal/types"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

type PostgresBranchStore struct {
	engine *database.Engine
}

func NewPostgresBranchStore(engine *database.Engine) *PostgresBranchStore {
	return &PostgresBranchStore{
		engine: engine,
	}
}

func (s *PostgresBranchStore) InsertBranch(ctx context.Context, branch pgdb.InsertBranchParams) (int64, error) {
	insertedId, err := s.engine.Queries.InsertBranch(ctx, branch)
	if err != nil {
		if database.IsUniqueViolation(err) {
			return 0, &ErrResourceAlreadyExists{Resource: "branch", Identifier: branch.Name}
		}
		return 0, err
	}
	return insertedId, nil
}

func (s *PostgresBranchStore) GetUpdatedMetadataByBranchName(ctx context.Context, appId string, branchName string) ([]pgdb.GetUpdatesMetadataByBranchNameRow, error) {
	pgAppID := ToPgUUID(appId)
	return s.engine.Queries.GetUpdatesMetadataByBranchName(ctx, pgdb.GetUpdatesMetadataByBranchNameParams{
		Name:  branchName,
		AppID: pgAppID,
	})
}

func (s *PostgresBranchStore) DeleteBranchByName(ctx context.Context, appId string, branchName string) error {
	pgAppID := ToPgUUID(appId)
	commandTag, err := s.engine.Queries.DeleteBranchByName(ctx, pgdb.DeleteBranchByNameParams{
		Name:  branchName,
		AppID: pgAppID,
	})
	if err != nil {
		return err
	}
	if commandTag.RowsAffected() == 0 {
		// The DELETE refuses protected branches inside the statement, so a miss
		// is either that refusal or a branch that does not exist. The follow-up
		// read picks the right error, and an infrastructure failure on that read
		// must surface as such, not masquerade as a missing branch.
		protected, protErr := s.engine.Queries.IsBranchProtected(ctx, pgdb.IsBranchProtectedParams{
			AppID: pgAppID,
			Name:  branchName,
		})
		if protErr != nil {
			if database.IsNoRows(protErr) {
				return &ErrResourceNotFound{Resource: "branch", Identifier: fmt.Sprintf("name: %s, appId: %s", branchName, appId)}
			}
			return fmt.Errorf("failed to check branch protection after a refused delete: %w", protErr)
		}
		if protected {
			return &ErrBranchProtected{BranchName: branchName}
		}
		return &ErrResourceNotFound{Resource: "branch", Identifier: fmt.Sprintf("name: %s, appId: %s", branchName, appId)}
	}
	return nil
}

func (s *PostgresBranchStore) GetBranches(ctx context.Context, appId string) ([]types.BranchMapping, error) {
	pgAppID := ToPgUUID(appId)
	appBranches, err := s.engine.Queries.GetBranchesByAppID(ctx, pgAppID)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve branches from database: %w", err)
	}
	branches := make([]types.BranchMapping, len(appBranches))
	for i, branch := range appBranches {
		var createdAtStr *string
		if branch.CreatedAt.Valid {
			timeStr := branch.CreatedAt.Time.Format(time.RFC3339)
			createdAtStr = &timeStr
		}
		branchIdStr := strconv.FormatInt(branch.ID, 10)
		branches[i] = types.BranchMapping{
			BranchName:     branch.Name,
			BranchId:       &branchIdStr,
			ReleaseChannel: branch.ChannelName,
			CreatedAt:      createdAtStr,
			Protected:      branch.Protected,
			CurrentUpdate: branchUpdateState(
				branch.CurrentRuntimeVersion,
				branch.CurrentCommitHash,
				branch.CurrentUpdateCreatedAt,
				branch.CurrentRolloutPercentage,
			),
		}
	}
	return branches, nil
}

func branchUpdateState(runtimeVersion *string, commitHash *string, createdAt pgtype.Timestamptz, rolloutPercentage *int32) *types.BranchUpdateState {
	if runtimeVersion == nil || commitHash == nil || !createdAt.Valid {
		return nil
	}
	var percentage *int
	if rolloutPercentage != nil {
		value := int(*rolloutPercentage)
		percentage = &value
	}
	return &types.BranchUpdateState{
		RuntimeVersion:    *runtimeVersion,
		CommitHash:        *commitHash,
		CreatedAt:         createdAt.Time.Format(time.RFC3339),
		RolloutPercentage: percentage,
	}
}

func (s *PostgresBranchStore) UpsertBranchAndRuntimeVersion(ctx context.Context, appId string, branchName string, runtimeVersion string) error {
	pgAppID := ToPgUUID(appId)
	_, err := s.InsertBranch(ctx, pgdb.InsertBranchParams{
		AppID: pgAppID,
		Name:  branchName,
	})
	if err != nil {
		if _, ok := err.(*ErrResourceAlreadyExists); ok {
			err = nil
		} else {
			return fmt.Errorf("failed to upsert branch: %w", err)
		}
	}
	_, err = s.CreateRuntimeVersion(ctx, appId, runtimeVersion)
	if err != nil {
		if _, ok := err.(*ErrResourceAlreadyExists); ok {
			err = nil
		} else {
			return fmt.Errorf("failed to upsert runtime version: %w", err)
		}
	}
	return err
}

func (s *PostgresBranchStore) GetRuntimeVersionsWithUpdateStats(ctx context.Context, appId string, branchName string) ([]types.RuntimeVersionWithStats, error) {
	pgAppID := ToPgUUID(appId)
	rows, err := s.engine.Queries.GetRuntimeVersionsWithUpdateCount(ctx, pgdb.GetRuntimeVersionsWithUpdateCountParams{
		AppID: pgAppID,
		Name:  branchName,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve runtime versions with update stats from database: %w", err)
	}
	runtimeVersions := make([]types.RuntimeVersionWithStats, len(rows))
	for i, row := range rows {
		var lastUpdatedAtStr string
		if row.UpdatedAt.Valid {
			lastUpdatedAtStr = row.UpdatedAt.Time.Format(time.RFC3339)
		} else {
			lastUpdatedAtStr = ""
		}
		var createdAtStr string
		if row.CreatedAt.Valid {
			createdAtStr = row.CreatedAt.Time.Format(time.RFC3339)
		} else {
			createdAtStr = ""
		}
		runtimeVersion := types.RuntimeVersionWithStats{
			RuntimeVersion:  row.Version,
			LastUpdatedAt:   lastUpdatedAtStr,
			CreatedAt:       createdAtStr,
			NumberOfUpdates: int(row.UpdateCount),
		}
		// sqlc cannot type an aggregate subquery, so RolloutPercentage comes
		// back as interface{}: pgx yields int32 for a value, nil for SQL NULL.
		if pct, ok := row.RolloutPercentage.(int32); ok {
			percentage := int(pct)
			runtimeVersion.ActiveRollout = true
			runtimeVersion.RolloutPercentage = &percentage
		}
		runtimeVersions[i] = runtimeVersion
	}
	return runtimeVersions, nil
}

func (s *PostgresBranchStore) UpdateChannelBranchMapping(ctx context.Context, appId string, channelId string, branchId string) error {
	channelIdInt, err := strconv.ParseInt(channelId, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid channel ID: %w", err)
	}
	branchIdInt, err := strconv.ParseInt(branchId, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid branch ID: %w", err)
	}
	branchIdIntPtr := &branchIdInt
	pgAppID := ToPgUUID(appId)
	commandTag, err := s.engine.Queries.UpdateChannelBranchMapping(ctx, pgdb.UpdateChannelBranchMappingParams{
		AppID:    pgAppID,
		BranchID: branchIdIntPtr,
		ID:       channelIdInt,
	})
	if err != nil {
		if database.IsForeignKeyViolation(err) {
			return &ErrResourceNotFound{Resource: "branch", Identifier: fmt.Sprintf("%s (appId: %s)", branchId, appId)}
		}
		return fmt.Errorf("failed to update channel-branch mapping in database: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		return &ErrResourceNotFound{Resource: "channel", Identifier: fmt.Sprintf("%s (appId: %s)", channelId, appId)}
	}
	return nil
}

func (s *PostgresBranchStore) CreateRuntimeVersion(ctx context.Context, appId string, version string) (int64, error) {
	pgAppID := ToPgUUID(appId)
	_, err := s.engine.Queries.InsertRuntimeVersion(ctx, pgdb.InsertRuntimeVersionParams{
		AppID:   pgAppID,
		Version: version,
	})
	if err != nil {
		if database.IsUniqueViolation(err) {
			return 0, &ErrResourceAlreadyExists{Resource: "runtime version", Identifier: fmt.Sprintf("%s (appId: %s)", version, appId)}
		}
		return 0, fmt.Errorf("failed to create runtime version in database: %w", err)
	}
	return 0, nil
}

func (s *PostgresBranchStore) GetBranchByName(ctx context.Context, appId string, branchName string) (int64, error) {
	pgAppID := ToPgUUID(appId)
	return s.engine.Queries.GetBranchByName(ctx, pgdb.GetBranchByNameParams{
		Name:  branchName,
		AppID: pgAppID,
	})
}
