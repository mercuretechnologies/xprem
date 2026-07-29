// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package apikeyrestrictions

import (
	"context"
	"fmt"
	"net/netip"
	"xprem/internal/database"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/store"
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

// GetRestrictionsByAppID returns one entry per API key that has at least one
// restriction set; keys in the default state (no protected access, no IP
// allowlist) are simply absent.
func (s *PostgresApiKeyRestrictionStore) GetRestrictionsByAppID(ctx context.Context, appID string) ([]ApiKeyRestrictions, error) {
	rows, err := s.engine.Queries.GetApiKeyRestrictionsByAppID(ctx, store.ToPgUUID(appID))
	if err != nil {
		return nil, fmt.Errorf("failed to read api key restrictions: %w", err)
	}
	result := make([]ApiKeyRestrictions, 0, len(rows))
	for _, row := range rows {
		if !row.CanAccessProtectedBranches && len(row.AllowedIps) == 0 {
			continue
		}
		result = append(result, ApiKeyRestrictions{
			ApiKeyID:                   row.ID,
			CanAccessProtectedBranches: row.CanAccessProtectedBranches,
			AllowedIps:                 row.AllowedIps,
		})
	}
	return result, nil
}

func (s *PostgresApiKeyRestrictionStore) SetRestrictions(ctx context.Context, appID string, apiKeyID int64, canAccessProtectedBranches bool, allowedIps []netip.Prefix) error {
	updated, err := s.engine.Queries.UpdateApiKeyRestrictions(ctx, pgdb.UpdateApiKeyRestrictionsParams{
		AllowedIps:                 allowedIps,
		CanAccessProtectedBranches: canAccessProtectedBranches,
		ID:                         apiKeyID,
		AppID:                      store.ToPgUUID(appID),
	})
	if err != nil {
		return fmt.Errorf("failed to update api key restrictions: %w", err)
	}
	if updated == 0 {
		return ErrApiKeyNotFound
	}
	return nil
}

// GetRestrictions is the enforcement read for one authenticated key. The key
// was validated against its app just before, so no app check is repeated here.
func (s *PostgresApiKeyRestrictionStore) GetRestrictions(ctx context.Context, apiKeyID int64) (ApiKeyRestrictions, error) {
	row, err := s.engine.Queries.GetApiKeyRestrictions(ctx, apiKeyID)
	if err != nil {
		return ApiKeyRestrictions{}, fmt.Errorf("failed to read api key restrictions: %w", err)
	}
	return ApiKeyRestrictions{
		ApiKeyID:                   apiKeyID,
		CanAccessProtectedBranches: row.CanAccessProtectedBranches,
		AllowedIps:                 row.AllowedIps,
	}, nil
}

func (s *PostgresApiKeyRestrictionStore) SetBranchProtection(ctx context.Context, appID string, branchName string, protected bool) error {
	updated, err := s.engine.Queries.SetBranchProtected(ctx, pgdb.SetBranchProtectedParams{
		Protected: protected,
		AppID:     store.ToPgUUID(appID),
		Name:      branchName,
	})
	if err != nil {
		return fmt.Errorf("failed to update branch protection: %w", err)
	}
	if updated == 0 {
		return ErrBranchNotFound
	}
	return nil
}

// IsBranchProtected answers false for a branch that does not exist yet: the
// CLI creates branches on first publish, and a brand-new branch cannot be
// protected before it exists.
func (s *PostgresApiKeyRestrictionStore) IsBranchProtected(ctx context.Context, appID string, branchName string) (bool, error) {
	protected, err := s.engine.Queries.IsBranchProtected(ctx, pgdb.IsBranchProtectedParams{
		AppID: store.ToPgUUID(appID),
		Name:  branchName,
	})
	if err != nil {
		if database.IsNoRows(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to read branch protection: %w", err)
	}
	return protected, nil
}
