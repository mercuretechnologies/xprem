package services

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math/big"
	"time"
	"xprem/config"
	"xprem/internal/auditlog"
	"xprem/internal/crypto"
	"xprem/internal/helpers"
	"xprem/internal/keyStore"
	"xprem/internal/providers/expo"
	"xprem/internal/store"
	"xprem/internal/validation"

	"github.com/google/uuid"
)

type AppService struct {
	appRepo AppRepository
	// onAuditEvent is the audit emission seam; nil (community) means app
	// changes leave no events.
	onAuditEvent auditlog.RecordFunc
}

type AppRepository interface {
	InsertApp(ctx context.Context, app store.InsertAppParameters) (string, error)
	DeleteAppByID(ctx context.Context, id string) error
	GetApps(ctx context.Context) ([]config.AppDescriptor, error)
	UpdateAppNameByID(ctx context.Context, id string, newName string) error
	GetAppByID(ctx context.Context, id string) (config.AppConfig, error)
}

func NewAppService(appRepo AppRepository) *AppService {
	return &AppService{
		appRepo: appRepo,
	}
}

// SetOnAuditEvent plugs the audit emission seam (see SetSSOEnforced for the
// pattern). Nil-safe.
func (s *AppService) SetOnAuditEvent(record auditlog.RecordFunc) {
	s.onAuditEvent = record
}

func (s *AppService) CreateApp(ctx context.Context, displayName string, keysConfig config.KeysConfig) (string, error) {
	return s.createApp(ctx, uuid.New(), displayName, keysConfig)
}

// Insert rejects a colliding id.
func (s *AppService) CreateAppWithId(ctx context.Context, appId uuid.UUID, displayName string, keysConfig config.KeysConfig) (string, error) {
	return s.createApp(ctx, appId, displayName, keysConfig)
}

func (s *AppService) createApp(ctx context.Context, appId uuid.UUID, displayName string, keysConfig config.KeysConfig) (string, error) {
	if err := validation.DisplayName("name", displayName); err != nil {
		return "", err
	}
	// Apps are only ever created through the control plane, the bucket store
	// rejects InsertApp, so creation always happens at runtime, from the
	// dashboard, which offers only database and aws-secrets-manager. Neither
	// legacy mode can carry usable key material for a new app: local key paths
	// would have to already exist on every replica and cannot be provisioned
	// from the UI, and the apps table has no column for an inline b64 key, so an
	// environment-mode app would persist nothing and fail at its first manifest
	// signature, unrepairably, since UpdateApp only renames. Reject both up
	// front rather than let them fall through to a 201 and a broken app.
	//
	// This deliberately lives here and not in config.ValidateKeys: the infra->DB
	// migration loads the legacy flat-env app (which may legitimately use local
	// key files or env b64 keys) through that validator and must keep working -
	// it seals such keys into mode=database instead.
	if keysConfig.Mode == config.KeysModeLocal || keysConfig.Mode == config.KeysModeEnvironment {
		return "", validation.Errorf("keysConfig.mode",
			"%q is not supported when creating an app: it cannot be provisioned from the dashboard, use %q or %q",
			keysConfig.Mode, config.KeysModeDatabase, config.KeysModeAWSSM)
	}
	if err := config.ValidateKeys(&keysConfig, "keysConfig"); err != nil {
		// Surface as a validation error so the handler answers 400, not 500.
		return "", validation.Errorf("keysConfig", "%v", err)
	}
	modeStr := string(keysConfig.Mode)
	params := store.InsertAppParameters{
		ID:       appId.String(),
		Name:     displayName,
		KeysMode: &modeStr,
	}
	switch keysConfig.Mode {
	case config.KeysModeDatabase:
		masterKeyStr := keyStore.ReadDBKeysMasterKey()
		if masterKeyStr == "" {
			return "", fmt.Errorf("master key is required for database keys mode")
		}
		masterKeyBytes := []byte(masterKeyStr)
		if masterKeyBytes == nil {
			return "", fmt.Errorf("invalid base64 configuration for master key")
		}
		if len(masterKeyBytes) != 32 {
			return "", fmt.Errorf("decoded master key must be exactly 32 bytes (got %d)", len(masterKeyBytes))
		}
		pubPEM, privPEM, err := crypto.GenerateRSAKeyPair()
		if err != nil {
			return "", fmt.Errorf("failed to generate application signing keys: %w", err)
		}
		// Sealed under this app id, which is also the id the row is inserted
		// with, so the binding is checked at unseal against the row the blob
		// was actually read from. See keyStore.AppKeyAAD.
		sealedPublicKey, err := crypto.SealAESGCM([]byte(pubPEM), masterKeyBytes, keyStore.AppKeyAAD(appId.String(), true))
		if err != nil {
			return "", fmt.Errorf("failed to seal public key: %w", err)
		}
		sealedPrivateKey, err := crypto.SealAESGCM([]byte(privPEM), masterKeyBytes, keyStore.AppKeyAAD(appId.String(), false))
		if err != nil {
			return "", fmt.Errorf("failed to seal private key: %w", err)
		}
		params.SealedPublicKey = &sealedPublicKey
		params.SealedPrivateKey = &sealedPrivateKey

	case config.KeysModeAWSSM:
		params.AwsSecretIDPublic = &keysConfig.PublicSecretId
		params.AwsSecretIDPrivate = &keysConfig.PrivateSecretId

	default:
		return "", fmt.Errorf("invalid keys mode %q", keysConfig.Mode)
	}

	insertedAppId, err := s.appRepo.InsertApp(ctx, params)
	if err != nil {
		return "", fmt.Errorf("failed to save app record to database: %w", err)
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionAppCreated,
		TargetType:    "app",
		TargetID:      insertedAppId,
		TargetDisplay: displayName,
		AppID:         insertedAppId,
		Metadata:      map[string]any{"keys_mode": modeStr},
	})
	return insertedAppId, nil
}

