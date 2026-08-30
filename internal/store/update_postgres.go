package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"xprem/internal/crypto"
	"xprem/internal/database"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/types"
	update2 "xprem/internal/update"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

type PostgresUpdateStore struct {
	engine *database.Engine
}

func NewPostgresUpdateStore(engine *database.Engine) *PostgresUpdateStore {
	return &PostgresUpdateStore{
		engine: engine,
	}
}

func (s *PostgresUpdateStore) GetUpdateDetails(ctx context.Context, appId string, branchName string, runtimeVersion string, updateId string) (types.UpdateDetails, error) {
	updateIdInt, err := strconv.ParseInt(updateId, 10, 64)
	if err != nil {
		return types.UpdateDetails{}, fmt.Errorf("failed to parse update ID: %w", err)
	}
	update, err := s.GetUpdateByBranchNameAndRuntime(ctx, appId, updateIdInt, branchName, runtimeVersion)
	if err != nil {
		return types.UpdateDetails{}, fmt.Errorf("failed to retrieve update by ID from database: %w", err)
	}
	expoConfig, err := update2.GetExpoConfig(types.Update{
		Branch:         update.BranchName,
		RuntimeVersion: update.RuntimeVersion,
		UpdateId:       strconv.FormatInt(update.ID, 10),
		CreatedAt:      time.Duration(update.CreatedAt.Time.UnixNano()),
		AppId:          appId,
	})
	if err != nil {
		return types.UpdateDetails{}, fmt.Errorf("failed to get expo config for update: %w", err)
	}
	messageStr := ""
	if update.Message != nil {
		messageStr = *update.Message
	}
	updateUUID := "Rollback to embedded"
	if update.UpdateType != int32(types.Rollback) {
		updateUUID = update.UpdateUuid.String()
	}
	details := types.UpdateDetails{
		UpdateUUID: updateUUID,
		UpdateId:   strconv.FormatInt(update.ID, 10),
		CreatedAt:  update.CreatedAt.Time.Format(time.RFC3339),
		CommitHash: update.CommitHash,
		Platform:   types.Platform(update.Platform),
		Message:    messageStr,
		Type:       types.UpdateType(update.UpdateType),
		ExpoConfig: string(expoConfig),
	}
	if update.RolloutPercentage != nil {
		pct := int(*update.RolloutPercentage)
		details.RolloutPercentage = &pct
	}
	if update.ControlUpdateID != nil {
		control := strconv.FormatInt(*update.ControlUpdateID, 10)
		details.ControlUpdateId = &control
	}
	return details, nil
}

