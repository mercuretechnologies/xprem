package services

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
	"xprem/internal/android"
	"xprem/internal/auditlog"
	"xprem/internal/crypto"
	"xprem/internal/keyStore"
	"xprem/internal/store"
	"xprem/internal/validation"
)

// maxKeystoreBytes caps the decoded keystore blob; real-world .jks files are
// a few kilobytes, anything near the cap is not a keystore.
const maxKeystoreBytes = 512 * 1024

type CredentialsRepository interface {
	UpsertAndroidCredentials(ctx context.Context, identifierId string, credentials store.SealedAndroidCredentials) error
	GetAndroidCredentials(ctx context.Context, identifierId string) (*store.SealedAndroidCredentials, error)
	DeleteAndroidCredentials(ctx context.Context, identifierId string) error
}

// AndroidCredentialsInput is one full replacement of an identifier's Android
// signing credentials, as received from the dashboard or the CLI.
type AndroidCredentialsInput struct {
	KeyAlias                    string
	KeystoreBase64              string
	KeystorePassword            string
	KeyPassword                 string
	GoogleServiceAccountKeyJSON string
}

// AndroidCredentialsMetadata is the non-secret projection served to viewers.
type AndroidCredentialsMetadata struct {
	Identifier                 string `json:"identifier"`
	KeyAlias                   string `json:"keyAlias"`
	HasGoogleServiceAccountKey bool   `json:"hasGoogleServiceAccountKey"`
	CreatedAt                  string `json:"createdAt"`
	UpdatedAt                  string `json:"updatedAt"`
}

type CredentialsService struct {
	repo        CredentialsRepository
	identifiers AppIdentifierRepository
	// onAuditEvent is the audit emission seam; nil (community) means
	// credential changes leave no events.
	onAuditEvent auditlog.RecordFunc
}

// NewCredentialsService builds the service; nil repos (stateless mode) make
// every method answer ErrNotSupportedInStatelessMode.
func NewCredentialsService(repo CredentialsRepository, identifiers AppIdentifierRepository) *CredentialsService {
	return &CredentialsService{
		repo:        repo,
		identifiers: identifiers,
	}
}

// SetOnAuditEvent plugs the audit emission seam. Nil-safe.
func (s *CredentialsService) SetOnAuditEvent(record auditlog.RecordFunc) {
	s.onAuditEvent = record
}

// androidCredentialAAD binds each sealed field to the identifier owning it,
// so a blob copied onto another row fails to unseal (see keyStore.AppKeyAAD).
func androidCredentialAAD(identifierId string, field string) []byte {
	return []byte(identifierId + "|android_credentials|" + field)
}

// resolveAndroidIdentifier maps (app, identifier id) to the identifier row,
// refusing unknown ids and non-android platforms.
func (s *CredentialsService) resolveAndroidIdentifier(ctx context.Context, appId string, identifierId string) (*store.AppIdentifierRef, error) {
	if s.repo == nil || s.identifiers == nil {
		return nil, store.ErrNotSupportedInStatelessMode
	}
	ref, err := s.identifiers.GetAppIdentifierByID(ctx, appId, identifierId)
	if err != nil {
		return nil, err
	}
	if ref == nil {
		return nil, &store.ErrResourceNotFound{Resource: "app identifier", Identifier: identifierId}
	}
	if ref.Platform != PlatformAndroid {
		return nil, validation.Errorf("identifier", "identifier %q is an %s identifier, android credentials require an android one", ref.Identifier, ref.Platform)
	}
	return ref, nil
}

