package store

import (
	"context"
	"expo-open-ota/internal/database"
	"expo-open-ota/internal/database/postgres/pgdb"
	"fmt"
)

type InsertOAuthClientParameters struct {
	ID           string
	Name         string
	RedirectURIs []string
}

type PostgresOAuthClientStore struct {
	engine *database.Engine
}

func NewPostgresOAuthClientStore(engine *database.Engine) *PostgresOAuthClientStore {
	return &PostgresOAuthClientStore{
		engine: engine,
	}
}

func (s *PostgresOAuthClientStore) InsertOAuthClient(ctx context.Context, params InsertOAuthClientParameters) error {
	if err := s.engine.Queries.InsertOAuthClient(ctx, pgdb.InsertOAuthClientParams{
		ID:           ToPgUUID(params.ID),
		Name:         params.Name,
		RedirectUris: params.RedirectURIs,
	}); err != nil {
		return fmt.Errorf("failed to insert oauth client into database: %w", err)
	}
	return nil
}
