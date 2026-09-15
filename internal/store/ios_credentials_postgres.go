package store

import (
	"context"
	"errors"
	"fmt"
	"time"
	"xprem/internal/database"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/types"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// IosCertificate is the non-secret identity of a certificate of the instance-wide pool.
type IosCertificate struct {
	Id              string
	CommonName      string
	SerialNumber    string
	FingerprintSHA1 string
	Type            types.IosCertificateType
	TeamID          string
	ExpiresAt       time.Time
	Source          types.IosCertificateSource
	CreatedAt       time.Time
}

// IosSigningSetting is the stored signing choice of an identifier; CertificateId is nil in mode
// certificate once the selected certificate was deleted.
type IosSigningSetting struct {
	Mode          types.IosSigningMode
	CertificateId *string
}

// SealedAppStoreConnectApiKey is an app's App Store Connect API key with its AES-GCM sealed .p8 file.
type SealedAppStoreConnectApiKey struct {
	KeyID            string
	IssuerID         string
	SealedPrivateKey string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// SealIosCertificateFunc seals the PKCS#12 file and its password for the pool row certificateId.
type SealIosCertificateFunc func(certificateId string) (sealedCertificate string, sealedPassword string, err error)

type PostgresIosCredentialsStore struct {
	engine *database.Engine
}

// NewPostgresIosCredentialsStore connects iOS credential persistence to the database engine.
func NewPostgresIosCredentialsStore(engine *database.Engine) *PostgresIosCredentialsStore {
	return &PostgresIosCredentialsStore{
		engine: engine,
	}
}

// iosCertificateFromRow converts a database certificate row into the store representation.
func iosCertificateFromRow(row pgdb.GetIosCertificateRow) IosCertificate {
	return IosCertificate{
		Id:              row.ID.String(),
		CommonName:      row.CommonName,
		SerialNumber:    row.SerialNumber,
		FingerprintSHA1: row.FingerprintSha1,
		Type:            row.CertificateType,
		TeamID:          row.TeamID,
		ExpiresAt:       row.ExpiresAt.Time,
		Source:          row.Source,
		CreatedAt:       row.CreatedAt.Time,
	}
}

// SaveIosCertificate adds the certificate to the pool, or replaces the stored file of the pool row
// with the same fingerprint, which keeps its source. It returns the id of the pool row.
func (s *PostgresIosCredentialsStore) SaveIosCertificate(ctx context.Context, certificate IosCertificate, seal SealIosCertificateFunc) (string, error) {
	var certificateId string
	err := s.engine.WithTx(ctx, func(q *pgdb.Queries) error {
		newId := uuid.NewString()
		sealedCertificate, sealedPassword, err := seal(newId)
		if err != nil {
			return err
		}
		id, err := q.InsertIosCertificate(ctx, pgdb.InsertIosCertificateParams{
			ID:                        ToPgUUID(newId),
			SealedCertificate:         sealedCertificate,
			SealedCertificatePassword: sealedPassword,
			CommonName:                certificate.CommonName,
			SerialNumber:              certificate.SerialNumber,
			FingerprintSha1:           certificate.FingerprintSHA1,
			CertificateType:           certificate.Type,
			TeamID:                    certificate.TeamID,
			ExpiresAt:                 ToPgTimestamptz(&certificate.ExpiresAt),
			Source:                    certificate.Source,
		})
		if err != nil {
			return fmt.Errorf("failed to save ios certificate in database: %w", err)
		}
		certificateId = id.String()
		if certificateId == newId {
			return nil
		}
		if sealedCertificate, sealedPassword, err = seal(certificateId); err != nil {
			return err
		}
		err = q.UpdateIosCertificateFile(ctx, pgdb.UpdateIosCertificateFileParams{
			ID:                        id,
			SealedCertificate:         sealedCertificate,
			SealedCertificatePassword: sealedPassword,
		})
		if err != nil {
			return fmt.Errorf("failed to update ios certificate in database: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return certificateId, nil
}

// GetIosCertificate returns (nil, nil) when the pool has no such certificate.
func (s *PostgresIosCredentialsStore) GetIosCertificate(ctx context.Context, certificateId string) (*IosCertificate, error) {
	row, err := s.engine.Queries.GetIosCertificate(ctx, ToPgUUID(certificateId))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to retrieve ios certificate from database: %w", err)
	}
	certificate := iosCertificateFromRow(row)
	return &certificate, nil
}

// ListIosCertificates returns the whole pool, newest first.
func (s *PostgresIosCredentialsStore) ListIosCertificates(ctx context.Context) ([]IosCertificate, error) {
	rows, err := s.engine.Queries.ListIosCertificates(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve ios certificates from database: %w", err)
	}
	certificates := make([]IosCertificate, len(rows))
	for i, row := range rows {
		certificates[i] = iosCertificateFromRow(pgdb.GetIosCertificateRow(row))
	}
	return certificates, nil
}

// GetIosSigningSetting returns (nil, nil) when the identifier has no stored setting.
func (s *PostgresIosCredentialsStore) GetIosSigningSetting(ctx context.Context, identifierId string) (*IosSigningSetting, error) {
	row, err := s.engine.Queries.GetIosSigningSetting(ctx, ToPgUUID(identifierId))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to retrieve ios signing setting from database: %w", err)
	}
	setting := &IosSigningSetting{Mode: row.Mode}
	if row.CertificateID.Valid {
		certificateId := row.CertificateID.String()
		setting.CertificateId = &certificateId
	}
	return setting, nil
}

// UpsertIosSigningSetting replaces the identifier single shared iOS signing choice.
func (s *PostgresIosCredentialsStore) UpsertIosSigningSetting(ctx context.Context, identifierId string, setting IosSigningSetting) error {
	err := s.engine.Queries.UpsertIosSigningSetting(ctx, pgdb.UpsertIosSigningSettingParams{
		AppIdentifierID: ToPgUUID(identifierId),
		Mode:            setting.Mode,
		CertificateID:   ToPgUUIDPtr(setting.CertificateId),
	})
	if err != nil {
		return fmt.Errorf("failed to save ios signing setting in database: %w", err)
	}
	return nil
}

// UpsertAppStoreConnectApiKey stores the app sealed API key and refreshes its update timestamp.
func (s *PostgresIosCredentialsStore) UpsertAppStoreConnectApiKey(ctx context.Context, appId string, key SealedAppStoreConnectApiKey) error {
	err := s.engine.Queries.UpsertAppStoreConnectApiKey(ctx, pgdb.UpsertAppStoreConnectApiKeyParams{
		ID:               ToPgUUID(uuid.NewString()),
		AppID:            ToPgUUID(appId),
		KeyID:            key.KeyID,
		IssuerID:         key.IssuerID,
		SealedPrivateKey: key.SealedPrivateKey,
	})
	if err != nil {
		return fmt.Errorf("failed to save app store connect api key in database: %w", err)
	}
	return nil
}

// GetAppStoreConnectApiKey returns (nil, nil) when the app has no key.
func (s *PostgresIosCredentialsStore) GetAppStoreConnectApiKey(ctx context.Context, appId string) (*SealedAppStoreConnectApiKey, error) {
	row, err := s.engine.Queries.GetAppStoreConnectApiKey(ctx, ToPgUUID(appId))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to retrieve app store connect api key from database: %w", err)
	}
	return &SealedAppStoreConnectApiKey{
		KeyID:            row.KeyID,
		IssuerID:         row.IssuerID,
		SealedPrivateKey: row.SealedPrivateKey,
		CreatedAt:        row.CreatedAt.Time,
		UpdatedAt:        row.UpdatedAt.Time,
	}, nil
}

// DeleteAppStoreConnectApiKey removes the app team credentials or reports that no key exists.
func (s *PostgresIosCredentialsStore) DeleteAppStoreConnectApiKey(ctx context.Context, appId string) error {
	commandTag, err := s.engine.Queries.DeleteAppStoreConnectApiKey(ctx, ToPgUUID(appId))
	if err != nil {
		return fmt.Errorf("failed to delete app store connect api key from database: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		return &ErrResourceNotFound{Resource: "app store connect api key", Identifier: fmt.Sprintf("appId: %s", appId)}
	}
	return nil
}
