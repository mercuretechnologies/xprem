package store

import (
	"context"
	"errors"
	"fmt"
	"time"
	"xprem/internal/database"
	"xprem/internal/database/postgres/pgdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// EnvVarRow is one env entry as listed in the dashboard: never the value.
type EnvVarRow struct {
	Key        string
	IsPublic   bool
	BranchName string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type PostgresEnvVarStore struct {
	engine *database.Engine
}

func NewPostgresEnvVarStore(engine *database.Engine) *PostgresEnvVarStore {
	return &PostgresEnvVarStore{
		engine: engine,
	}
}

func (s *PostgresEnvVarStore) UpsertEnvVar(ctx context.Context, appId string, branchId int64, key string, isPublic bool, sealedValue string) error {
	_, err := s.engine.Queries.UpsertBranchEnvVar(ctx, pgdb.UpsertBranchEnvVarParams{
		ID:          ToPgUUID(uuid.NewString()),
		AppID:       ToPgUUID(appId),
		BranchID:    branchId,
		Key:         key,
		IsPublic:    isPublic,
		SealedValue: sealedValue,
	})
	if err != nil {
		return fmt.Errorf("failed to save env var in database: %w", err)
	}
	return nil
}

func (s *PostgresEnvVarStore) ListEnvVars(ctx context.Context, appId string) ([]EnvVarRow, error) {
	rows, err := s.engine.Queries.ListEnvVarsByAppID(ctx, ToPgUUID(appId))
	if err != nil {
		return nil, fmt.Errorf("failed to list env vars from database: %w", err)
	}
	envVars := make([]EnvVarRow, len(rows))
	for i, row := range rows {
		envVars[i] = EnvVarRow{
			Key:        row.Key,
			IsPublic:   row.IsPublic,
			BranchName: row.BranchName,
			CreatedAt:  row.CreatedAt.Time,
			UpdatedAt:  row.UpdatedAt.Time,
		}
	}
	return envVars, nil
}

// GetSealedValue returns (nil, nil) when the entry does not exist on the branch.
func (s *PostgresEnvVarStore) GetSealedValue(ctx context.Context, appId string, branchId int64, key string) (*string, error) {
	sealedValue, err := s.engine.Queries.GetSealedEnvValue(ctx, pgdb.GetSealedEnvValueParams{
		AppID:    ToPgUUID(appId),
		BranchID: branchId,
		Key:      key,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to retrieve env var from database: %w", err)
	}
	return &sealedValue, nil
}

func (s *PostgresEnvVarStore) DeleteEnvVar(ctx context.Context, appId string, branchId int64, key string) error {
	commandTag, err := s.engine.Queries.DeleteEnvVar(ctx, pgdb.DeleteEnvVarParams{
		AppID:    ToPgUUID(appId),
		BranchID: branchId,
		Key:      key,
	})
	if err != nil {
		return fmt.Errorf("failed to delete env var from database: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		return &ErrResourceNotFound{Resource: "env var", Identifier: key}
	}
	return nil
}
