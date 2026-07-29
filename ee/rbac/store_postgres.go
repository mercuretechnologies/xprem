// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package rbac

import (
	"context"
	"errors"
	"expo-open-ota/internal/database"
	"expo-open-ota/internal/database/postgres/pgdb"
	"expo-open-ota/internal/store"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

type PostgresRBACStore struct {
	engine *database.Engine
}

func NewPostgresRBACStore(engine *database.Engine) *PostgresRBACStore {
	return &PostgresRBACStore{engine: engine}
}

func toPermissions(values []string) []Permission {
	perms := make([]Permission, len(values))
	for i, v := range values {
		perms[i] = Permission(v)
	}
	return perms
}

// fromPermissions also keeps a nil slice from becoming SQL NULL: the array
// columns are NOT NULL, empty means "no permissions".
func fromPermissions(perms []Permission) []string {
	values := make([]string, len(perms))
	for i, p := range perms {
		values[i] = string(p)
	}
	return values
}

func roleFromRow(row pgdb.Role) Role {
	return Role{
		ID:          row.ID.String(),
		Name:        row.Name,
		Permissions: toPermissions(row.Permissions),
		CreatedAt:   row.CreatedAt.Time,
		UpdatedAt:   row.UpdatedAt.Time,
	}
}

func (s *PostgresRBACStore) ListRoles(ctx context.Context) ([]Role, error) {
	rows, err := s.engine.Queries.ListRoles(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list roles from database: %w", err)
	}
	roles := make([]Role, len(rows))
	for i, row := range rows {
		roles[i] = roleFromRow(row)
	}
	return roles, nil
}

func (s *PostgresRBACStore) GetRoleByID(ctx context.Context, id string) (Role, error) {
	row, err := s.engine.Queries.GetRoleByID(ctx, store.ToPgUUID(id))
	if err != nil {
		if database.IsNoRows(err) {
			return Role{}, ErrRoleNotFound
		}
		return Role{}, fmt.Errorf("failed to read role from database: %w", err)
	}
	return roleFromRow(row), nil
}

func (s *PostgresRBACStore) InsertRole(ctx context.Context, role Role) (Role, error) {
	row, err := s.engine.Queries.InsertRole(ctx, pgdb.InsertRoleParams{
		ID:          store.ToPgUUID(role.ID),
		Name:        role.Name,
		Permissions: fromPermissions(role.Permissions),
	})
	if err != nil {
		if database.IsUniqueViolation(err) {
			return Role{}, &store.ErrResourceAlreadyExists{Resource: "role", Identifier: role.Name}
		}
		return Role{}, fmt.Errorf("failed to insert role in database: %w", err)
	}
	return roleFromRow(row), nil
}

func (s *PostgresRBACStore) UpdateRole(ctx context.Context, id string, name string, permissions []Permission) error {
	commandTag, err := s.engine.Queries.UpdateRole(ctx, pgdb.UpdateRoleParams{
		ID:          store.ToPgUUID(id),
		Name:        name,
		Permissions: fromPermissions(permissions),
	})
	if err != nil {
		if database.IsUniqueViolation(err) {
			return &store.ErrResourceAlreadyExists{Resource: "role", Identifier: name}
		}
		return fmt.Errorf("failed to update role in database: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		return ErrRoleNotFound
	}
	return nil
}

func (s *PostgresRBACStore) DeleteRole(ctx context.Context, id string) error {
	commandTag, err := s.engine.Queries.DeleteRole(ctx, store.ToPgUUID(id))
	if err != nil {
		// The ON DELETE RESTRICT from user_app_grants.role_id.
		if database.IsForeignKeyViolation(err) {
			return ErrRoleInUse
		}
		return fmt.Errorf("failed to delete role from database: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		return ErrRoleNotFound
	}
	return nil
}

func (s *PostgresRBACStore) ListUserGrants(ctx context.Context, userID string) ([]AppGrant, error) {
	rows, err := s.engine.Queries.ListUserAppGrants(ctx, store.ToPgUUID(userID))
	if err != nil {
		return nil, fmt.Errorf("failed to list user grants from database: %w", err)
	}
	grants := make([]AppGrant, len(rows))
	for i, row := range rows {
		grant := AppGrant{
			AppID:            row.AppID.String(),
			RoleName:         row.RoleName,
			RolePermissions:  toPermissions(row.RolePermissions),
			ExtraPermissions: toPermissions(row.ExtraPermissions),
		}
		if row.RoleID.Valid {
			roleID := row.RoleID.String()
			grant.RoleID = &roleID
		}
		grants[i] = grant
	}
	return grants, nil
}

// GetUserAppGrant returns nil (no error) when the member has no grant on the
// app. RoleName stays nil here, the enforcement path only needs the
// permissions, ListUserGrants is the display read.
func (s *PostgresRBACStore) GetUserAppGrant(ctx context.Context, userID string, appID string) (*AppGrant, error) {
	row, err := s.engine.Queries.GetUserAppGrant(ctx, pgdb.GetUserAppGrantParams{
		UserID: store.ToPgUUID(userID),
		AppID:  store.ToPgUUID(appID),
	})
	if err != nil {
		if database.IsNoRows(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read user grant from database: %w", err)
	}
	grant := &AppGrant{
		AppID:            row.AppID.String(),
		RolePermissions:  toPermissions(row.RolePermissions),
		ExtraPermissions: toPermissions(row.ExtraPermissions),
	}
	if row.RoleID.Valid {
		roleID := row.RoleID.String()
		grant.RoleID = &roleID
	}
	return grant, nil
}

// ReplaceUserGrants swaps every grant of one member atomically: readers never
// observe a half-written set, and any failure (unknown app, unknown role)
// rolls the previous grants back untouched.
func (s *PostgresRBACStore) ReplaceUserGrants(ctx context.Context, userID string, grants []GrantInput) error {
	pgUserID := store.ToPgUUID(userID)
	err := s.engine.WithTx(ctx, func(q *pgdb.Queries) error {
		if err := q.DeleteUserAppGrantsByUser(ctx, pgUserID); err != nil {
			return err
		}
		for _, grant := range grants {
			// The zero pgtype.UUID (Valid: false) is SQL NULL, a grant
			// without a role.
			var roleID pgtype.UUID
			if grant.RoleID != nil {
				roleID = store.ToPgUUID(*grant.RoleID)
				if !roleID.Valid {
					return &ValidationError{Message: fmt.Sprintf("invalid role id %q", *grant.RoleID)}
				}
			}
			if err := q.InsertUserAppGrant(ctx, pgdb.InsertUserAppGrantParams{
				UserID:           pgUserID,
				AppID:            store.ToPgUUID(grant.AppID),
				RoleID:           roleID,
				ExtraPermissions: fromPermissions(grant.ExtraPermissions),
			}); err != nil {
				return replaceGrantError(err, grant)
			}
		}
		return nil
	})
	if err != nil {
		validationErr := (*ValidationError)(nil)
		if errors.As(err, &validationErr) {
			return err
		}
		return fmt.Errorf("failed to replace user grants in database: %w", err)
	}
	return nil
}

// replaceGrantError turns the grant insert's foreign-key violations into
// messages naming the reference that does not exist, using the constraint
// name Postgres reports.
func replaceGrantError(err error, grant GrantInput) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && database.IsForeignKeyViolation(err) {
		switch {
		case strings.Contains(pgErr.ConstraintName, "app_id"):
			return &ValidationError{Message: fmt.Sprintf("app %q does not exist", grant.AppID)}
		case strings.Contains(pgErr.ConstraintName, "role_id"):
			roleID := ""
			if grant.RoleID != nil {
				roleID = *grant.RoleID
			}
			return &ValidationError{Message: fmt.Sprintf("role %q does not exist", roleID)}
		case strings.Contains(pgErr.ConstraintName, "user_id"):
			return &ValidationError{Message: "user does not exist"}
		}
	}
	return err
}

func (s *PostgresRBACStore) GrantCountsByUser(ctx context.Context) (map[string]int64, error) {
	rows, err := s.engine.Queries.CountGrantsPerUser(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to count grants per user from database: %w", err)
	}
	counts := make(map[string]int64, len(rows))
	for _, row := range rows {
		counts[row.UserID.String()] = row.GrantCount
	}
	return counts, nil
}

func (s *PostgresRBACStore) ListAccessibleAppIDs(ctx context.Context, userID string) ([]string, error) {
	rows, err := s.engine.Queries.ListAccessibleAppIDs(ctx, store.ToPgUUID(userID))
	if err != nil {
		return nil, fmt.Errorf("failed to list accessible apps from database: %w", err)
	}
	ids := make([]string, len(rows))
	for i, row := range rows {
		ids[i] = row.String()
	}
	return ids, nil
}
