// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package branchprotection

import (
	"context"
	"expo-open-ota/internal/database"
	"expo-open-ota/internal/database/postgres/pgdb"
	"expo-open-ota/internal/store"
	"fmt"
)

type PostgresStore struct {
	engine *database.Engine
}

func NewPostgresStore(engine *database.Engine) *PostgresStore {
	return &PostgresStore{engine: engine}
}

func (s *PostgresStore) SetBranchProtection(ctx context.Context, appID string, branchName string, protected bool) error {
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
