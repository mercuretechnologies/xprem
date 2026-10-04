// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package branchprotection

import (
	"context"
	"fmt"
	"xprem/internal/database"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/repository"
)

type PostgresRepository struct {
	engine *database.Engine
}

func NewPostgresRepository(engine *database.Engine) *PostgresRepository {
	return &PostgresRepository{engine: engine}
}

func (s *PostgresRepository) SetBranchProtection(ctx context.Context, appID string, branchName string, protected bool) error {
	updated, err := s.engine.Queries.SetBranchProtected(ctx, pgdb.SetBranchProtectedParams{
		Protected: protected,
		AppID:     repository.ToPgUUID(appID),
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