func (s *AppService) DeleteApp(ctx context.Context, app config.AppConfig) error {
	if err := s.appRepo.DeleteAppByID(ctx, app.Id); err != nil {
		return err
	}
	displayName := app.Name
	if displayName == "" {
		displayName = app.Id
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionAppDeleted,
		TargetType:    "app",
		TargetID:      app.Id,
		TargetDisplay: displayName,
		AppID:         app.Id,
	})
	return nil
}

func (s *AppService) GetApps(ctx context.Context) ([]config.AppDescriptor, error) {
	apps, err := s.appRepo.GetApps(ctx)
	if err != nil {
		return nil, err
	}
	for i := range apps {
		apps[i].RepositoryUrl = resolveRepositoryUrl(apps[i].RepositoryUrl)
	}
	return apps, nil
}

// resolveRepositoryUrl fills in the repository for an app that carries none.
// The control plane has no apps.repository_url column yet, so its rows fall back
// to the server-wide env var: right for one repo, wrong for a plane hosting many.
func resolveRepositoryUrl(configured string) string {
	if configured != "" {
		return configured
	}
	return config.RepositoryURL()
}

func (s *AppService) GetAppByID(ctx context.Context, appId string) (config.AppConfig, error) {
	app, err := s.appRepo.GetAppByID(ctx, appId)
	if err != nil {
		return config.AppConfig{}, err
	}
	return s.PresentApp(ctx, app), nil
}

// PresentApp is the dashboard view of an app: secrets stripped, key paths
// masked, display name resolved.
func (s *AppService) PresentApp(ctx context.Context, app config.AppConfig) config.AppConfig {
	app.AccessToken = ""
	app.Keys.SealedPrivateKey = ""
	app.Keys.SealedPublicKey = ""
	app.Keys.PrivateB64 = ""
	app.Keys.PublicB64 = ""
	if app.Keys.Mode == config.KeysModeLocal {
		app.Keys.PrivatePath = helpers.MaskKeyPath(app.Keys.PrivatePath)
		app.Keys.PublicPath = helpers.MaskKeyPath(app.Keys.PublicPath)
	}
	if app.Name == "" {
		// The stateless flat env carries no display name, resolve it from
		// Expo for the dashboard. Best-effort and cached; "" keeps the
		// id-as-label fallback. Lives here rather than in the store so the
		// device-facing OTA path never pays the Expo round-trip.
		app.Name = expo.FetchAppName(ctx, app.Id)
	}
	// Same fallback the listing applies, so /api/apps/{id} and /api/apps agree.
	app.RepositoryUrl = resolveRepositoryUrl(app.RepositoryUrl)
	return app
}

func (s *AppService) UpdateApp(ctx context.Context, app config.AppConfig, newName string) error {
	if err := validation.DisplayName("name", newName); err != nil {
		return err
	}
	if err := s.appRepo.UpdateAppNameByID(ctx, app.Id, newName); err != nil {
		return err
	}
	// An idempotent rename is not a change: no event.
	if app.Name == newName {
		return nil
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionAppRenamed,
		TargetType:    "app",
		TargetID:      app.Id,
		TargetDisplay: newName,
		AppID:         app.Id,
		Metadata:      map[string]any{"name": newName, "previous_name": app.Name},
	})
	return nil
}

func (s *AppService) RetrieveAppCertificate(ctx context.Context, appId string) (string, error) {
	app, err := s.appRepo.GetAppByID(ctx, appId)
	if err != nil {
		return "", err
	}
	return s.CertificateFor(ctx, app)
}

// CertificateFor wraps the app's public key in a self-signed code-signing
// certificate.
func (s *AppService) CertificateFor(ctx context.Context, app config.AppConfig) (string, error) {
	if app.Keys.Mode != config.KeysModeDatabase {
		return "", fmt.Errorf("app with id %s does not use database keys mode", app.Id)
	}
	publicKey := keyStore.GetPublicExpoKey(app)
	privateKey := keyStore.GetPrivateExpoKey(app)
	// Deterministic serial number by hashing the public key so it never changes
	hash := sha256.Sum256([]byte(publicKey))
	serialNumber := new(big.Int).SetBytes(hash[:8])

	// 2. Deterministic Validity: Use the database app creation timestamp
	// Convert your 13-digit millisecond timestamp safely
	notBefore := time.UnixMilli(int64(app.CreatedAt)).UTC().Add(-1 * time.Hour)
	pemCertificateString, err := crypto.GenerateSelfSignedCodeSigningCertificateFromPEM(publicKey, privateKey, app.Name, serialNumber, notBefore)
	if err != nil {
		return "", fmt.Errorf("failed to wrap public key in self-signed certificate: %w", err)
	}
	// The one read we audit: key material access, behind its own permission.
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionCertificateDownloaded,
		TargetType:    "app",
		TargetID:      app.Id,
		TargetDisplay: app.Name,
		AppID:         app.Id,
	})
	return pemCertificateString, nil
}
