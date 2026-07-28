// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package sso

import (
	"context"
	"expo-open-ota/internal/crypto"
	"expo-open-ota/internal/database"
	"expo-open-ota/internal/database/postgres/pgdb"
	"expo-open-ota/internal/keyStore"
	"expo-open-ota/internal/store"
	"fmt"
)

// clientSecretAAD binds the sealed client secret blob to its context as
// AES-GCM additional authenticated data, following the keyStore.AppKeyAAD
// doctrine: derived at seal/unseal time, never stored. A blob copied into
// this column from any other sealed context refuses to open here.
func clientSecretAAD() []byte {
	return []byte("sso|client_secret")
}

type PostgresSSOStore struct {
	engine *database.Engine
}

func NewPostgresSSOStore(engine *database.Engine) *PostgresSSOStore {
	return &PostgresSSOStore{engine: engine}
}

func (s *PostgresSSOStore) GetConfig(ctx context.Context) (*SSOConfig, error) {
	row, err := s.engine.Queries.GetSSOConfig(ctx)
	if err != nil {
		if database.IsNoRows(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read the sso configuration from database: %w", err)
	}
	cfg := &SSOConfig{
		Issuer:               row.Issuer,
		ClientID:             row.ClientID,
		ProviderName:         row.ProviderName,
		Scopes:               row.Scopes,
		Enabled:              row.Enabled,
		AllowedEmailDomains:  row.AllowedEmailDomains,
		AllowedGroups:        row.AllowedGroups,
		GroupsClaim:          row.GroupsClaim,
		TrustUnverifiedEmail: row.TrustUnverifiedEmail,
		ManualUserValidation: row.ManualUserValidation,
	}
	secret, err := crypto.UnsealAESGCM(row.SealedClientSecret, []byte(keyStore.ReadDBKeysMasterKey()), clientSecretAAD())
	if err != nil {
		// The configuration itself is intact: return it alongside the marker
		// so the dashboard can prompt for the secret instead of going blank.
		return cfg, fmt.Errorf("%w: %v", ErrClientSecretUnreadable, err)
	}
	cfg.ClientSecret = string(secret)
	return cfg, nil
}

func (s *PostgresSSOStore) SaveConfig(ctx context.Context, cfg SSOConfig) error {
	sealedSecret, err := crypto.SealAESGCM([]byte(cfg.ClientSecret), []byte(keyStore.ReadDBKeysMasterKey()), clientSecretAAD())
	if err != nil {
		return fmt.Errorf("failed to seal the sso client secret: %w", err)
	}
	if _, err := s.engine.Queries.UpsertSSOConfig(ctx, pgdb.UpsertSSOConfigParams{
		Issuer:               cfg.Issuer,
		ClientID:             cfg.ClientID,
		SealedClientSecret:   sealedSecret,
		ProviderName:         cfg.ProviderName,
		Scopes:               cfg.Scopes,
		Enabled:              cfg.Enabled,
		AllowedEmailDomains:  emptyIfNil(cfg.AllowedEmailDomains),
		AllowedGroups:        emptyIfNil(cfg.AllowedGroups),
		GroupsClaim:          cfg.GroupsClaim,
		TrustUnverifiedEmail: cfg.TrustUnverifiedEmail,
		ManualUserValidation: cfg.ManualUserValidation,
	}); err != nil {
		return fmt.Errorf("failed to store the sso configuration in database: %w", err)
	}
	return nil
}

// emptyIfNil keeps a nil slice from becoming SQL NULL: the array columns are
// NOT NULL, empty means "no restriction".
func emptyIfNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func (s *PostgresSSOStore) DeleteConfig(ctx context.Context) error {
	if err := s.engine.Queries.DeleteSSOConfig(ctx); err != nil {
		return fmt.Errorf("failed to delete the sso configuration from database: %w", err)
	}
	return nil
}

func (s *PostgresSSOStore) FindUserBySubject(ctx context.Context, issuer string, subject string) (store.User, error) {
	row, err := s.engine.Queries.GetUserBySSOSubject(ctx, pgdb.GetUserBySSOSubjectParams{
		Issuer:  issuer,
		Subject: subject,
	})
	if err != nil {
		if database.IsNoRows(err) {
			return store.User{}, &store.ErrResourceNotFound{Resource: "sso identity", Identifier: subject}
		}
		return store.User{}, fmt.Errorf("failed to resolve the sso identity from database: %w", err)
	}
	return userFromPgdbRow(row), nil
}

func (s *PostgresSSOStore) LinkIdentity(ctx context.Context, issuer string, subject string, userID string, email string) error {
	err := s.engine.Queries.InsertSSOIdentity(ctx, pgdb.InsertSSOIdentityParams{
		Issuer:  issuer,
		Subject: subject,
		UserID:  store.ToPgUUID(userID),
		Email:   store.NormalizeEmail(email),
	})
	if err != nil {
		if database.IsUniqueViolation(err) {
			return &store.ErrResourceAlreadyExists{Resource: "sso identity", Identifier: subject}
		}
		return fmt.Errorf("failed to link the sso identity in database: %w", err)
	}
	return nil
}

// ProvisionUser inserts the user row and its sso identity in one transaction:
// a failure on either side leaves nothing behind, so the retry after a
// concurrent first sign-in starts from a clean slate.
func (s *PostgresSSOStore) ProvisionUser(ctx context.Context, params store.InsertUserParameters, issuer string, subject string) (store.User, error) {
	email := store.NormalizeEmail(params.Email)
	var user store.User
	err := s.engine.WithTx(ctx, func(q *pgdb.Queries) error {
		row, err := q.InsertUser(ctx, pgdb.InsertUserParams{
			ID:           store.ToPgUUID(params.ID),
			Email:        email,
			PasswordHash: params.PasswordHash,
			IsAdmin:      params.IsAdmin,
			Enabled:      params.Enabled,
		})
		if err != nil {
			if database.IsUniqueViolation(err) {
				return &store.ErrResourceAlreadyExists{Resource: "user", Identifier: email}
			}
			return fmt.Errorf("failed to insert user into database: %w", err)
		}
		user = store.User{
			Id:        row.ID.String(),
			Email:     row.Email,
			IsAdmin:   row.IsAdmin,
			Enabled:   row.Enabled,
			CreatedAt: row.CreatedAt.Time,
		}
		if err := q.InsertSSOIdentity(ctx, pgdb.InsertSSOIdentityParams{
			Issuer:  issuer,
			Subject: subject,
			UserID:  row.ID,
			Email:   email,
		}); err != nil {
			if database.IsUniqueViolation(err) {
				return &store.ErrResourceAlreadyExists{Resource: "sso identity", Identifier: subject}
			}
			return fmt.Errorf("failed to insert the sso identity into database: %w", err)
		}
		return nil
	})
	if err != nil {
		return store.User{}, err
	}
	return user, nil
}

func (s *PostgresSSOStore) TouchLastLogin(ctx context.Context, issuer string, subject string) error {
	if err := s.engine.Queries.TouchSSOIdentityLastLogin(ctx, pgdb.TouchSSOIdentityLastLoginParams{
		Issuer:  issuer,
		Subject: subject,
	}); err != nil {
		return fmt.Errorf("failed to touch the sso identity last login in database: %w", err)
	}
	return nil
}

func userFromPgdbRow(row pgdb.User) store.User {
	user := store.User{
		Id:           row.ID.String(),
		Email:        row.Email,
		PasswordHash: row.PasswordHash,
		IsAdmin:      row.IsAdmin,
		Enabled:      row.Enabled,
		CreatedAt:    row.CreatedAt.Time,
		// Load-bearing: this row feeds IssueSession, which stamps the account's
		// security generation into both JWTs. Dropping it here would mint every
		// returning SSO session at generation 0, and any account whose sessions
		// were ever revoked would sign in successfully and then be refused on
		// its very next request, forever.
		SessionVersion: row.SessionVersion,
	}
	if row.LastConnectedAt.Valid {
		lastConnected := row.LastConnectedAt.Time
		user.LastConnectedAt = &lastConnected
	}
	return user
}