func (s *CredentialsService) SaveAndroidCredentials(ctx context.Context, appId string, identifierId string, input AndroidCredentialsInput) error {
	ref, err := s.resolveAndroidIdentifier(ctx, appId, identifierId)
	if err != nil {
		return err
	}
	// Bind every field to the canonical database ID, independent of the UUID
	// spelling accepted by the HTTP handler.
	identifierId = ref.Id
	if input.KeyAlias == "" || len(input.KeyAlias) > 255 {
		return validation.Errorf("keyAlias", "key alias must be between 1 and 255 characters")
	}
	if input.KeystorePassword == "" {
		return validation.Errorf("keystorePassword", "keystore password is empty")
	}
	if input.KeyPassword == "" {
		return validation.Errorf("keyPassword", "key password is empty")
	}
	keystore, err := base64.StdEncoding.DecodeString(input.KeystoreBase64)
	if err != nil {
		return validation.Errorf("keystore", "keystore is not valid base64")
	}
	if len(keystore) == 0 {
		return validation.Errorf("keystore", "keystore is empty")
	}
	if len(keystore) > maxKeystoreBytes {
		return validation.Errorf("keystore", "keystore exceeds the %d KB limit", maxKeystoreBytes/1024)
	}
	if err := android.ValidateKeystore(keystore, input.KeystorePassword, input.KeyPassword, input.KeyAlias); err != nil {
		return err
	}
	if input.GoogleServiceAccountKeyJSON != "" && !json.Valid([]byte(input.GoogleServiceAccountKeyJSON)) {
		return validation.Errorf("googleServiceAccountKey", "google service account key is not valid JSON")
	}

	masterKey := []byte(keyStore.ReadDBKeysMasterKey())
	sealedKeystore, err := crypto.SealAESGCM(keystore, masterKey, androidCredentialAAD(identifierId, "keystore"))
	if err != nil {
		return fmt.Errorf("failed to seal keystore: %w", err)
	}
	sealedKeystorePassword, err := crypto.SealAESGCM([]byte(input.KeystorePassword), masterKey, androidCredentialAAD(identifierId, "keystore_password"))
	if err != nil {
		return fmt.Errorf("failed to seal keystore password: %w", err)
	}
	sealedKeyPassword, err := crypto.SealAESGCM([]byte(input.KeyPassword), masterKey, androidCredentialAAD(identifierId, "key_password"))
	if err != nil {
		return fmt.Errorf("failed to seal key password: %w", err)
	}
	sealed := store.SealedAndroidCredentials{
		KeyAlias:               input.KeyAlias,
		SealedKeystore:         sealedKeystore,
		SealedKeystorePassword: sealedKeystorePassword,
		SealedKeyPassword:      sealedKeyPassword,
	}
	if input.GoogleServiceAccountKeyJSON != "" {
		sealedGSA, err := crypto.SealAESGCM([]byte(input.GoogleServiceAccountKeyJSON), masterKey, androidCredentialAAD(identifierId, "google_service_account_key"))
		if err != nil {
			return fmt.Errorf("failed to seal google service account key: %w", err)
		}
		sealed.SealedGoogleServiceAccountKey = &sealedGSA
	}

	if err := s.repo.UpsertAndroidCredentials(ctx, identifierId, sealed); err != nil {
		return err
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionAndroidCredentialsSaved,
		TargetType:    "android_credentials",
		TargetID:      identifierId,
		TargetDisplay: ref.Identifier,
		AppID:         appId,
		Metadata: map[string]any{
			"identifier":                     ref.Identifier,
			"key_alias":                      input.KeyAlias,
			"has_google_service_account_key": input.GoogleServiceAccountKeyJSON != "",
		},
	})
	return nil
}

// GetAndroidCredentialsMetadata returns (nil, nil) when the identifier has no
// credentials configured yet.
func (s *CredentialsService) GetAndroidCredentialsMetadata(ctx context.Context, appId string, identifierId string) (*AndroidCredentialsMetadata, error) {
	ref, err := s.resolveAndroidIdentifier(ctx, appId, identifierId)
	if err != nil {
		return nil, err
	}
	credentials, err := s.repo.GetAndroidCredentials(ctx, identifierId)
	if err != nil || credentials == nil {
		return nil, err
	}
	return &AndroidCredentialsMetadata{
		Identifier:                 ref.Identifier,
		KeyAlias:                   credentials.KeyAlias,
		HasGoogleServiceAccountKey: credentials.SealedGoogleServiceAccountKey != nil,
		CreatedAt:                  credentials.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:                  credentials.UpdatedAt.UTC().Format(time.RFC3339),
	}, nil
}

func (s *CredentialsService) DeleteAndroidCredentials(ctx context.Context, appId string, identifierId string) error {
	ref, err := s.resolveAndroidIdentifier(ctx, appId, identifierId)
	if err != nil {
		return err
	}
	if err := s.repo.DeleteAndroidCredentials(ctx, identifierId); err != nil {
		return err
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionAndroidCredentialsDeleted,
		TargetType:    "android_credentials",
		TargetID:      identifierId,
		TargetDisplay: ref.Identifier,
		AppID:         appId,
	})
	return nil
}
