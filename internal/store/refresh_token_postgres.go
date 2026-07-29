package store

import (
	"context"
	"expo-open-ota/internal/database"
	"expo-open-ota/internal/database/postgres/pgdb"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// RefreshToken is one link in a sign-in's refresh chain, identified by the
// jti claim in the dashboard's refresh JWT.
type RefreshToken struct {
	Id     string
	UserId string
	// FamilyId groups every token descended from one sign-in, so a leaked
	// chain can be revoked without touching the account's other devices.
	FamilyId  string
	ExpiresAt time.Time
	// Nil while this is the family's live token, stamped when it is rotated.
	UsedAt *time.Time
	// UsedRecently reports whether UsedAt falls inside the replay grace passed to
	// GetRefreshToken; always false from the other reads, which do not ask.
	UsedRecently bool
	// ReplacedBy names the successor this token was rotated into, nil while it
	// is still live.
	ReplacedBy *string
}

type InsertRefreshTokenParameters struct {
	ID        string
	UserID    string
	FamilyID  string
	ExpiresAt time.Time
}

// RotateRefreshTokenParameters names the token being retired and the one
// replacing it.
type RotateRefreshTokenParameters struct {
	OldID     string
	NewID     string
	ExpiresAt time.Time
}

type PostgresRefreshTokenStore struct {
	engine *database.Engine
}

func NewPostgresRefreshTokenStore(engine *database.Engine) *PostgresRefreshTokenStore {
	return &PostgresRefreshTokenStore{
		engine: engine,
	}
}

func (s *PostgresRefreshTokenStore) InsertRefreshToken(ctx context.Context, params InsertRefreshTokenParameters) error {
	if err := s.engine.Queries.InsertRefreshToken(ctx, pgdb.InsertRefreshTokenParams{
		ID:        ToPgUUID(params.ID),
		UserID:    ToPgUUID(params.UserID),
		FamilyID:  ToPgUUID(params.FamilyID),
		ExpiresAt: pgtype.Timestamptz{Time: params.ExpiresAt.UTC(), Valid: true},
	}); err != nil {
		return fmt.Errorf("failed to insert refresh token into database: %w", err)
	}
	return nil
}

// RotateRefreshToken retires params.OldID and issues params.NewID in the same
// family, in one transaction, and returns the row it retired.
// ErrResourceNotFound means the old token was not claimable: unknown, expired, or already rotated.
func (s *PostgresRefreshTokenStore) RotateRefreshToken(ctx context.Context, params RotateRefreshTokenParameters) (RefreshToken, error) {
	var rotated RefreshToken
	err := s.engine.WithTx(ctx, func(q *pgdb.Queries) error {
		row, err := q.ConsumeRefreshToken(ctx, pgdb.ConsumeRefreshTokenParams{
			ID:         ToPgUUID(params.OldID),
			ReplacedBy: ToPgUUID(params.NewID),
		})
		if err != nil {
			if database.IsNoRows(err) {
				return &ErrResourceNotFound{Resource: "refresh token", Identifier: params.OldID}
			}
			return fmt.Errorf("failed to consume refresh token in database: %w", err)
		}
		rotated = refreshTokenFromRow(row)
		if err := q.InsertRefreshToken(ctx, pgdb.InsertRefreshTokenParams{
			ID:        ToPgUUID(params.NewID),
			UserID:    row.UserID,
			FamilyID:  row.FamilyID,
			ExpiresAt: pgtype.Timestamptz{Time: params.ExpiresAt.UTC(), Valid: true},
		}); err != nil {
			return fmt.Errorf("failed to insert the rotated refresh token into database: %w", err)
		}
		return nil
	})
	if err != nil {
		return RefreshToken{}, err
	}
	return rotated, nil
}

// GetRefreshToken explains why a token could not be claimed. replayGrace sets
// how recent a rotation still counts as the same client asking twice.
func (s *PostgresRefreshTokenStore) GetRefreshToken(ctx context.Context, id string, replayGrace time.Duration) (RefreshToken, error) {
	row, err := s.engine.Queries.GetRefreshToken(ctx, pgdb.GetRefreshTokenParams{
		ID:          ToPgUUID(id),
		ReplayGrace: pgtype.Interval{Microseconds: replayGrace.Microseconds(), Valid: true},
	})
	if err != nil {
		if database.IsNoRows(err) {
			return RefreshToken{}, &ErrResourceNotFound{Resource: "refresh token", Identifier: id}
		}
		return RefreshToken{}, fmt.Errorf("failed to retrieve refresh token from database: %w", err)
	}
	token := refreshTokenFromRow(pgdb.RefreshToken{
		ID:         row.ID,
		UserID:     row.UserID,
		FamilyID:   row.FamilyID,
		CreatedAt:  row.CreatedAt,
		ExpiresAt:  row.ExpiresAt,
		UsedAt:     row.UsedAt,
		ReplacedBy: row.ReplacedBy,
	})
	// UsedRecently is nullable to sqlc; NULL means not used, i.e. false.
	token.UsedRecently = row.UsedRecently != nil && *row.UsedRecently
	return token, nil
}

// DeleteRefreshTokenFamily revokes a whole sign-in chain; used when a replayed
// token is detected.
func (s *PostgresRefreshTokenStore) DeleteRefreshTokenFamily(ctx context.Context, familyId string) error {
	if err := s.engine.Queries.DeleteRefreshTokenFamily(ctx, ToPgUUID(familyId)); err != nil {
		return fmt.Errorf("failed to delete refresh token family from database: %w", err)
	}
	return nil
}

func (s *PostgresRefreshTokenStore) DeleteExpiredRefreshTokens(ctx context.Context, userId string) error {
	if err := s.engine.Queries.DeleteExpiredRefreshTokensForUser(ctx, ToPgUUID(userId)); err != nil {
		return fmt.Errorf("failed to delete expired refresh tokens from database: %w", err)
	}
	return nil
}

func refreshTokenFromRow(row pgdb.RefreshToken) RefreshToken {
	token := RefreshToken{
		Id:        row.ID.String(),
		UserId:    row.UserID.String(),
		FamilyId:  row.FamilyID.String(),
		ExpiresAt: row.ExpiresAt.Time,
		UsedAt:    timestamptzToPtr(row.UsedAt),
	}
	if row.ReplacedBy.Valid {
		successor := row.ReplacedBy.String()
		token.ReplacedBy = &successor
	}
	return token
}
