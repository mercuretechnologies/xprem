package store

import (
	"context"
	"fmt"
	"xprem/internal/database"
	"xprem/internal/database/postgres/pgdb"
)

// OAuthClient is a dynamically registered OAuth client: a public client
// pinned to the redirect URIs it declared at registration.
type OAuthClient struct {
	Id           string
	Name         string
	RedirectURIs []string
}

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

func (s *PostgresOAuthClientStore) GetOAuthClient(ctx context.Context, id string) (OAuthClient, error) {
	row, err := s.engine.Queries.GetOAuthClient(ctx, ToPgUUID(id))
	if err != nil {
		if database.IsNoRows(err) {
			return OAuthClient{}, &ErrResourceNotFound{Resource: "oauth client", Identifier: id}
		}
		return OAuthClient{}, fmt.Errorf("failed to retrieve oauth client from database: %w", err)
	}
	return OAuthClient{
		Id:           row.ID.String(),
		Name:         row.Name,
		RedirectURIs: row.RedirectUris,
	}, nil
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
