package repository

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

// SealedIosCertificateFile is the AES-GCM sealed PKCS#12 file of a pool certificate and its sealed password.
type SealedIosCertificateFile struct {
	SealedCertificate string
	SealedPassword    string
}

// SealIosCertificateFunc seals the PKCS#12 file and its password for the pool row certificateId.
type SealIosCertificateFunc func(certificateId string) (sealedCertificate string, sealedPassword string, err error)

type PostgresIosCredentialsRepository struct {
	engine *database.Engine
}

// NewPostgresIosCredentialsRepository connects iOS credential persistence to the database engine.
func NewPostgresIosCredentialsRepository(engine *database.Engine) *PostgresIosCredentialsRepository {
	return &PostgresIosCredentialsRepository{
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
		CreatedAt:       row.CreatedAt.Time,
	}
}

// SaveIosCertificate adds the certificate to the pool, or replaces the stored file of the pool row
// with the same fingerprint. It returns the id of the pool row.
func (s *PostgresIosCredentialsRepository) SaveIosCertificate(ctx context.Context, certificate IosCertificate, seal SealIosCertificateFunc) (string, error) {
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
func (s *PostgresIosCredentialsRepository) GetIosCertificate(ctx context.Context, certificateId string) (*IosCertificate, error) {
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

// GetIosCertificateFile returns (nil, nil) when the pool has no such certificate.
func (s *PostgresIosCredentialsRepository) GetIosCertificateFile(ctx context.Context, certificateId string) (*SealedIosCertificateFile, error) {
	row, err := s.engine.Queries.GetIosCertificateFile(ctx, ToPgUUID(certificateId))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to retrieve ios certificate file from database: %w", err)
	}
	return &SealedIosCertificateFile{SealedCertificate: row.SealedCertificate, SealedPassword: row.SealedCertificatePassword}, nil
}

// ListIosCertificates returns the whole pool, newest first.
func (s *PostgresIosCredentialsRepository) ListIosCertificates(ctx context.Context) ([]IosCertificate, error) {
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
func (s *PostgresIosCredentialsRepository) GetIosSigningSetting(ctx context.Context, identifierId string) (*IosSigningSetting, error) {
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
func (s *PostgresIosCredentialsRepository) UpsertIosSigningSetting(ctx context.Context, identifierId string, setting IosSigningSetting) error {
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
func (s *PostgresIosCredentialsRepository) UpsertAppStoreConnectApiKey(ctx context.Context, appId string, key SealedAppStoreConnectApiKey) error {
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
func (s *PostgresIosCredentialsRepository) GetAppStoreConnectApiKey(ctx context.Context, appId string) (*SealedAppStoreConnectApiKey, error) {
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

// DeleteAppStoreConnectApiKey removes the app team credentials and revokes its pending registration
// links in one transaction, or reports that no key exists.
func (s *PostgresIosCredentialsRepository) DeleteAppStoreConnectApiKey(ctx context.Context, appId string) ([]RevokedIosDeviceInvitation, error) {
	var revoked []RevokedIosDeviceInvitation
	err := s.engine.WithTx(ctx, func(q *pgdb.Queries) error {
		commandTag, err := q.DeleteAppStoreConnectApiKey(ctx, ToPgUUID(appId))
		if err != nil {
			return fmt.Errorf("failed to delete app store connect api key from database: %w", err)
		}
		if commandTag.RowsAffected() == 0 {
			return &ErrResourceNotFound{Resource: "app store connect api key", Identifier: fmt.Sprintf("appId: %s", appId)}
		}
		rows, err := q.RevokePendingIosDeviceInvitations(ctx, ToPgUUID(appId))
		if err != nil {
			return fmt.Errorf("failed to revoke pending ios device invitations in database: %w", err)
		}
		revoked = make([]RevokedIosDeviceInvitation, len(rows))
		for i, row := range rows {
			revoked[i] = RevokedIosDeviceInvitation{Id: row.ID.String(), Label: row.Label}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return revoked, nil
}
