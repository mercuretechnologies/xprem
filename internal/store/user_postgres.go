package store

import (
	"context"
	"expo-open-ota/internal/database"
	"expo-open-ota/internal/database/postgres/pgdb"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// User is a dashboard user account. PasswordHash is only populated by the
// lookups the auth flows need (by email, by id) — never by listings — and must
// never leave the service layer.
type User struct {
	Id           string
	Email        string
	PasswordHash string
	IsAdmin      bool
	// Enabled gates every sign-in path. Accounts are enabled on creation; an
	// admin can revoke access without deleting the account, and SSO manual
	// validation provisions new accounts disabled until an admin approves them.
	Enabled   bool
	CreatedAt time.Time
	// Nil until the account's first successful sign-in.
	LastConnectedAt *time.Time
	// SessionVersion is the account's security generation. Every dashboard JWT
	// carries the value it was minted with, and a mismatch means the session
	// was revoked (the account was disabled, demoted or changed its password
	// since). Only the lookups that authenticate populate it. GetUsers does
	// not: a listing has no session to check.
	SessionVersion int32
}

type InsertUserParameters struct {
	ID           string
	Email        string
	PasswordHash string
	IsAdmin      bool
	Enabled      bool
}

// NormalizeEmail is the single place an email is canonicalized before hitting
// the users table. Rows are always stored in this form, which is what lets the
// plain UNIQUE(email) constraint enforce case-insensitive uniqueness.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

type PostgresUserStore struct {
	engine *database.Engine
}

func NewPostgresUserStore(engine *database.Engine) *PostgresUserStore {
	return &PostgresUserStore{
		engine: engine,
	}
}

func (s *PostgresUserStore) InsertUser(ctx context.Context, params InsertUserParameters) (User, error) {
	email := NormalizeEmail(params.Email)
	row, err := s.engine.Queries.InsertUser(ctx, pgdb.InsertUserParams{
		ID:           ToPgUUID(params.ID),
		Email:        email,
		PasswordHash: params.PasswordHash,
		IsAdmin:      params.IsAdmin,
		Enabled:      params.Enabled,
	})
	if err != nil {
		if database.IsUniqueViolation(err) {
			return User{}, &ErrResourceAlreadyExists{Resource: "user", Identifier: email}
		}
		return User{}, fmt.Errorf("failed to insert user into database: %w", err)
	}
	return User{
		Id:        row.ID.String(),
		Email:     row.Email,
		IsAdmin:   row.IsAdmin,
		Enabled:   row.Enabled,
		CreatedAt: row.CreatedAt.Time,
	}, nil
}

func (s *PostgresUserStore) GetUserByEmail(ctx context.Context, email string) (User, error) {
	normalizedEmail := NormalizeEmail(email)
	row, err := s.engine.Queries.GetUserByEmail(ctx, normalizedEmail)
	if err != nil {
		if database.IsNoRows(err) {
			return User{}, &ErrResourceNotFound{Resource: "user", Identifier: normalizedEmail}
		}
		return User{}, fmt.Errorf("failed to retrieve user from database: %w", err)
	}
	return userFromRow(row), nil
}

func (s *PostgresUserStore) GetUserByID(ctx context.Context, id string) (User, error) {
	row, err := s.engine.Queries.GetUserByID(ctx, ToPgUUID(id))
	if err != nil {
		if database.IsNoRows(err) {
			return User{}, &ErrResourceNotFound{Resource: "user", Identifier: id}
		}
		return User{}, fmt.Errorf("failed to retrieve user from database: %w", err)
	}
	return userFromRow(row), nil
}

func (s *PostgresUserStore) GetUsers(ctx context.Context) ([]User, error) {
	rows, err := s.engine.Queries.GetUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve users from database: %w", err)
	}
	users := make([]User, len(rows))
	for i, row := range rows {
		users[i] = User{
			Id:              row.ID.String(),
			Email:           row.Email,
			IsAdmin:         row.IsAdmin,
			Enabled:         row.Enabled,
			CreatedAt:       row.CreatedAt.Time,
			LastConnectedAt: timestamptzToPtr(row.LastConnectedAt),
		}
	}
	return users, nil
}

