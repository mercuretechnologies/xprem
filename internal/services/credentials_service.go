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
	"xprem/internal/repository"
	"xprem/internal/types"
	"xprem/internal/validation"
)

// maxKeystoreBytes caps the decoded keystore blob; real-world .jks files are
// a few kilobytes, anything near the cap is not a keystore.
const maxKeystoreBytes = 512 * 1024

type CredentialsRepository interface {
	// UpsertAndroidCredentials changes only the signing material on conflict;
	// the separately managed Google Play service account key is preserved.
	UpsertAndroidCredentials(ctx context.Context, identifierId string, credentials repository.SealedAndroidCredentials) error
	GetAndroidCredentials(ctx context.Context, identifierId string) (*repository.SealedAndroidCredentials, error)
	UpdateGooglePlayServiceAccountKey(ctx context.Context, identifierId string, sealedKey, email, projectID *string) error
	DeleteAndroidCredentials(ctx context.Context, identifierId string) error
}

// AndroidCredentialsInput is one full replacement of an identifier's Android
// signing credentials, as received from the dashboard or the CLI.
type AndroidCredentialsInput struct {
	KeyAlias         string
	KeystoreBase64   string
	KeystorePassword string
	KeyPassword      string
}

// AndroidCredentialsMetadata is the non-secret projection served to viewers.
type AndroidCredentialsMetadata struct {
	Identifier                    string `json:"identifier"`
	KeyAlias                      string `json:"keyAlias"`
	HasGoogleServiceAccountKey    bool   `json:"hasGoogleServiceAccountKey"`
	GoogleServiceAccountEmail     string `json:"googleServiceAccountEmail,omitempty"`
	GoogleServiceAccountProjectID string `json:"googleServiceAccountProjectId,omitempty"`
	CreatedAt                     string `json:"createdAt"`
	UpdatedAt                     string `json:"updatedAt"`
}

type googlePlayServiceAccountKey struct {
	Type        string `json:"type"`
	ProjectID   string `json:"project_id"`
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
}

// AndroidKeystoreExport is returned only by the permission-protected export
// endpoint. It deliberately excludes the Google Play service account key.
type AndroidKeystoreExport struct {
	Keystore         []byte
	CertificatePEM   []byte
	KeystorePassword string
	KeyAlias         string
	KeyPassword      string
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
func (s *CredentialsService) resolveAndroidIdentifier(ctx context.Context, appId string, identifierId string) (*repository.AppIdentifierRef, error) {
	if s.repo == nil || s.identifiers == nil {
		return nil, repository.ErrNotSupportedInStatelessMode
	}
	ref, err := s.identifiers.GetAppIdentifierByID(ctx, appId, identifierId)
	if err != nil {
		return nil, err
	}
	if ref == nil {
		return nil, &repository.ErrResourceNotFound{Resource: "app identifier", Identifier: identifierId}
	}
	if ref.Platform != types.PlatformAndroid {
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
	sealed, err := sealAndroidKeystore(identifierId, input.KeyAlias, keystore, input.KeystorePassword, input.KeyPassword)
	if err != nil {
		return err
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
			"identifier": ref.Identifier,
			"key_alias":  input.KeyAlias,
		},
	})
	return nil
}

func sealAndroidKeystore(identifierId, keyAlias string, keystore []byte, keystorePassword, keyPassword string) (repository.SealedAndroidCredentials, error) {
	masterKey := []byte(keyStore.ReadDBKeysMasterKey())
	sealedKeystore, err := crypto.SealAESGCM(keystore, masterKey, androidCredentialAAD(identifierId, "keystore"))
	if err != nil {
		return repository.SealedAndroidCredentials{}, fmt.Errorf("failed to seal keystore: %w", err)
	}
	sealedKeystorePassword, err := crypto.SealAESGCM([]byte(keystorePassword), masterKey, androidCredentialAAD(identifierId, "keystore_password"))
	if err != nil {
		return repository.SealedAndroidCredentials{}, fmt.Errorf("failed to seal keystore password: %w", err)
	}
	sealedKeyPassword, err := crypto.SealAESGCM([]byte(keyPassword), masterKey, androidCredentialAAD(identifierId, "key_password"))
	if err != nil {
		return repository.SealedAndroidCredentials{}, fmt.Errorf("failed to seal key password: %w", err)
	}
	return repository.SealedAndroidCredentials{
		KeyAlias:               keyAlias,
		SealedKeystore:         sealedKeystore,
		SealedKeystorePassword: sealedKeystorePassword,
		SealedKeyPassword:      sealedKeyPassword,
	}, nil
}

// GenerateAndroidCredentials creates a new upload keystore while preserving
// the separately managed Google Play service account key, if one exists.
func (s *CredentialsService) GenerateAndroidCredentials(ctx context.Context, appId string, identifierId string) error {
	ref, err := s.resolveAndroidIdentifier(ctx, appId, identifierId)
	if err != nil {
		return err
	}
	identifierId = ref.Id
	generated, err := android.GenerateKeystore(ref.Identifier)
	if err != nil {
		return err
	}
	sealed, err := sealAndroidKeystore(identifierId, generated.KeyAlias, generated.Keystore, generated.KeystorePassword, generated.KeyPassword)
	if err != nil {
		return err
	}
	if err := s.repo.UpsertAndroidCredentials(ctx, identifierId, sealed); err != nil {
		return err
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionAndroidCredentialsGenerated,
		TargetType:    "android_credentials",
		TargetID:      identifierId,
		TargetDisplay: ref.Identifier,
		AppID:         appId,
		Metadata:      map[string]any{"identifier": ref.Identifier, "key_alias": generated.KeyAlias},
	})
	return nil
}

