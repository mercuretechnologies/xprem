package store

import (
	"context"
	"errors"
	"expo-open-ota/internal/crypto"
	"expo-open-ota/internal/database"
	"expo-open-ota/internal/database/postgres/pgdb"
	"expo-open-ota/internal/types"
	update2 "expo-open-ota/internal/update"
	"fmt"
	"strconv"
	"strings"
	"time"

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
		Platform:   update.Platform,
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

func (s *PostgresUpdateStore) GetLatestUpdate(ctx context.Context, appId string, branchName string, runtimeVersion string, platform string) (*types.Update, error) {
	pgAppID := ToPgUUID(appId)
	row, err := s.engine.Queries.GetLatestUpdate(ctx, pgdb.GetLatestUpdateParams{
		AppID:    pgAppID,
		Name:     branchName,
		Version:  runtimeVersion,
		Platform: platform,
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

// IsUpdateValid reports whether an update is complete. checked_at is the DB
// equivalent of the bucket backend's ".check" sentinel: the row is inserted when
// the upload URLs are handed out, and only stamped once the uploaded files have
// been verified, so a row without it is an upload that never finished — its
// bucket folder may be missing the bundle or assets the metadata references.
// Presence in the database is therefore not enough to call an update valid.
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
		// A missing row keeps the pre-guard behavior (silent success) so legacy
		// callers with an unknown id are unaffected.
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

func (s *PostgresUpdateStore) CreateUpdate(ctx context.Context, appId string, updateId int64, branchName string, runtimeVersion string, platform string, commitHash string, message string, publishGroup *string) (*types.Update, error) {
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
		Platform:     platform,
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
			Platform:   row.Platform,
			CommitHash: row.CommitHash,
		})
	}
	return members, nil
}

func (s *PostgresUpdateStore) GetUpdatesByRunTimeVersionAndBranchName(ctx context.Context, appId string, runtimeVersion string, branchName string) ([]types.UpdateItem, error) {
	pgAppID := ToPgUUID(appId)
	rows, err := s.engine.Queries.GetUpdatesByByBranchNameAndRuntimeVersion(ctx, pgdb.GetUpdatesByByBranchNameAndRuntimeVersionParams{
		ID:      pgAppID,
		Version: runtimeVersion,
		Name:    branchName,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve updates by runtime version and branch name from database: %w", err)
	}
	var updatesResponse []types.UpdateItem
	for _, row := range rows {
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
			return nil, fmt.Errorf("unknown update type %d for update ID %s", row.UpdateType, strconv.FormatInt(row.ID, 10))
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
			Platform:   row.Platform,
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
	return updatesResponse, nil
}

// escapeLikePattern neutralizes the ILIKE escape character in user-supplied
// search terms: a term ending in "\" makes Postgres reject the whole pattern
// ("LIKE pattern must not end with escape character"), surfacing as a 500.
// "%" and "_" stay live wildcards on purpose (search semantics).
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
		Platform:        query.Platform,
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
				Platform:   row.Platform,
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
		Platform:   metadata.Platform,
	}, nil
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

// GetLatestUpdateWithRollout returns the newest checked update for the platform along
// with its per-update rollout state and its resolved control (via the explicit
// control_update_id pointer). Both RolloutPercentage and Control stay nil for a plain
// update. Returns nil when the branch has no checked update for (rtv, platform).
func (s *PostgresUpdateStore) GetLatestUpdateWithRollout(ctx context.Context, appId string, branchName string, runtimeVersion string, platform string) (*types.UpdateWithRollout, error) {
	row, err := s.engine.Queries.GetLatestUpdateWithRollout(ctx, pgdb.GetLatestUpdateWithRolloutParams{
		AppID:    ToPgUUID(appId),
		Name:     branchName,
		Version:  runtimeVersion,
		Platform: platform,
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

// GetUpdateByUUID resolves a checked update by its persistent UUID, app-scoped. Returns
// nil when no checked update matches. Backs the /assets rollout fix.
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
func (s *PostgresUpdateStore) CreateUpdateWithRollout(ctx context.Context, appId string, updateId int64, branchName string, runtimeVersion string, platform string, commitHash string, message string, rolloutPercentage int, publishGroup *string) (*types.Update, error) {
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
		Platform:          platform,
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

func (s *PostgresUpdateStore) CreateRollback(ctx context.Context, appId string, updateId int64, branchName string, runtimeVersion string, platform string, commitHash string) (*types.Update, error) {
	pgAppID := ToPgUUID(appId)
	row, err := s.engine.InsertUpdate(ctx, pgdb.InsertUpdateParams{
		AppID:      pgAppID,
		ID:         updateId,
		Name:       branchName,
		Version:    runtimeVersion,
		UpdateType: int32(types.Rollback),
		Platform:   platform,
		CommitHash: commitHash,
		Message:    nil,
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

// GetUpdateOriginByUUID resolves both dimensions the observe flattener
// denormalizes onto every row: the branch of an update and the publish it came
// from. Both are permanent properties of the update, so one cached lookup
// covers the batch. An empty group is data, not an error: older CLIs and
// rollback markers have none.
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