func (s *PostgresUserStore) DeleteUserByID(ctx context.Context, id string) error {
	commandTag, err := s.engine.Queries.DeleteUserByID(ctx, ToPgUUID(id))
	if err != nil {
		return fmt.Errorf("failed to delete user from database: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		// The guarded query matches no row both for a missing user and for the
		// last remaining admin — look the row up to tell the two apart.
		if _, lookupErr := s.GetUserByID(ctx, id); lookupErr != nil {
			return lookupErr
		}
		return ErrWouldLeaveNoAdmin
	}
	return nil
}

func (s *PostgresUserStore) UpdateUserPassword(ctx context.Context, id string, passwordHash string) error {
	commandTag, err := s.engine.Queries.UpdateUserPasswordByID(ctx, pgdb.UpdateUserPasswordByIDParams{
		ID:           ToPgUUID(id),
		PasswordHash: passwordHash,
	})
	if err != nil {
		return fmt.Errorf("failed to update user password in database: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		return &ErrResourceNotFound{Resource: "user", Identifier: id}
	}
	return nil
}

func (s *PostgresUserStore) UpdateUserIsAdmin(ctx context.Context, id string, isAdmin bool) error {
	commandTag, err := s.engine.Queries.UpdateUserIsAdminByID(ctx, pgdb.UpdateUserIsAdminByIDParams{
		ID:      ToPgUUID(id),
		IsAdmin: isAdmin,
	})
	if err != nil {
		return fmt.Errorf("failed to update user admin flag in database: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		// The guarded query matches no row both for a missing user and for the
		// last remaining admin — look the row up to tell the two apart.
		if _, lookupErr := s.GetUserByID(ctx, id); lookupErr != nil {
			return lookupErr
		}
		return ErrWouldLeaveNoAdmin
	}
	return nil
}

func (s *PostgresUserStore) UpdateUserEnabled(ctx context.Context, id string, enabled bool) error {
	commandTag, err := s.engine.Queries.UpdateUserEnabledByID(ctx, pgdb.UpdateUserEnabledByIDParams{
		ID:      ToPgUUID(id),
		Enabled: enabled,
	})
	if err != nil {
		return fmt.Errorf("failed to update user enabled flag in database: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		// The guarded query matches no row both for a missing user and for the
		// last remaining enabled admin — look the row up to tell the two apart.
		if _, lookupErr := s.GetUserByID(ctx, id); lookupErr != nil {
			return lookupErr
		}
		return ErrWouldLeaveNoAdmin
	}
	return nil
}

// BumpUserSessionVersion revokes every session the account currently holds:
// each of its live JWTs carries the previous generation and stops validating
// on its next use, access tokens included.
func (s *PostgresUserStore) BumpUserSessionVersion(ctx context.Context, id string) error {
	commandTag, err := s.engine.Queries.BumpUserSessionVersion(ctx, ToPgUUID(id))
	if err != nil {
		return fmt.Errorf("failed to bump user session version in database: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		return &ErrResourceNotFound{Resource: "user", Identifier: id}
	}
	return nil
}

func (s *PostgresUserStore) TouchUserLastConnected(ctx context.Context, id string) error {
	if err := s.engine.Queries.TouchUserLastConnectedAt(ctx, ToPgUUID(id)); err != nil {
		return fmt.Errorf("failed to touch user last connection in database: %w", err)
	}
	return nil
}

func userFromRow(row pgdb.User) User {
	return User{
		Id:              row.ID.String(),
		Email:           row.Email,
		PasswordHash:    row.PasswordHash,
		IsAdmin:         row.IsAdmin,
		Enabled:         row.Enabled,
		CreatedAt:       row.CreatedAt.Time,
		LastConnectedAt: timestamptzToPtr(row.LastConnectedAt),
		SessionVersion:  row.SessionVersion,
	}
}

func timestamptzToPtr(ts pgtype.Timestamptz) *time.Time {
	if !ts.Valid {
		return nil
	}
	t := ts.Time
	return &t
}
