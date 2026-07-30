package store

import (
	"context"
	"expo-open-ota/internal/database"
	"expo-open-ota/internal/database/postgres/pgdb"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

type InsertOAuthAuthorizationCodeParameters struct {
	ID            string
	ClientID      string
	UserID        string
	RedirectURI   string
	CodeChallenge string
	Scope         string
	ExpiresAt     time.Time
}

type PostgresOAuthCodeStore struct {
	engine *database.Engine
}

func NewPostgresOAuthCodeStore(engine *database.Engine) *PostgresOAuthCodeStore {
	return &PostgresOAuthCodeStore{
		engine: engine,
	}
}

func (s *PostgresOAuthCodeStore) InsertOAuthAuthorizationCode(ctx context.Context, params InsertOAuthAuthorizationCodeParameters) error {
	if err := s.engine.Queries.InsertOAuthAuthorizationCode(ctx, pgdb.InsertOAuthAuthorizationCodeParams{
		ID:            ToPgUUID(params.ID),
		ClientID:      ToPgUUID(params.ClientID),
		UserID:        ToPgUUID(params.UserID),
		RedirectUri:   params.RedirectURI,
		CodeChallenge: params.CodeChallenge,
		Scope:         params.Scope,
		ExpiresAt:     pgtype.Timestamptz{Time: params.ExpiresAt.UTC(), Valid: true},
	}); err != nil {
		return fmt.Errorf("failed to insert oauth authorization code into database: %w", err)
	}
	return nil
}

// DeleteExpiredOAuthAuthorizationCodes purges dead codes; called inline when a
// new one is minted, which keeps the table bounded without a background job.
func (s *PostgresOAuthCodeStore) DeleteExpiredOAuthAuthorizationCodes(ctx context.Context) error {
	if err := s.engine.Queries.DeleteExpiredOAuthAuthorizationCodes(ctx); err != nil {
		return fmt.Errorf("failed to delete expired oauth authorization codes from database: %w", err)
	}
	return nil
}
