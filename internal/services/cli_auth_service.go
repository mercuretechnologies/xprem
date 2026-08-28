package services

import (
	"context"
	"fmt"
	"strconv"
	"xprem/internal/auditlog"
	"xprem/internal/crypto"
	"xprem/internal/types"
	"xprem/internal/validation"
)

var ErrUnauthorized = fmt.Errorf("unauthorized")

// ErrCliAccessDenied marks a CLI request whose credential authenticated fine
// but is rejected by per-key access restrictions (enterprise). Handlers map
// it to a 403 with the wrapped reason, instead of the generic auth 401.
var ErrCliAccessDenied = fmt.Errorf("access denied")

// ErrCliAuthUnavailable marks a CLI request that could not be JUDGED, as
// opposed to one that was judged and refused: the control plane was
// unreachable while resolving what the key is allowed to do. Handlers map it
// to a 500, the same way NewAuthMiddleware refuses to let ErrAuthUnavailable
// read as a dead dashboard session. Without it a database blip tells a CI job
// its key is invalid, and the remediation an operator reaches for is rotating
// a key that was never the problem.
var ErrCliAuthUnavailable = fmt.Errorf("could not verify this credential")

// CliAuthRepository validates the credential a CLI client presents for an app,
// and stores the API keys that back it. ValidateCliCredential means a different
// thing per mode, which is why it is the repository's job and not the service's:
//   - Postgres (DB mode): hashes the presented eoo_ key and looks it up in the
//     api_keys table, scoped to appId. Returns the matched key's id.
//   - Bucket (stateless mode): no API keys exist; the credential is an Expo
//     token or session, verified against the Expo API. Returns 0 as the id.
type CliAuthRepository interface {
	ValidateCliCredential(ctx context.Context, appId string, auth types.Auth) (int64, error)
	InsertApiKey(ctx context.Context, appId string, name string, hint string, hashedKey string) (int64, error)
	GetApiKeysMetadataByAppID(ctx context.Context, appId string) ([]types.ApiKeyMetadata, error)
	// GetApiKeyNameByID feeds the audit actor display; RevokeApiKeyByID
	// returns the revoked key's name so the audit entry needs no second read.
	GetApiKeyNameByID(ctx context.Context, appId string, apiKeyId int64) (string, error)
	RevokeApiKeyByID(ctx context.Context, apiKeyId int64, appId string) (string, error)
}

// CliAuthService authenticates CLI clients (eoas) against a given app and
// manages the API keys backing that access in DB mode. The dashboard's own
// login and session tokens are a separate concern; see DashboardAuthService.
//
// It answers "is this credential real, and for this app", and stops there.
// What the credential may then DO is decided in the router, which is the only
// layer holding the route, its branch and its action at once; the enterprise
// implementation of that decision lives in ee/apikeyrestrictions.
type CliAuthService struct {
	authRepo CliAuthRepository
	// onAuditEvent is the audit emission seam; nil (community) means key
	// changes leave no events.
	onAuditEvent auditlog.RecordFunc
	// auditActive reports whether audit events are being collected right now
	// (the enterprise wiring injects auditService.Enabled). It only gates the
	// key-name lookup that enriches the audit actor display: nil or false
	// means no lookup, never any security behavior change.
	auditActive func() bool
}

func NewCliAuthService(authRepo CliAuthRepository) *CliAuthService {
	return &CliAuthService{authRepo: authRepo}
}

// SetAuditActive plugs the live "audit is collecting" signal. Nil-safe.
func (s *CliAuthService) SetAuditActive(active func() bool) {
	s.auditActive = active
}

// SetOnAuditEvent plugs the audit emission seam (see SetSSOEnforced for the
// pattern). Nil-safe.
func (s *CliAuthService) SetOnAuditEvent(record auditlog.RecordFunc) {
	s.onAuditEvent = record
}

// AuthenticateCliCredential resolves the credential against the app. A key id
// of 0 means stateless mode, where no API key exists to carry access rules.
func (s *CliAuthService) AuthenticateCliCredential(ctx context.Context, appId string, auth types.Auth) (CliCredential, error) {
	apiKeyID, err := s.authRepo.ValidateCliCredential(ctx, appId, auth)
	if err != nil {
		return CliCredential{}, fmt.Errorf("failed to validate auth: %w", err)
	}
	credential := CliCredential{AppID: appId, KeyID: apiKeyID}
	if apiKeyID != 0 {
		// The name only serves the audit trail's actor display: skipped
		// entirely while nothing is collected (community edition never pays
		// the lookup), and best-effort when it runs, an unreadable key list
		// must not fail a valid credential.
		if s.auditActive != nil && s.auditActive() {
			if name, nameErr := s.authRepo.GetApiKeyNameByID(ctx, appId, apiKeyID); nameErr == nil {
				credential.KeyName = name
			}
		}
	}
	return credential, nil
}

func (s *CliAuthService) GenerateAPIKey(ctx context.Context, appId string, name string) (string, error) {
	if err := validation.DisplayName("name", name); err != nil {
		return "", err
	}
	apiKey, err := crypto.GenerateAPIKey()
	if err != nil {
		return "", fmt.Errorf("failed to generate API key: %w", err)
	}
	hashedKey, err := crypto.HashPlaintextAPIKey(apiKey)
	if err != nil {
		return "", fmt.Errorf("failed to hash API key: %w", err)
	}
	lastFour := apiKey[len(apiKey)-4:]
	hint := fmt.Sprintf("%s*******%s", crypto.PrefixActive, lastFour)
	// Store only the hashed version of the API key in the database for security reasons
	keyID, err := s.authRepo.InsertApiKey(ctx, appId, name, hint, hashedKey)
	if err != nil {
		return "", fmt.Errorf("failed to insert API key into database: %w", err)
	}
	// The hint is the shape shown in the dashboard, never key material.
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionAPIKeyCreated,
		TargetType:    "api_key",
		TargetID:      strconv.FormatInt(keyID, 10),
		TargetDisplay: name,
		AppID:         appId,
		Metadata:      map[string]any{"hint": hint},
	})
	return apiKey, nil
}

func (s *CliAuthService) GetApiKeysMetadata(ctx context.Context, appId string) ([]types.ApiKeyMetadata, error) {
	apiKeysMetadata, err := s.authRepo.GetApiKeysMetadataByAppID(ctx, appId)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve API keys metadata from database: %w", err)
	}
	return apiKeysMetadata, nil
}

func (s *CliAuthService) RevokeApiKey(ctx context.Context, appId string, apiKeyId string) error {
	if err := validation.NumericID("apiKeyId", apiKeyId); err != nil {
		return err
	}
	apiKeyIdInt, err := strconv.ParseInt(apiKeyId, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid API key ID: %w", err)
	}
	keyName, err := s.authRepo.RevokeApiKeyByID(ctx, apiKeyIdInt, appId)
	if err != nil {
		return err
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionAPIKeyRevoked,
		TargetType:    "api_key",
		TargetID:      apiKeyId,
		TargetDisplay: keyName,
		AppID:         appId,
	})
	return nil
}
