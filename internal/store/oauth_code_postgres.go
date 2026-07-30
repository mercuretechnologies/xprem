package store

import (
	"context"
	"fmt"
	"time"
	"xprem/internal/database"
	"xprem/internal/database/postgres/pgdb"

	"github.com/jackc/pgx/v5/pgtype"
)

// OAuthAuthorizationCode is the consent context a consumed code was frozen
// with; the token exchange verifies the request against it.
type OAuthAuthorizationCode struct {
	ID            string
	ClientID      string
	UserID        string
	RedirectURI   string
	CodeChallenge string
	Scope         string
	ExpiresAt     time.Time
}

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

// ConsumeOAuthAuthorizationCode claims a code, atomically and exactly once.
// ErrResourceNotFound means it was not claimable: unknown, expired, or
// already exchanged.
func (s *PostgresOAuthCodeStore) ConsumeOAuthAuthorizationCode(ctx context.Context, id string) (OAuthAuthorizationCode, error) {
	row, err := s.engine.Queries.ConsumeOAuthAuthorizationCode(ctx, ToPgUUID(id))
	if err != nil {
		if database.IsNoRows(err) {
			return OAuthAuthorizationCode{}, &ErrResourceNotFound{Resource: "oauth authorization code", Identifier: id}
		}
		return OAuthAuthorizationCode{}, fmt.Errorf("failed to consume oauth authorization code in database: %w", err)
	}
	return OAuthAuthorizationCode{
		ID:            row.ID.String(),
		ClientID:      row.ClientID.String(),
		UserID:        row.UserID.String(),
		RedirectURI:   row.RedirectUri,
		CodeChallenge: row.CodeChallenge,
		Scope:         row.Scope,
		ExpiresAt:     row.ExpiresAt.Time,
	}, nil
}

// DeleteExpiredOAuthAuthorizationCodes purges dead codes; called inline when a
// new one is minted, which keeps the table bounded without a background job.
func (s *PostgresOAuthCodeStore) DeleteExpiredOAuthAuthorizationCodes(ctx context.Context) error {
	if err := s.engine.Queries.DeleteExpiredOAuthAuthorizationCodes(ctx); err != nil {
		return fmt.Errorf("failed to delete expired oauth authorization codes from database: %w", err)
	}
	return nil
}
