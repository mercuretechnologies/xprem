// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package apikeyrestrictions

import (
	"context"
	"errors"
	"fmt"
	"xprem/internal/database"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/repository"
	"xprem/internal/types"
	"xprem/internal/validation"

	"github.com/jackc/pgx/v5"
)

type PostgresApiKeyAccessRepository struct {
	engine *database.Engine
}

func NewPostgresApiKeyAccessRepository(engine *database.Engine) *PostgresApiKeyAccessRepository {
	return &PostgresApiKeyAccessRepository{engine: engine}
}

func (s *PostgresApiKeyAccessRepository) GetApiKeyName(ctx context.Context, appID string, apiKeyID int64) (string, error) {
	return s.engine.Queries.GetApiKeyNameByID(ctx, pgdb.GetApiKeyNameByIDParams{
		ID:    apiKeyID,
		AppID: repository.ToPgUUID(appID),
	})
}

// GetAccess reads the live key and all permission domains in one database
// snapshot, checking the key and its native targets still belong to this app.
// Zero rows means the key is gone; a key with no rules still yields one row.
func (s *PostgresApiKeyAccessRepository) GetAccess(ctx context.Context, appID string, apiKeyID int64) (ApiKeyAccess, error) {
	rows, err := s.engine.Queries.GetApiKeyAccess(ctx, pgdb.GetApiKeyAccessParams{
		ApiKeyID: apiKeyID,
		AppID:    repository.ToPgUUID(appID),
	})
	if err != nil {
		return ApiKeyAccess{}, fmt.Errorf("failed to read api key access: %w", err)
	}
	if len(rows) == 0 {
		return ApiKeyAccess{}, ErrApiKeyNotFound
	}
	access := ApiKeyAccess{ApiKeyID: apiKeyID, AllowedIps: rows[0].AllowedIps}
	for _, row := range rows {
		switch row.Domain {
		case "updates":
			access.UpdateRules = append(access.UpdateRules, UpdateRule{
				Pattern: row.Pattern,
				Actions: toUpdateActions(row.Actions),
			})
		case "build":
			access.BuildRules = append(access.BuildRules, BuildRule{
				AppIdentifierID: row.AppIdentifierID.String(),
				Actions:         toBuildActions(row.Actions),
			})
		case "submit":
			access.SubmitRules = append(access.SubmitRules, SubmitRule{
				AppIdentifierID: row.AppIdentifierID.String(),
				Destination:     SubmitDestination(row.Destination),
				Actions:         toSubmitActions(row.Actions),
			})
		case "environments":
			access.EnvironmentRules = append(access.EnvironmentRules, EnvironmentRule{Pattern: row.Pattern})
		}
	}
	return access, nil
}

// GetAccessByAppID returns the access of every live key of one app, including
// keys still at their default.
func (s *PostgresApiKeyAccessRepository) GetAccessByAppID(ctx context.Context, appID string) ([]ApiKeyAccess, error) {
	rows, err := s.engine.Queries.GetApiKeyAccessByAppID(ctx, repository.ToPgUUID(appID))
	if err != nil {
		return nil, fmt.Errorf("failed to read api key access: %w", err)
	}
	result := foldAccessRows(rows)
	byID := make(map[int64]*ApiKeyAccess, len(result))
	for i := range result {
		byID[result[i].ApiKeyID] = &result[i]
	}
	buildRows, err := s.engine.Queries.GetApiKeyBuildRulesByAppID(ctx, repository.ToPgUUID(appID))
	if err != nil {
		return nil, err
	}
	for _, row := range buildRows {
		if access := byID[row.ApiKeyID]; access != nil {
			access.BuildRules = append(access.BuildRules, BuildRule{AppIdentifierID: row.AppIdentifierID.String(), Actions: toBuildActions(row.Actions)})
		}
	}
	submitRows, err := s.engine.Queries.GetApiKeySubmitRulesByAppID(ctx, repository.ToPgUUID(appID))
	if err != nil {
		return nil, err
	}
	for _, row := range submitRows {
		if access := byID[row.ApiKeyID]; access != nil {
			access.SubmitRules = append(access.SubmitRules, SubmitRule{AppIdentifierID: row.AppIdentifierID.String(), Destination: SubmitDestination(row.Destination), Actions: toSubmitActions(row.Actions)})
		}
	}
	environmentRows, err := s.engine.Queries.GetApiKeyEnvironmentRulesByAppID(ctx, repository.ToPgUUID(appID))
	if err != nil {
		return nil, err
	}
	for _, row := range environmentRows {
		if access := byID[row.ApiKeyID]; access != nil {
			access.EnvironmentRules = append(access.EnvironmentRules, EnvironmentRule{Pattern: row.Pattern})
		}
	}
	return result, nil
}

// foldAccessRows turns the LEFT JOIN's one-row-per-rule shape back into one
// entry per key. It relies on the query's ORDER BY k.id, so rows of one key
// are contiguous.
func foldAccessRows(rows []pgdb.GetApiKeyAccessByAppIDRow) []ApiKeyAccess {
	result := make([]ApiKeyAccess, 0, len(rows))
	for _, row := range rows {
		if len(result) == 0 || result[len(result)-1].ApiKeyID != row.ID {
			result = append(result, ApiKeyAccess{ApiKeyID: row.ID, AllowedIps: row.AllowedIps})
		}
		if row.Pattern == nil {
			continue
		}
		// Re-derived after each append: holding this pointer across appends would
		// dangle if the slice reallocates.
		current := &result[len(result)-1]
		current.UpdateRules = append(current.UpdateRules, UpdateRule{
			Pattern: *row.Pattern,
			Actions: toUpdateActions(row.Actions),
		})
	}
	return result
}

