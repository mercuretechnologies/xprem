package services

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"time"
	"xprem/internal/auditlog"
	"xprem/internal/crypto"
	"xprem/internal/keyStore"
	"xprem/internal/store"
	"xprem/internal/validation"
)

// maxKeystoreBytes caps the decoded keystore blob; real-world .jks files are
// a few kilobytes, anything near the cap is not a keystore.
const maxKeystoreBytes = 512 * 1024

var androidPackagePattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*(\.[a-zA-Z][a-zA-Z0-9_]*)+$`)

type CredentialsRepository interface {
	UpsertAndroidCredentials(ctx context.Context, appId string, credentials store.SealedAndroidCredentials) error
	GetAndroidCredentials(ctx context.Context, appId string) (*store.SealedAndroidCredentials, error)
	DeleteAndroidCredentials(ctx context.Context, appId string) error
}

// AndroidCredentialsInput is one full replacement of an app's Android signing
// credentials, as received from the dashboard or the CLI.
type AndroidCredentialsInput struct {
	AndroidPackage              string
	KeyAlias                    string
	KeystoreBase64              string
	KeystorePassword            string
	KeyPassword                 string
	GoogleServiceAccountKeyJSON string
}

// AndroidCredentialsMetadata is the non-secret projection served to viewers.
type AndroidCredentialsMetadata struct {
	AndroidPackage             string `json:"androidPackage"`
	KeyAlias                   string `json:"keyAlias"`
	HasGoogleServiceAccountKey bool   `json:"hasGoogleServiceAccountKey"`
	CreatedAt                  string `json:"createdAt"`
	UpdatedAt                  string `json:"updatedAt"`
}

type CredentialsService struct {
	repo CredentialsRepository
	// onAuditEvent is the audit emission seam; nil (community) means
	// credential changes leave no events.
	onAuditEvent auditlog.RecordFunc
}

// NewCredentialsService builds the service; a nil repo (stateless mode) makes
// every method answer ErrNotSupportedInStatelessMode.
func NewCredentialsService(repo CredentialsRepository) *CredentialsService {
	return &CredentialsService{
		repo: repo,
	}
}

// SetOnAuditEvent plugs the audit emission seam. Nil-safe.
func (s *CredentialsService) SetOnAuditEvent(record auditlog.RecordFunc) {
	s.onAuditEvent = record
}

// androidCredentialAAD binds each sealed field to the app it belongs to, so a
// blob copied onto another row fails to unseal (see keyStore.AppKeyAAD).
func androidCredentialAAD(appId string, field string) []byte {
	return []byte(appId + "|android_credentials|" + field)
}

func (s *CredentialsService) SaveAndroidCredentials(ctx context.Context, appId string, input AndroidCredentialsInput) error {
	if s.repo == nil {
		return store.ErrNotSupportedInStatelessMode
	}
	if !androidPackagePattern.MatchString(input.AndroidPackage) || len(input.AndroidPackage) > 255 {
		return validation.Errorf("androidPackage", "%q is not a valid Android application id", input.AndroidPackage)
	}
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
	if input.GoogleServiceAccountKeyJSON != "" && !json.Valid([]byte(input.GoogleServiceAccountKeyJSON)) {
		return validation.Errorf("googleServiceAccountKey", "google service account key is not valid JSON")
	}

	masterKey := []byte(keyStore.ReadDBKeysMasterKey())
	sealedKeystore, err := crypto.SealAESGCM(keystore, masterKey, androidCredentialAAD(appId, "keystore"))
	if err != nil {
		return fmt.Errorf("failed to seal keystore: %w", err)
	}
	sealedKeystorePassword, err := crypto.SealAESGCM([]byte(input.KeystorePassword), masterKey, androidCredentialAAD(appId, "keystore_password"))
	if err != nil {
		return fmt.Errorf("failed to seal keystore password: %w", err)
	}
	sealedKeyPassword, err := crypto.SealAESGCM([]byte(input.KeyPassword), masterKey, androidCredentialAAD(appId, "key_password"))
	if err != nil {
		return fmt.Errorf("failed to seal key password: %w", err)
	}
	sealed := store.SealedAndroidCredentials{
		AndroidPackage:         input.AndroidPackage,
		KeyAlias:               input.KeyAlias,
		SealedKeystore:         sealedKeystore,
		SealedKeystorePassword: sealedKeystorePassword,
		SealedKeyPassword:      sealedKeyPassword,
	}
	if input.GoogleServiceAccountKeyJSON != "" {
		sealedGSA, err := crypto.SealAESGCM([]byte(input.GoogleServiceAccountKeyJSON), masterKey, androidCredentialAAD(appId, "google_service_account_key"))
		if err != nil {
			return fmt.Errorf("failed to seal google service account key: %w", err)
		}
		sealed.SealedGoogleServiceAccountKey = &sealedGSA
	}

	if err := s.repo.UpsertAndroidCredentials(ctx, appId, sealed); err != nil {
		return err
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionAndroidCredentialsSaved,
		TargetType:    "android_credentials",
		TargetID:      appId,
		TargetDisplay: input.AndroidPackage,
		AppID:         appId,
		Metadata: map[string]any{
			"android_package":                input.AndroidPackage,
			"key_alias":                      input.KeyAlias,
			"has_google_service_account_key": input.GoogleServiceAccountKeyJSON != "",
		},
	})
	return nil
}

// GetAndroidCredentialsMetadata returns (nil, nil) when the app has no
// Android credentials configured.
func (s *CredentialsService) GetAndroidCredentialsMetadata(ctx context.Context, appId string) (*AndroidCredentialsMetadata, error) {
	if s.repo == nil {
		return nil, store.ErrNotSupportedInStatelessMode
	}
	credentials, err := s.repo.GetAndroidCredentials(ctx, appId)
	if err != nil || credentials == nil {
		return nil, err
	}
	return &AndroidCredentialsMetadata{
		AndroidPackage:             credentials.AndroidPackage,
		KeyAlias:                   credentials.KeyAlias,
		HasGoogleServiceAccountKey: credentials.SealedGoogleServiceAccountKey != nil,
		CreatedAt:                  credentials.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:                  credentials.UpdatedAt.UTC().Format(time.RFC3339),
	}, nil
}

func (s *CredentialsService) DeleteAndroidCredentials(ctx context.Context, appId string) error {
	if s.repo == nil {
		return store.ErrNotSupportedInStatelessMode
	}
	// Read before the delete: afterwards there is no row left to name in the
	// audit entry. Best-effort, like the entry itself.
	displayName := appId
	if credentials, err := s.repo.GetAndroidCredentials(ctx, appId); err == nil && credentials != nil {
		displayName = credentials.AndroidPackage
	}
	if err := s.repo.DeleteAndroidCredentials(ctx, appId); err != nil {
		return err
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionAndroidCredentialsDeleted,
		TargetType:    "android_credentials",
		TargetID:      appId,
		TargetDisplay: displayName,
		AppID:         appId,
	})
	return nil
}