func (s *PostgresUpdateStore) GetLatestUpdate(ctx context.Context, appId string, branchName string, runtimeVersion string, platform types.Platform) (*types.Update, error) {
	pgAppID := ToPgUUID(appId)
	row, err := s.engine.Queries.GetLatestUpdate(ctx, pgdb.GetLatestUpdateParams{
		AppID:    pgAppID,
		Name:     branchName,
		Version:  runtimeVersion,
		Platform: string(platform),
	})
	if err != nil {
		if database.IsNoRows(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to retrieve latest update from database: %w", err)
	}
	return &types.Update{
		UpdateId:       strconv.FormatInt(row.ID, 10),
		Branch:         branchName,
		RuntimeVersion: runtimeVersion,
		CreatedAt:      time.Duration(row.CreatedAt.Time.UnixNano()),
		AppId:          appId,
	}, nil
}

func (s *PostgresUpdateStore) GetUpdateType(ctx context.Context, update types.Update) (types.UpdateType, error) {
	updateIdInt, err := strconv.ParseInt(update.UpdateId, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse update ID: %w", err)
	}
	pgAppID := ToPgUUID(update.AppId)
	updateTypeInt, err := s.engine.Queries.GetUpdateType(ctx, pgdb.GetUpdateTypeParams{
		AppID: pgAppID,
		ID:    updateIdInt,
		Name:  update.Branch,
	})
	if err != nil {
		return 0, fmt.Errorf("failed to retrieve update type from database: %w", err)
	}
	return types.UpdateType(updateTypeInt), nil
}

// IsUpdateValid reports whether an update is complete, based on whether
// checked_at is set.
func (s *PostgresUpdateStore) IsUpdateValid(ctx context.Context, update types.Update) (bool, error) {
	updateIdInt, err := strconv.ParseInt(update.UpdateId, 10, 64)
	if err != nil {
		return false, fmt.Errorf("failed to parse update ID: %w", err)
	}
	checkedAt, err := s.engine.Queries.GetUpdateCheckedAt(ctx, pgdb.GetUpdateCheckedAtParams{
		AppID: ToPgUUID(update.AppId),
		Name:  update.Branch,
		ID:    updateIdInt,
	})
	if err != nil {
		if database.IsNoRows(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to retrieve update checked state from database: %w", err)
	}
	return checkedAt.Valid, nil
}

func (s *PostgresUpdateStore) MarkUpdateAsChecked(ctx context.Context, update types.Update) error {
	pgAppID := ToPgUUID(update.AppId)
	updateIdInt, err := strconv.ParseInt(update.UpdateId, 10, 64)
	if err != nil {
		return fmt.Errorf("failed to parse update ID: %w", err)
	}
	rows, err := s.engine.MarkUpdateAsChecked(ctx, pgdb.MarkUpdateAsCheckedParams{
		ID:    updateIdInt,
		AppID: pgAppID,
		Name:  update.Branch,
	})
	if err != nil {
		return fmt.Errorf("failed to mark update as checked in database: %w", err)
	}
	if rows == 0 {
		// The conditional stamp refused: disambiguate on the row's own rollout state.
		// A missing row keeps the pre-guard silent-success behavior.
		row, lookupErr := s.engine.GetUpdateByBranchNameAndRuntime(ctx, pgdb.GetUpdateByBranchNameAndRuntimeParams{
			AppID:   pgAppID,
			ID:      updateIdInt,
			Name:    update.Branch,
			Version: update.RuntimeVersion,
		})
		if lookupErr != nil {
			if database.IsNoRows(lookupErr) {
				return nil
			}
			return fmt.Errorf("failed to disambiguate refused update check: %w", lookupErr)
		}
		if row.RolloutPercentage != nil {
			return ErrRolloutSupersededByNewerUpdate
		}
		return ErrPublishBlockedByActiveRollout
	}
	return nil
}

func (s *PostgresUpdateStore) CreateUpdate(ctx context.Context, appId string, updateId int64, branchName string, runtimeVersion string, platform types.Platform, commitHash string, message string, publishGroup *string) (*types.Update, error) {
	messagePtr := &message
	if message == "" {
		messagePtr = (*string)(nil)
	}
	pgAppID := ToPgUUID(appId)
	row, err := s.engine.InsertUpdate(ctx, pgdb.InsertUpdateParams{
		AppID:        pgAppID,
		ID:           updateId,
		Name:         branchName,
		Version:      runtimeVersion,
		UpdateType:   int32(types.NormalUpdate),
		Platform:     string(platform),
		CommitHash:   commitHash,
		Message:      messagePtr,
		PublishGroup: ToPgUUIDPtr(publishGroup),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to insert update into database: %w", err)
	}
	return &types.Update{
		UpdateId:       strconv.FormatInt(row.ID, 10),
		Branch:         row.BranchName,
		RuntimeVersion: row.RuntimeVersion,
		CreatedAt:      time.Duration(row.CreatedAt.Time.UnixNano()),
		AppId:          appId,
	}, nil
}

func (s *PostgresUpdateStore) GetUpdate(ctx context.Context, appId string, branchName string, runtimeVersion string, updateId string) (*types.Update, error) {
	updateIdInt, err := strconv.ParseInt(updateId, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("failed to parse update ID: %w", err)
	}
	update, err := s.GetUpdateByBranchNameAndRuntime(ctx, appId, updateIdInt, branchName, runtimeVersion)
	if err != nil {
		if database.IsNoRows(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to retrieve update by ID from database: %w", err)
	}
	return &types.Update{
		UpdateId:       strconv.FormatInt(update.ID, 10),
		Branch:         update.BranchName,
		RuntimeVersion: update.RuntimeVersion,
		CreatedAt:      time.Duration(update.CreatedAt.Time.UnixNano()),
		AppId:          appId,
	}, nil
}

// GetCheckedUpdate answers in one query what GetUpdate + IsUpdateValid answer
// in two; it sits on the per-asset-download hot path.
func (s *PostgresUpdateStore) GetCheckedUpdate(ctx context.Context, appId string, branchName string, runtimeVersion string, updateId string) (*types.Update, error) {
	updateIdInt, err := strconv.ParseInt(updateId, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("failed to parse update ID: %w", err)
	}
	update, err := s.GetUpdateByBranchNameAndRuntime(ctx, appId, updateIdInt, branchName, runtimeVersion)
	if err != nil {
		if database.IsNoRows(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to retrieve update by ID from database: %w", err)
	}
	if !update.CheckedAt.Valid {
		return nil, nil
	}
	return &types.Update{
		UpdateId:       strconv.FormatInt(update.ID, 10),
		Branch:         update.BranchName,
		RuntimeVersion: update.RuntimeVersion,
		CreatedAt:      time.Duration(update.CreatedAt.Time.UnixNano()),
		AppId:          appId,
	}, nil
}

func (s *PostgresUpdateStore) GetUpdateByBranchNameAndRuntime(ctx context.Context, appId string, updateId int64, branchName string, runtimeVersion string) (pgdb.GetUpdateByBranchNameAndRuntimeRow, error) {
	return s.engine.Queries.GetUpdateByBranchNameAndRuntime(ctx, pgdb.GetUpdateByBranchNameAndRuntimeParams{
		AppID:   ToPgUUID(appId),
		ID:      updateId,
		Name:    branchName,
		Version: runtimeVersion,
	})
}

func (s *PostgresUpdateStore) GetUpdatesByPublishGroup(ctx context.Context, appId string, branchName string, runtimeVersion string, publishGroup string) ([]types.PublishGroupMember, error) {
	rows, err := s.engine.Queries.GetUpdatesByPublishGroup(ctx, pgdb.GetUpdatesByPublishGroupParams{
		AppID:        ToPgUUID(appId),
		Name:         branchName,
		Version:      runtimeVersion,
		PublishGroup: ToPgUUID(publishGroup),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve updates by publish group from database: %w", err)
	}
	members := make([]types.PublishGroupMember, 0, len(rows))
	for _, row := range rows {
		members = append(members, types.PublishGroupMember{
			UpdateId:   strconv.FormatInt(row.ID, 10),
			Platform:   types.Platform(row.Platform),
			CommitHash: row.CommitHash,
		})
	}
	return members, nil
}

func (s *PostgresUpdateStore) GetPublishGroupsPage(ctx context.Context, appId string, branchName string, runtimeVersion string, cursor *int64, limit int) (types.PublishGroupsPage, error) {
	rows, err := s.engine.Queries.GetPublishGroupsPage(ctx, pgdb.GetPublishGroupsPageParams{
		AppID:          ToPgUUID(appId),
		BranchName:     branchName,
		RuntimeVersion: runtimeVersion,
		BeforeID:       cursor,
		RowLimit:       int32(limit + 1),
	})
	if err != nil {
		return types.PublishGroupsPage{}, fmt.Errorf("failed to retrieve publish groups: %w", err)
	}

	type groupWithCursor struct {
		item     types.PublishGroupItem
		newestID int64
	}
	grouped := make([]groupWithCursor, 0, limit+1)
	groupIndexes := make(map[string]int, limit+1)
	for _, row := range rows {
		groupID := row.PublishGroup.String()
		index, ok := groupIndexes[groupID]
		if !ok {
			message := ""
			if row.Message != nil {
				message = *row.Message
			}
			index = len(grouped)
			groupIndexes[groupID] = index
			grouped = append(grouped, groupWithCursor{
				newestID: row.NewestID,
				item: types.PublishGroupItem{
					PublishGroup: groupID,
					CreatedAt:    row.CreatedAt.Time.Format(time.RFC3339),
					CommitHash:   row.CommitHash,
					Message:      message,
					Platforms:    make([]string, 0, 2),
					Updates:      make([]types.PublishGroupUpdateItem, 0, 2),
				},
			})
		}
		group := &grouped[index].item
		createdAt := row.CreatedAt.Time.Format(time.RFC3339)
		if createdAt > group.CreatedAt {
			group.CreatedAt = createdAt
		}
		if !slices.Contains(group.Platforms, row.Platform) {
			group.Platforms = append(group.Platforms, row.Platform)
		}
		group.Updates = append(group.Updates, types.PublishGroupUpdateItem{
			UpdateId:   strconv.FormatInt(row.ID, 10),
			CreatedAt:  createdAt,
			Platform:   types.Platform(row.Platform),
			CommitHash: row.CommitHash,
		})
	}

	hasMore := len(grouped) > limit
	if hasMore {
		grouped = grouped[:limit]
	}
	items := make([]types.PublishGroupItem, 0, len(grouped))
	for _, group := range grouped {
		items = append(items, group.item)
	}
	var nextCursor *string
	if hasMore {
		cursorValue := strconv.FormatInt(grouped[len(grouped)-1].newestID, 10)
		nextCursor = &cursorValue
	}
	return types.PublishGroupsPage{Items: items, NextCursor: nextCursor}, nil
}

func (s *PostgresUpdateStore) GetUpdatesByRunTimeVersionAndBranchName(ctx context.Context, appId string, runtimeVersion string, branchName string, cursor *int64, limit int) (types.UpdatesPage, error) {
	pgAppID := ToPgUUID(appId)
	rows, err := s.engine.Queries.GetUpdatesPageByBranchNameAndRuntimeVersion(ctx, pgdb.GetUpdatesPageByBranchNameAndRuntimeVersionParams{
		AppID:          pgAppID,
		RuntimeVersion: runtimeVersion,
		BranchName:     branchName,
		BeforeID:       cursor,
		RowLimit:       int32(limit + 1),
	})
	if err != nil {
		return types.UpdatesPage{}, fmt.Errorf("failed to retrieve updates by runtime version and branch name from database: %w", err)
	}
	hasMore := len(rows) > limit
	pageRows := rows
	if hasMore {
		pageRows = rows[:limit]
	}
	updatesResponse := make([]types.UpdateItem, 0, len(pageRows))
	for _, row := range pageRows {
		createdAtStr := row.CreatedAt.Time.Format(time.RFC3339)
		updateUUID := ""
		switch row.UpdateType {
		case int32(types.Rollback):
			updateUUID = "Rollback to embedded"
		case int32(types.NormalUpdate):
			if row.UpdateUuid.Valid && row.UpdateUuid.String() != "" {
				updateUUID = row.UpdateUuid.String()
			} else {
				metadata, err := update2.GetMetadata(types.Update{
					Branch:         branchName,
					RuntimeVersion: runtimeVersion,
					UpdateId:       strconv.FormatInt(row.ID, 10),
					CreatedAt:      time.Duration(row.CreatedAt.Time.UnixNano()),
					AppId:          appId,
				})
				// A phantom row (files gone from storage) must stay listed,
				// or it could not be deleted from the dashboard.
				if err != nil && !errors.Is(err, update2.ErrUpdateMetadataMissing) {
					continue
				}
				updateUUID = crypto.ConvertSHA256HashToUUID(metadata.ID)
			}
		default:
			return types.UpdatesPage{}, fmt.Errorf("unknown update type %d for update ID %s", row.UpdateType, strconv.FormatInt(row.ID, 10))
		}
		messageStr := ""
		if row.Message != nil {
			messageStr = *row.Message
		}
		item := types.UpdateItem{
			UpdateUUID: updateUUID,
			UpdateId:   strconv.FormatInt(row.ID, 10),
			CreatedAt:  createdAtStr,
			CommitHash: row.CommitHash,
			Message:    messageStr,
			Platform:   types.Platform(row.Platform),
		}
		if row.RolloutPercentage != nil {
			pct := int(*row.RolloutPercentage)
			item.RolloutPercentage = &pct
		}
		if row.ControlUpdateID != nil {
			control := strconv.FormatInt(*row.ControlUpdateID, 10)
			item.ControlUpdateId = &control
		}
		if row.PublishGroup.Valid {
			group := row.PublishGroup.String()
			item.PublishGroup = &group
		}
		updatesResponse = append(updatesResponse, item)
	}
	var nextCursor *string
	if hasMore {
		// Cursor progression follows the raw SQL page rather than the successfully
		// converted items. A corrupt metadata row must not hide later rows.
		cursorValue := strconv.FormatInt(pageRows[len(pageRows)-1].ID, 10)
		nextCursor = &cursorValue
	}
	return types.UpdatesPage{Items: updatesResponse, NextCursor: nextCursor}, nil
}

// escapeLikePattern neutralizes the ILIKE escape character in user-supplied
// search terms; "%" and "_" stay live wildcards on purpose.
func escapeLikePattern(term string) string {
	return strings.ReplaceAll(term, `\`, `\\`)
}

func (s *PostgresUpdateStore) GetUpdateFeed(ctx context.Context, appId string, query types.UpdateFeedQuery) ([]types.UpdateFeedItem, error) {
	from := pgtype.Timestamptz{}
	if query.From != nil {
		from = pgtype.Timestamptz{Time: *query.From, Valid: true}
	}
	to := pgtype.Timestamptz{}
	if query.To != nil {
		to = pgtype.Timestamptz{Time: *query.To, Valid: true}
	}
	cursorCreatedAt := pgtype.Timestamptz{}
	if query.CursorCreatedAt != nil {
		cursorCreatedAt = pgtype.Timestamptz{Time: *query.CursorCreatedAt, Valid: true}
	}
	rows, err := s.engine.Queries.GetUpdateFeed(ctx, pgdb.GetUpdateFeedParams{
		AppID:           ToPgUUID(appId),
		Branch:          query.Branch,
		RuntimeVersion:  query.RuntimeVersion,
		Platform:        string(query.Platform),
		UpdateUuid:      escapeLikePattern(query.UpdateUUID),
		PublishGroup:    escapeLikePattern(query.PublishGroup),
		CommitHash:      escapeLikePattern(query.CommitHash),
		CreatedFrom:     from,
		CreatedTo:       to,
		HasCursor:       query.CursorCreatedAt != nil,
		CursorCreatedAt: cursorCreatedAt,
		CursorBranchID:  query.CursorBranchID,
		CursorUpdateID:  query.CursorUpdateID,
		RowLimit:        int32(query.Limit),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve update feed from database: %w", err)
	}
	items := make([]types.UpdateFeedItem, 0, len(rows))
	for _, row := range rows {
		updateUUID := "Rollback to embedded"
		if row.UpdateType == int32(types.NormalUpdate) {
			if row.UpdateUuid.Valid {
				updateUUID = row.UpdateUuid.String()
			}
		} else if row.UpdateType != int32(types.Rollback) {
			return nil, fmt.Errorf("unknown update type %d for update ID %d", row.UpdateType, row.ID)
		}

		item := types.UpdateFeedItem{
			UpdateItem: types.UpdateItem{
				UpdateUUID: updateUUID,
				UpdateId:   strconv.FormatInt(row.ID, 10),
				CreatedAt:  row.CreatedAt.Time.Format(time.RFC3339),
				CommitHash: row.CommitHash,
				Platform:   types.Platform(row.Platform),
			},
			Branch:         row.BranchName,
			RuntimeVersion: row.RuntimeVersion,
			BranchID:       row.BranchID,
			FeedCreatedAt:  row.CreatedAt.Time,
		}
		if row.Message != nil {
			item.Message = *row.Message
		}
		if row.RolloutPercentage != nil {
			percentage := int(*row.RolloutPercentage)
			item.RolloutPercentage = &percentage
		}
		if row.ControlUpdateID != nil {
			controlID := strconv.FormatInt(*row.ControlUpdateID, 10)
			item.ControlUpdateId = &controlID
		}
		if row.PublishGroup.Valid {
			group := row.PublishGroup.String()
			item.PublishGroup = &group
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *PostgresUpdateStore) RetrieveUpdateStoredMetadata(ctx context.Context, update types.Update) (*types.UpdateStoredMetadata, error) {
	updateIdInt, _ := strconv.ParseInt(update.UpdateId, 10, 64)
	pgAppID := ToPgUUID(update.AppId)
	metadata, err := s.engine.Queries.GetUpdateMetadata(ctx, pgdb.GetUpdateMetadataParams{
		ID:    updateIdInt,
		Name:  update.Branch,
		AppID: pgAppID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve update metadata from database: %w", err)
	}
	messageStr := ""
	if metadata.Message != nil {
		messageStr = *metadata.Message
	}
	return &types.UpdateStoredMetadata{
		UpdateUUID: metadata.UpdateUuid.String(),
		CommitHash: metadata.CommitHash,
		Message:    messageStr,
		Platform:   types.Platform(metadata.Platform),
	}, nil
}

func (s *PostgresUpdateStore) GetUpdateAssetMapping(ctx context.Context, update types.Update) (*types.UpdateAssetMapping, error) {
	updateIdInt, err := strconv.ParseInt(update.UpdateId, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("failed to parse update ID: %w", err)
	}
	mapping, err := s.engine.Queries.GetUpdateAssetMapping(ctx, pgdb.GetUpdateAssetMappingParams{
		ID:    updateIdInt,
		AppID: ToPgUUID(update.AppId),
		Name:  update.Branch,
	})
	if err != nil {
		if database.IsNoRows(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to retrieve update asset mapping from database: %w", err)
	}
	return mapping, nil
}

func (s *PostgresUpdateStore) StoreUpdateAssetMapping(ctx context.Context, update types.Update, mapping *types.UpdateAssetMapping) error {
	updateIdInt, err := strconv.ParseInt(update.UpdateId, 10, 64)
	if err != nil {
		return fmt.Errorf("failed to parse update ID: %w", err)
	}
	commandTag, err := s.engine.Queries.SetUpdateAssetMapping(ctx, pgdb.SetUpdateAssetMappingParams{
		ID:           updateIdInt,
		AssetMapping: mapping,
		AppID:        ToPgUUID(update.AppId),
		Name:         update.Branch,
	})
	if err != nil {
		return fmt.Errorf("failed to store update asset mapping in database: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		return fmt.Errorf("no rows were updated when storing the asset mapping for update ID %s", update.UpdateId)
	}
	return nil
}

func (s *PostgresUpdateStore) StoreUpdateUUIDInMetadata(ctx context.Context, update types.Update, updateUUID string) error {
	updateIdInt, _ := strconv.ParseInt(update.UpdateId, 10, 64)
	var uuidToStore pgtype.UUID
	if err := uuidToStore.Scan(updateUUID); err != nil {
		return fmt.Errorf("failed to parse update UUID: %w", err)
	}
	pgAppID := ToPgUUID(update.AppId)
	commandTag, err := s.engine.Queries.StoreUpdateUUID(ctx, pgdb.StoreUpdateUUIDParams{
		ID:         updateIdInt,
		UpdateUuid: uuidToStore,
		AppID:      pgAppID,
		Name:       update.Branch,
	})
	if err != nil {
		return fmt.Errorf("failed to store update UUID in database: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		return fmt.Errorf("no rows were updated when trying to store update UUID in database for update ID %s", update.UpdateId)
	}
	return nil
}

// GetLatestUpdateWithRollout returns the newest checked update for the platform
// along with its rollout state and resolved control, or nil if none exists.
func (s *PostgresUpdateStore) GetLatestUpdateWithRollout(ctx context.Context, appId string, branchName string, runtimeVersion string, platform types.Platform) (*types.UpdateWithRollout, error) {
	row, err := s.engine.Queries.GetLatestUpdateWithRollout(ctx, pgdb.GetLatestUpdateWithRolloutParams{
		AppID:    ToPgUUID(appId),
		Name:     branchName,
		Version:  runtimeVersion,
		Platform: string(platform),
	})
	if err != nil {
		if database.IsNoRows(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to retrieve latest update with rollout from database: %w", err)
	}
	result := &types.UpdateWithRollout{
		Update: types.Update{
			UpdateId:       strconv.FormatInt(row.ID, 10),
			Branch:         branchName,
			RuntimeVersion: runtimeVersion,
			CreatedAt:      time.Duration(row.CreatedAt.Time.UnixNano()),
			AppId:          appId,
		},
	}
	// Legacy rows without a stored UUID leave the field empty; the poll
	// short-circuit then falls back to the composed manifest's id.
	if row.UpdateUuid.Valid {
		result.Update.UpdateUUID = row.UpdateUuid.String()
	}
	if row.RolloutPercentage != nil {
		pct := int(*row.RolloutPercentage)
		result.RolloutPercentage = &pct
	}
	if row.ControlID != nil {
		result.Control = &types.Update{
			UpdateId:       strconv.FormatInt(*row.ControlID, 10),
			Branch:         branchName,
			RuntimeVersion: runtimeVersion,
			CreatedAt:      time.Duration(row.ControlCreatedAt.Time.UnixNano()),
			AppId:          appId,
		}
		if row.ControlUpdateUuid.Valid {
			result.Control.UpdateUUID = row.ControlUpdateUuid.String()
		}
	}
	return result, nil
}

// HasActiveRolloutUpdate reports whether (branch, rtv) already has an active per-update
// rollout on any platform. Used as the fail-fast publish guard.
func (s *PostgresUpdateStore) HasActiveRolloutUpdate(ctx context.Context, appId string, branchName string, runtimeVersion string) (bool, error) {
	return s.engine.Queries.HasActiveRolloutUpdate(ctx, pgdb.HasActiveRolloutUpdateParams{
		AppID:   ToPgUUID(appId),
		Name:    branchName,
		Version: runtimeVersion,
	})
}

// GetUpdateByUUID resolves a checked update by its persistent UUID, app-scoped.
// Returns nil when no checked update matches.
func (s *PostgresUpdateStore) GetUpdateByUUID(ctx context.Context, appId string, updateUUID string) (*types.Update, error) {
	var pgUUID pgtype.UUID
	if err := pgUUID.Scan(updateUUID); err != nil {
		return nil, fmt.Errorf("failed to parse update UUID: %w", err)
	}
	row, err := s.engine.Queries.GetUpdateByUUID(ctx, pgdb.GetUpdateByUUIDParams{
		AppID:      ToPgUUID(appId),
		UpdateUuid: pgUUID,
	})
	if err != nil {
		if database.IsNoRows(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to retrieve update by UUID from database: %w", err)
	}
	return &types.Update{
		UpdateId:       strconv.FormatInt(row.ID, 10),
		Branch:         row.BranchName,
		RuntimeVersion: row.RuntimeVersion,
		CreatedAt:      time.Duration(row.CreatedAt.Time.UnixNano()),
		AppId:          appId,
	}, nil
}

// CreateUpdateWithRollout inserts a normal update carrying a rollout percentage. The
// control (previous checked update of the same branch/rtv/platform) is resolved inside
// the same statement and may be NULL for the first update of a branch.
func (s *PostgresUpdateStore) CreateUpdateWithRollout(ctx context.Context, appId string, updateId int64, branchName string, runtimeVersion string, platform types.Platform, commitHash string, message string, rolloutPercentage int, publishGroup *string) (*types.Update, error) {
	messagePtr := &message
	if message == "" {
		messagePtr = (*string)(nil)
	}
	pct := int32(rolloutPercentage)
	pgAppID := ToPgUUID(appId)
	row, err := s.engine.InsertUpdateWithRollout(ctx, pgdb.InsertUpdateWithRolloutParams{
		AppID:             pgAppID,
		ID:                updateId,
		Name:              branchName,
		Version:           runtimeVersion,
		UpdateType:        int32(types.NormalUpdate),
		Platform:          string(platform),
		CommitHash:        commitHash,
		Message:           messagePtr,
		RolloutPercentage: &pct,
		PublishGroup:      ToPgUUIDPtr(publishGroup),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to insert update with rollout into database: %w", err)
	}
	return &types.Update{
		UpdateId:       strconv.FormatInt(row.ID, 10),
		Branch:         row.BranchName,
		RuntimeVersion: row.RuntimeVersion,
		CreatedAt:      time.Duration(row.CreatedAt.Time.UnixNano()),
		AppId:          appId,
	}, nil
}

func (s *PostgresUpdateStore) CreateRollback(ctx context.Context, appId string, updateId int64, branchName string, runtimeVersion string, platform types.Platform, commitHash string, message string) (*types.Update, error) {
	pgAppID := ToPgUUID(appId)
	var messageParam *string
	if message != "" {
		messageParam = &message
	}
	row, err := s.engine.InsertUpdate(ctx, pgdb.InsertUpdateParams{
		AppID:      pgAppID,
		ID:         updateId,
		Name:       branchName,
		Version:    runtimeVersion,
		UpdateType: int32(types.Rollback),
		Platform:   string(platform),
		CommitHash: commitHash,
		Message:    messageParam,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to insert rollback update into database: %w", err)
	}
	return &types.Update{
		UpdateId:       strconv.FormatInt(row.ID, 10),
		Branch:         row.BranchName,
		RuntimeVersion: row.RuntimeVersion,
		CreatedAt:      time.Duration(row.CreatedAt.Time.UnixNano()),
		AppId:          pgAppID.String(),
	}, nil
}

// GetUpdateOriginByUUID resolves the branch and publish group an update belongs to.
// An empty group is not an error: older CLIs and rollback markers have none.
func (s *PostgresUpdateStore) GetUpdateOriginByUUID(ctx context.Context, appID string, updateUUID string) (string, string, error) {
	row, err := s.engine.GetUpdateOriginByUUID(ctx, pgdb.GetUpdateOriginByUUIDParams{
		AppID:      ToPgUUID(appID),
		UpdateUuid: ToPgUUID(updateUUID),
	})
	if err != nil {
		if database.IsNoRows(err) {
			return "", "", nil
		}
		return "", "", err
	}
	group := ""
	if row.PublishGroup.Valid {
		group = uuid.UUID(row.PublishGroup.Bytes).String()
	}
	return row.BranchName, group, nil
}