// SetAccess replaces one key's whole access, the IP allow-list and the rule
// list, in one transaction; rules are deleted then re-inserted rather than
// diffed.
func (s *PostgresApiKeyAccessRepository) SetAccess(ctx context.Context, appID string, access ApiKeyAccess) error {
	return s.engine.WithTx(ctx, func(q *pgdb.Queries) error {
		updated, err := q.UpdateApiKeyAccess(ctx, pgdb.UpdateApiKeyAccessParams{
			AllowedIps: access.AllowedIps,
			ID:         access.ApiKeyID,
			AppID:      repository.ToPgUUID(appID),
		})
		if err != nil {
			return fmt.Errorf("failed to update api key access: %w", err)
		}
		// Checked before rules are touched, so a key in another app or revoked
		// cannot end up with rules.
		if updated == 0 {
			return ErrApiKeyNotFound
		}
		if err := q.DeleteApiKeyUpdateRules(ctx, access.ApiKeyID); err != nil {
			return fmt.Errorf("failed to clear api key update rules: %w", err)
		}
		for _, rule := range access.UpdateRules {
			if err := q.InsertApiKeyUpdateRule(ctx, pgdb.InsertApiKeyUpdateRuleParams{
				ApiKeyID: access.ApiKeyID,
				Pattern:  rule.Pattern,
				Actions:  fromUpdateActions(rule.Actions),
			}); err != nil {
				return fmt.Errorf("failed to insert api key update rule: %w", err)
			}
		}
		if err := q.DeleteApiKeyBuildRules(ctx, access.ApiKeyID); err != nil {
			return err
		}
		if err := q.DeleteApiKeySubmitRules(ctx, access.ApiKeyID); err != nil {
			return err
		}
		for _, rule := range access.BuildRules {
			if _, err := identifierPlatform(ctx, q, appID, rule.AppIdentifierID); err != nil {
				return err
			}
			if err := q.InsertApiKeyBuildRule(ctx, pgdb.InsertApiKeyBuildRuleParams{
				ApiKeyID: access.ApiKeyID, AppID: repository.ToPgUUID(appID), AppIdentifierID: repository.ToPgUUID(rule.AppIdentifierID), Actions: fromBuildActions(rule.Actions),
			}); err != nil {
				return err
			}
		}
		for _, rule := range access.SubmitRules {
			platform, err := identifierPlatform(ctx, q, appID, rule.AppIdentifierID)
			if err != nil {
				return err
			}
			if rule.Destination.platform() != platform {
				return validation.Errorf("submit.destination", "destination does not match the app identifier platform")
			}
			if err := q.InsertApiKeySubmitRule(ctx, pgdb.InsertApiKeySubmitRuleParams{
				ApiKeyID: access.ApiKeyID, AppID: repository.ToPgUUID(appID), AppIdentifierID: repository.ToPgUUID(rule.AppIdentifierID), Destination: string(rule.Destination), Actions: fromSubmitActions(rule.Actions),
			}); err != nil {
				return err
			}
		}
		if err := q.DeleteApiKeyEnvironmentRules(ctx, access.ApiKeyID); err != nil {
			return err
		}
		for _, rule := range access.EnvironmentRules {
			if err := q.InsertApiKeyEnvironmentRule(ctx, pgdb.InsertApiKeyEnvironmentRuleParams{ApiKeyID: access.ApiKeyID, Pattern: rule.Pattern}); err != nil {
				return err
			}
		}
		return nil
	})
}

// toUpdateActions drops any value that is not a known action, which can only come
// from a hand-written row.
func toUpdateActions(raw []string) []UpdateAction {
	actions := make([]UpdateAction, 0, len(raw))
	for _, value := range raw {
		if IsValidUpdateAction(value) {
			actions = append(actions, UpdateAction(value))
		}
	}
	return actions
}

func fromUpdateActions(actions []UpdateAction) []string {
	raw := make([]string, 0, len(actions))
	for _, action := range actions {
		raw = append(raw, string(action))
	}
	return raw
}

// Unknown stored actions cannot become a grant.
func toBuildActions(raw []string) []BuildAction {
	var actions []BuildAction
	for _, value := range raw {
		switch action := BuildAction(value); action {
		case BuildActionCreate:
			actions = append(actions, action)
		}
	}
	return actions
}

func fromBuildActions(actions []BuildAction) []string {
	raw := make([]string, 0, len(actions))
	for _, action := range actions {
		raw = append(raw, string(action))
	}
	return raw
}

// Membership is checked in the same transaction as the policy replacement.
func identifierPlatform(ctx context.Context, q *pgdb.Queries, appID, identifierID string) (types.Platform, error) {
	row, err := q.GetAppIdentifierByID(ctx, pgdb.GetAppIdentifierByIDParams{AppID: repository.ToPgUUID(appID), ID: repository.ToPgUUID(identifierID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", validation.Errorf("appIdentifierId", "app identifier is not registered in this app")
	}
	if err != nil {
		return "", err
	}
	return row.Platform, nil
}

func toSubmitActions(raw []string) []SubmitAction {
	actions := make([]SubmitAction, 0, len(raw))
	for _, value := range raw {
		switch action := SubmitAction(value); action {
		case SubmitActionUpload:
			actions = append(actions, action)
		}
	}
	return actions
}
func fromSubmitActions(actions []SubmitAction) []string {
	raw := make([]string, 0, len(actions))
	for _, action := range actions {
		raw = append(raw, string(action))
	}
	return raw
}
