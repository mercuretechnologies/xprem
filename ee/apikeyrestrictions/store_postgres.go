// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package apikeyrestrictions

import (
	"context"
	"expo-open-ota/internal/database"
	"expo-open-ota/internal/database/postgres/pgdb"
	"expo-open-ota/internal/store"
	"fmt"
)

type PostgresApiKeyRestrictionStore struct {
	engine *database.Engine
}

func NewPostgresApiKeyRestrictionStore(engine *database.Engine) *PostgresApiKeyRestrictionStore {
	return &PostgresApiKeyRestrictionStore{engine: engine}
}

func (s *PostgresApiKeyRestrictionStore) GetApiKeyName(ctx context.Context, appID string, apiKeyID int64) (string, error) {
	return s.engine.Queries.GetApiKeyNameByID(ctx, pgdb.GetApiKeyNameByIDParams{
		ID:    apiKeyID,
		AppID: store.ToPgUUID(appID),
	})
}

// GetAccess is the enforcement read for one authenticated key; no app check
// is repeated here since the key was already validated against its app.
// Zero rows means the key is gone; a key with no rule still yields one row.
func (s *PostgresApiKeyRestrictionStore) GetAccess(ctx context.Context, apiKeyID int64) (ApiKeyAccess, error) {
	rows, err := s.engine.Queries.GetApiKeyAccess(ctx, apiKeyID)
	if err != nil {
		return ApiKeyAccess{}, fmt.Errorf("failed to read api key access: %w", err)
	}
	if len(rows) == 0 {
		return ApiKeyAccess{}, ErrApiKeyNotFound
	}
	access := ApiKeyAccess{ApiKeyID: apiKeyID, AllowedIps: rows[0].AllowedIps}
	for _, row := range rows {
		if row.Pattern == nil {
			continue
		}
		access.BranchRules = append(access.BranchRules, BranchRule{
			Pattern: *row.Pattern,
			Actions: toActions(row.Actions),
		})
	}
	return access, nil
}

// GetAccessByAppID returns the access of every live key of one app, including
// keys still at their default.
func (s *PostgresApiKeyRestrictionStore) GetAccessByAppID(ctx context.Context, appID string) ([]ApiKeyAccess, error) {
	rows, err := s.engine.Queries.GetApiKeyAccessByAppID(ctx, store.ToPgUUID(appID))
	if err != nil {
		return nil, fmt.Errorf("failed to read api key access: %w", err)
	}
	return foldAccessRows(rows), nil
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
		current.BranchRules = append(current.BranchRules, BranchRule{
			Pattern: *row.Pattern,
			Actions: toActions(row.Actions),
		})
	}
	return result
}

// SetAccess replaces one key's whole access, the IP allow-list and the rule
// list, in one transaction; rules are deleted then re-inserted rather than
// diffed.
func (s *PostgresApiKeyRestrictionStore) SetAccess(ctx context.Context, appID string, access ApiKeyAccess) error {
	return s.engine.WithTx(ctx, func(q *pgdb.Queries) error {
		updated, err := q.UpdateApiKeyAccess(ctx, pgdb.UpdateApiKeyAccessParams{
			AllowedIps: access.AllowedIps,
			ID:         access.ApiKeyID,
			AppID:      store.ToPgUUID(appID),
		})
		if err != nil {
			return fmt.Errorf("failed to update api key access: %w", err)
		}
		// Checked before rules are touched, so a key in another app or revoked
		// cannot end up with rules.
		if updated == 0 {
			return ErrApiKeyNotFound
		}
		if err := q.DeleteApiKeyBranchRules(ctx, access.ApiKeyID); err != nil {
			return fmt.Errorf("failed to clear api key branch rules: %w", err)
		}
		for _, rule := range access.BranchRules {
			if err := q.InsertApiKeyBranchRule(ctx, pgdb.InsertApiKeyBranchRuleParams{
				ApiKeyID: access.ApiKeyID,
				Pattern:  rule.Pattern,
				Actions:  fromActions(rule.Actions),
			}); err != nil {
				return fmt.Errorf("failed to insert api key branch rule: %w", err)
			}
		}
		return nil
	})
}

// toActions drops any value that is not a known action, which can only come
// from a hand-written row.
func toActions(raw []string) []Action {
	actions := make([]Action, 0, len(raw))
	for _, value := range raw {
		if IsValidAction(value) {
			actions = append(actions, Action(value))
		}
	}
	return actions
}

func fromActions(actions []Action) []string {
	raw := make([]string, 0, len(actions))
	for _, action := range actions {
		raw = append(raw, string(action))
	}
	return raw
}
