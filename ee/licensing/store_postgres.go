// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package licensing

import (
	"context"
	"fmt"
	"xprem/internal/database"
)

type PostgresLicenseStore struct {
	engine *database.Engine
}

func NewPostgresLicenseStore(engine *database.Engine) *PostgresLicenseStore {
	return &PostgresLicenseStore{engine: engine}
}

func (s *PostgresLicenseStore) GetLicense(ctx context.Context) (*StoredLicense, error) {
	row, err := s.engine.Queries.GetEnterpriseLicense(ctx)
	if err != nil {
		if database.IsNoRows(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read enterprise license from database: %w", err)
	}
	return &StoredLicense{Key: row.LicenseKey, UpdatedAt: row.UpdatedAt.Time}, nil
}

func (s *PostgresLicenseStore) UpsertLicense(ctx context.Context, key string) (StoredLicense, error) {
	row, err := s.engine.Queries.UpsertEnterpriseLicense(ctx, key)
	if err != nil {
		return StoredLicense{}, fmt.Errorf("failed to store enterprise license in database: %w", err)
	}
	return StoredLicense{Key: row.LicenseKey, UpdatedAt: row.UpdatedAt.Time}, nil
}

func (s *PostgresLicenseStore) DeleteLicense(ctx context.Context) error {
	if err := s.engine.Queries.DeleteEnterpriseLicense(ctx); err != nil {
		return fmt.Errorf("failed to delete enterprise license from database: %w", err)
	}
	return nil
}
