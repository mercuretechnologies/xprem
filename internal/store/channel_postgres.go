package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"
	"xprem/internal/database"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/providers/expo"
	"xprem/internal/types"

	"github.com/jackc/pgx/v5"
)

type PostgresChannelStore struct {
	engine *database.Engine
}

func NewPostgresChannelStore(engine *database.Engine) *PostgresChannelStore {
	return &PostgresChannelStore{
		engine: engine,
	}
}

func (s *PostgresChannelStore) InsertChannel(ctx context.Context, appId string, branchId *int64, channelName string) (int64, error) {
	pgAppID := ToPgUUID(appId)
	insertedId, err := s.engine.Queries.InsertChannel(ctx, pgdb.InsertChannelParams{
		AppID:    pgAppID,
		Name:     channelName,
		BranchID: branchId,
	})
	if err != nil {
		if database.IsUniqueViolation(err) {
			return 0, &ErrResourceAlreadyExists{Resource: "channel", Identifier: fmt.Sprintf("%s (appId: %s)", channelName, appId)}
		}
		return 0, fmt.Errorf("failed to create channel in database: %w", err)
	}
	return insertedId, nil
}

func (s *PostgresChannelStore) DeleteChannel(ctx context.Context, channelName string, appId string) error {
	pgAppID := ToPgUUID(appId)
	commandTag, err := s.engine.Queries.DeleteChannelByName(ctx, pgdb.DeleteChannelByNameParams{
		AppID: pgAppID,
		Name:  channelName,
	})
	if err != nil {
		return fmt.Errorf("failed to delete channel from database: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		return &ErrResourceNotFound{Resource: "channel", Identifier: fmt.Sprintf("%s (appId: %s)", channelName, appId)}
	}
	return nil
}

func (s *PostgresChannelStore) GetChannelNameByBranchName(ctx context.Context, appId string, branchName string) ([]string, error) {
	pgAppID := ToPgUUID(appId)
	return s.engine.Queries.GetChannelNamesByBranchName(ctx, pgdb.GetChannelNamesByBranchNameParams{
		Name:  branchName,
		AppID: pgAppID,
	})
}

func (s *PostgresChannelStore) GetChannels(ctx context.Context, appId string) ([]types.ChannelMapping, error) {
	pgAppID := ToPgUUID(appId)
	appChannels, err := s.engine.Queries.GetChannelsByAppID(ctx, pgAppID)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve channels from database: %w", err)
	}
	channels := make([]types.ChannelMapping, len(appChannels))
	for i, channel := range appChannels {
		var branchIdPtr *string
		if channel.BranchID != nil {
			branchIdStr := strconv.FormatInt(*channel.BranchID, 10)
			branchIdPtr = &branchIdStr
		}
		var createdAtStr *string
		if channel.CreatedAt.Valid {
			timeStr := channel.CreatedAt.Time.Format(time.RFC3339)
			createdAtStr = &timeStr
		}
		mapping := types.ChannelMapping{
			ReleaseChannelName: channel.Name,
			ReleaseChannelId:   strconv.FormatInt(channel.ID, 10),
			BranchName:         channel.BranchName,
			BranchId:           branchIdPtr,
			CreatedAt:          createdAtStr,
			BranchCurrentUpdate: branchUpdateState(
				channel.BranchCurrentRuntimeVersion,
				channel.BranchCurrentCommitHash,
				channel.BranchCurrentUpdateCreatedAt,
				channel.BranchCurrentRolloutPercentage,
			),
			RolloutBranchCurrentUpdate: branchUpdateState(
				channel.RolloutBranchCurrentRuntimeVersion,
				channel.RolloutBranchCurrentCommitHash,
				channel.RolloutBranchCurrentUpdateCreatedAt,
				channel.RolloutBranchCurrentRolloutPercentage,
			),
		}
		if channel.RolloutID.Valid && channel.BranchName != nil && channel.RolloutBranchName != nil && channel.RolloutPercentage != nil {
			mapping.Rollout = &types.ChannelRollout{
				ID:                channel.RolloutID.String(),
				ChannelName:       channel.Name,
				DefaultBranchName: *channel.BranchName,
				RolloutBranchName: *channel.RolloutBranchName,
				Percentage:        int(*channel.RolloutPercentage),
				CreatedAt:         channel.RolloutCreatedAt.Time.Format(time.RFC3339),
				UpdatedAt:         channel.RolloutUpdatedAt.Time.Format(time.RFC3339),
			}
		}
		channels[i] = mapping
	}
	return channels, nil
}

func (s *PostgresChannelStore) GetUpdatesByRunTimeVersionAndBranchName(ctx context.Context, appId string, runtimeVersion string, branchName string) ([]pgdb.GetUpdatesByByBranchNameAndRuntimeVersionRow, error) {
	pgAppID := ToPgUUID(appId)
	return s.engine.Queries.GetUpdatesByByBranchNameAndRuntimeVersion(ctx, pgdb.GetUpdatesByByBranchNameAndRuntimeVersionParams{
		ID:      pgAppID,
		Version: runtimeVersion,
		Name:    branchName,
	})
}

func (s *PostgresChannelStore) GetChannelBranchMapping(ctx context.Context, appId string, channelName string) (*expo.ChannelMapping, error) {
	pgAppID := ToPgUUID(appId)
	mapping, err := s.engine.Queries.GetChannelBranchMapping(ctx, pgdb.GetChannelBranchMappingParams{
		AppID: pgAppID,
		Name:  channelName,
	})
	if err != nil {
		// An unknown channel, or one deliberately left unmapped (branch_id is
		// nilable, and the INNER JOIN on branches then yields no row), is a
		// 404 for the caller — not a server error. The bucket backend already
		// reports it as (nil, nil); match it so ResolveManifestBundle's
		// nil-check stays live in DB mode.
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to retrieve channel mapping from database: %w", err)
	}
	mappingStr := strconv.FormatInt(mapping.ID, 10)
	result := &expo.ChannelMapping{
		Id:         mappingStr,
		BranchName: mapping.BranchName,
	}
	if mapping.RolloutID.Valid && mapping.RolloutBranchName != nil && mapping.RolloutPercentage != nil {
		result.Rollout = &expo.ChannelRolloutInfo{
			ID:         mapping.RolloutID.String(),
			BranchName: *mapping.RolloutBranchName,
			Percentage: int(*mapping.RolloutPercentage),
		}
	}
	return result, nil
}