func (s *CredentialsService) SaveGooglePlayServiceAccountKey(ctx context.Context, appId string, identifierId string, serviceAccountKeyJSON string) error {
	ref, err := s.resolveAndroidIdentifier(ctx, appId, identifierId)
	if err != nil {
		return err
	}
	var serviceAccountKey googlePlayServiceAccountKey
	if err := json.Unmarshal([]byte(serviceAccountKeyJSON), &serviceAccountKey); err != nil {
		return validation.Errorf("serviceAccountKey", "google play service account key is not valid JSON")
	}
	if serviceAccountKey.Type != "service_account" || serviceAccountKey.ProjectID == "" || serviceAccountKey.ClientEmail == "" || serviceAccountKey.PrivateKey == "" {
		return validation.Errorf("serviceAccountKey", "file is not a Google service account JSON key")
	}
	identifierId = ref.Id
	sealedKey, err := crypto.SealAESGCM(
		[]byte(serviceAccountKeyJSON),
		[]byte(keyStore.ReadDBKeysMasterKey()),
		androidCredentialAAD(identifierId, "google_service_account_key"),
	)
	if err != nil {
		return fmt.Errorf("failed to seal google play service account key: %w", err)
	}
	if err := s.repo.UpdateGooglePlayServiceAccountKey(ctx, identifierId, &sealedKey, &serviceAccountKey.ClientEmail, &serviceAccountKey.ProjectID); err != nil {
		return err
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionGooglePlayServiceAccountSaved,
		TargetType:    "google_play_service_account",
		TargetID:      identifierId,
		TargetDisplay: ref.Identifier,
		AppID:         appId,
	})
	return nil
}

func (s *CredentialsService) DeleteGooglePlayServiceAccountKey(ctx context.Context, appId string, identifierId string) error {
	ref, err := s.resolveAndroidIdentifier(ctx, appId, identifierId)
	if err != nil {
		return err
	}
	identifierId = ref.Id
	if err := s.repo.UpdateGooglePlayServiceAccountKey(ctx, identifierId, nil, nil, nil); err != nil {
		return err
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionGooglePlayServiceAccountDeleted,
		TargetType:    "google_play_service_account",
		TargetID:      identifierId,
		TargetDisplay: ref.Identifier,
		AppID:         appId,
	})
	return nil
}

// ExportAndroidKeystore decrypts only the Android signing material. The
// Google Play service account remains sealed and is never part of this export.
func (s *CredentialsService) ExportAndroidKeystore(ctx context.Context, appId string, identifierId string) (*AndroidKeystoreExport, error) {
	ref, err := s.resolveAndroidIdentifier(ctx, appId, identifierId)
	if err != nil {
		return nil, err
	}
	identifierId = ref.Id
	credentials, err := s.repo.GetAndroidCredentials(ctx, identifierId)
	if err != nil {
		return nil, err
	}
	if credentials == nil {
		return nil, &repository.ErrResourceNotFound{Resource: "android credentials", Identifier: identifierId}
	}
	masterKey := []byte(keyStore.ReadDBKeysMasterKey())
	keystore, err := crypto.UnsealAESGCM(credentials.SealedKeystore, masterKey, androidCredentialAAD(identifierId, "keystore"))
	if err != nil {
		return nil, fmt.Errorf("failed to unseal keystore: %w", err)
	}
	keystorePassword, err := crypto.UnsealAESGCM(credentials.SealedKeystorePassword, masterKey, androidCredentialAAD(identifierId, "keystore_password"))
	if err != nil {
		return nil, fmt.Errorf("failed to unseal keystore password: %w", err)
	}
	keyPassword, err := crypto.UnsealAESGCM(credentials.SealedKeyPassword, masterKey, androidCredentialAAD(identifierId, "key_password"))
	if err != nil {
		return nil, fmt.Errorf("failed to unseal key password: %w", err)
	}
	certificatePEM, err := android.SigningCertificatePEM(
		keystore,
		string(keystorePassword),
		string(keyPassword),
		credentials.KeyAlias,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to export android signing certificate: %w", err)
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionAndroidKeystoreDownloaded,
		TargetType:    "android_credentials",
		TargetID:      identifierId,
		TargetDisplay: ref.Identifier,
		AppID:         appId,
	})
	return &AndroidKeystoreExport{
		Keystore:         keystore,
		CertificatePEM:   certificatePEM,
		KeystorePassword: string(keystorePassword),
		KeyAlias:         credentials.KeyAlias,
		KeyPassword:      string(keyPassword),
	}, nil
}

// GetAndroidCredentialsMetadata returns (nil, nil) when the identifier has no
// credentials configured yet.
func (s *CredentialsService) GetAndroidCredentialsMetadata(ctx context.Context, appId string, identifierId string) (*AndroidCredentialsMetadata, error) {
	ref, err := s.resolveAndroidIdentifier(ctx, appId, identifierId)
	if err != nil {
		return nil, err
	}
	identifierId = ref.Id
	credentials, err := s.repo.GetAndroidCredentials(ctx, identifierId)
	if err != nil || credentials == nil {
		return nil, err
	}
	metadata := &AndroidCredentialsMetadata{
		Identifier:                 ref.Identifier,
		KeyAlias:                   credentials.KeyAlias,
		HasGoogleServiceAccountKey: credentials.SealedGoogleServiceAccountKey != nil,
		CreatedAt:                  credentials.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:                  credentials.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if credentials.GoogleServiceAccountEmail != nil {
		metadata.GoogleServiceAccountEmail = *credentials.GoogleServiceAccountEmail
	}
	if credentials.GoogleServiceAccountProjectID != nil {
		metadata.GoogleServiceAccountProjectID = *credentials.GoogleServiceAccountProjectID
	}
	return metadata, nil
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
