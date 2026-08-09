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

// SealedAndroidCredentials is the at-rest shape of an identifier's Android
// signing credentials: every secret field is an AES-GCM sealed blob, never
// plaintext.
type SealedAndroidCredentials struct {
	KeyAlias                      string
	SealedKeystore                string
	SealedKeystorePassword        string
	SealedKeyPassword             string
	SealedGoogleServiceAccountKey *string
	CreatedAt                     time.Time
	UpdatedAt                     time.Time
}

type PostgresCredentialsStore struct {
	engine *database.Engine
}

func NewPostgresCredentialsStore(engine *database.Engine) *PostgresCredentialsStore {
	return &PostgresCredentialsStore{
		engine: engine,
	}
}

func (s *PostgresCredentialsStore) UpsertAndroidCredentials(ctx context.Context, identifierId string, credentials SealedAndroidCredentials) error {
	_, err := s.engine.Queries.UpsertAndroidCredentials(ctx, pgdb.UpsertAndroidCredentialsParams{
		ID:                            ToPgUUID(uuid.NewString()),
		AppIdentifierID:               ToPgUUID(identifierId),
		KeyAlias:                      credentials.KeyAlias,
		SealedKeystore:                credentials.SealedKeystore,
		SealedKeystorePassword:        credentials.SealedKeystorePassword,
		SealedKeyPassword:             credentials.SealedKeyPassword,
		SealedGoogleServiceAccountKey: credentials.SealedGoogleServiceAccountKey,
	})
	if err != nil {
		return fmt.Errorf("failed to save android credentials in database: %w", err)
	}
	return nil
}

func (s *PostgresCredentialsStore) GetAndroidCredentials(ctx context.Context, identifierId string) (*SealedAndroidCredentials, error) {
	row, err := s.engine.Queries.GetAndroidCredentialsByIdentifierID(ctx, ToPgUUID(identifierId))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to retrieve android credentials from database: %w", err)
	}
	return &SealedAndroidCredentials{
		KeyAlias:                      row.KeyAlias,
		SealedKeystore:                row.SealedKeystore,
		SealedKeystorePassword:        row.SealedKeystorePassword,
		SealedKeyPassword:             row.SealedKeyPassword,
		SealedGoogleServiceAccountKey: row.SealedGoogleServiceAccountKey,
		CreatedAt:                     row.CreatedAt.Time,
		UpdatedAt:                     row.UpdatedAt.Time,
	}, nil
}

func (s *PostgresCredentialsStore) DeleteAndroidCredentials(ctx context.Context, identifierId string) error {
	commandTag, err := s.engine.Queries.DeleteAndroidCredentialsByIdentifierID(ctx, ToPgUUID(identifierId))
	if err != nil {
		return fmt.Errorf("failed to delete android credentials from database: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		return &ErrResourceNotFound{Resource: "android credentials", Identifier: fmt.Sprintf("identifierId: %s", identifierId)}
	}
	return nil
}
