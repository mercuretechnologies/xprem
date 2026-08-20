// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package licensing

import (
	"context"
	"fmt"
	"time"
	"xprem/internal/crypto"
	"xprem/internal/database"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/keyStore"

	"github.com/jackc/pgx/v5/pgtype"
)

// activationSecretAAD is the AES-GCM AAD binding the sealed secret to this
// column.
func activationSecretAAD() []byte {
	return []byte("enterprise_license|activation_secret")
}

type PostgresLicenseStore struct {
	engine *database.Engine
}

func NewPostgresLicenseStore(engine *database.Engine) *PostgresLicenseStore {
	return &PostgresLicenseStore{engine: engine}
}

func toPgTimestamptz(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}

func fromPgTimestamptz(ts pgtype.Timestamptz) *time.Time {
	if !ts.Valid {
		return nil
	}
	t := ts.Time
	return &t
}

func storedFromRow(row pgdb.EnterpriseLicense) *StoredLicense {
	stored := &StoredLicense{
		Key: row.LicenseKey,
		License: License{
			OrgName:               row.OrgName,
			PlanCode:              row.PlanCode,
			SubscriptionStartAt:   row.SubscriptionStartAt.Time,
			SubscriptionEndAt:     fromPgTimestamptz(row.SubscriptionEndAt),
			SubscriptionRenewalAt: fromPgTimestamptz(row.SubscriptionRenewalAt),
		},
		ActivatedAt:        row.ActivatedAt.Time,
		LastValidatedAt:    fromPgTimestamptz(row.LastValidatedAt),
		ValidationFailedAt: fromPgTimestamptz(row.ValidationFailedAt),
	}
	if row.ValidationErrorCode != nil {
		stored.ValidationErrorCode = *row.ValidationErrorCode
	}
	return stored
}

func (s *PostgresLicenseStore) GetLicense(ctx context.Context) (*StoredLicense, error) {
	row, err := s.engine.Queries.GetEnterpriseLicense(ctx)
	if err != nil {
		if database.IsNoRows(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read enterprise license from database: %w", err)
	}
	return storedFromRow(row), nil
}

func (s *PostgresLicenseStore) GetActivationSecret(ctx context.Context) (string, error) {
	row, err := s.engine.Queries.GetEnterpriseLicense(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to read enterprise license from database: %w", err)
	}
	secret, err := crypto.UnsealAESGCM(row.SealedActivationSecret, []byte(keyStore.ReadDBKeysMasterKey()), activationSecretAAD())
	if err != nil {
		return "", fmt.Errorf("failed to unseal the license activation secret: %w", err)
	}
	return string(secret), nil
}

func (s *PostgresLicenseStore) SaveActivation(ctx context.Context, key string, activationSecret string, license License) (StoredLicense, error) {
	sealedSecret, err := crypto.SealAESGCM([]byte(activationSecret), []byte(keyStore.ReadDBKeysMasterKey()), activationSecretAAD())
	if err != nil {
		return StoredLicense{}, fmt.Errorf("failed to seal the license activation secret: %w", err)
	}
	startAt := license.SubscriptionStartAt
	row, err := s.engine.Queries.UpsertEnterpriseLicense(ctx, pgdb.UpsertEnterpriseLicenseParams{
		LicenseKey:             key,
		SealedActivationSecret: sealedSecret,
		OrgName:                license.OrgName,
		PlanCode:               license.PlanCode,
		SubscriptionStartAt:    toPgTimestamptz(&startAt),
		SubscriptionEndAt:      toPgTimestamptz(license.SubscriptionEndAt),
		SubscriptionRenewalAt:  toPgTimestamptz(license.SubscriptionRenewalAt),
	})
	if err != nil {
		return StoredLicense{}, fmt.Errorf("failed to store enterprise license in database: %w", err)
	}
	return *storedFromRow(row), nil
}

func (s *PostgresLicenseStore) MarkValidated(ctx context.Context, license License) (StoredLicense, error) {
	startAt := license.SubscriptionStartAt
	row, err := s.engine.Queries.MarkEnterpriseLicenseValidated(ctx, pgdb.MarkEnterpriseLicenseValidatedParams{
		OrgName:               license.OrgName,
		PlanCode:              license.PlanCode,
		SubscriptionStartAt:   toPgTimestamptz(&startAt),
		SubscriptionEndAt:     toPgTimestamptz(license.SubscriptionEndAt),
		SubscriptionRenewalAt: toPgTimestamptz(license.SubscriptionRenewalAt),
	})
	if err != nil {
		return StoredLicense{}, fmt.Errorf("failed to record the license validation in database: %w", err)
	}
	return *storedFromRow(row), nil
}

func (s *PostgresLicenseStore) MarkValidationFailed(ctx context.Context, errorCode string) (StoredLicense, error) {
	row, err := s.engine.Queries.MarkEnterpriseLicenseValidationFailed(ctx, &errorCode)
	if err != nil {
		return StoredLicense{}, fmt.Errorf("failed to record the license validation failure in database: %w", err)
	}
	return *storedFromRow(row), nil
}

func (s *PostgresLicenseStore) DeleteLicense(ctx context.Context) error {
	if err := s.engine.Queries.DeleteEnterpriseLicense(ctx); err != nil {
		return fmt.Errorf("failed to delete enterprise license from database: %w", err)
	}
	return nil
}
