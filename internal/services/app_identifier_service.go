package services

import (
	"context"
	"regexp"
	"time"
	"xprem/internal/auditlog"
	"xprem/internal/store"
	"xprem/internal/validation"
)

const (
	PlatformAndroid = "android"
	PlatformIOS     = "ios"
)

var androidPackagePattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*(\.[a-zA-Z][a-zA-Z0-9_]*)+$`)
var iosBundleIdPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]*$`)

type AppIdentifierRepository interface {
	InsertAppIdentifier(ctx context.Context, appId string, platform string, identifier string) (string, error)
	GetAppIdentifiers(ctx context.Context, appId string) ([]store.AppIdentifierRow, error)
	GetAppIdentifierByID(ctx context.Context, appId string, identifierId string) (*store.AppIdentifierRef, error)
	DeleteAppIdentifier(ctx context.Context, appId string, identifierId string) error
}

// AppIdentifier is the dashboard projection of one store identity.
type AppIdentifier struct {
	Id                    string `json:"id"`
	Platform              string `json:"platform"`
	Identifier            string `json:"identifier"`
	HasAndroidCredentials bool   `json:"hasAndroidCredentials"`
	CreatedAt             string `json:"createdAt"`
}

type AppIdentifierService struct {
	repo AppIdentifierRepository
	// onAuditEvent is the audit emission seam; nil (community) means
	// identifier changes leave no events.
	onAuditEvent auditlog.RecordFunc
}

// NewAppIdentifierService builds the service; a nil repo (stateless mode)
// makes every method answer ErrNotSupportedInStatelessMode.
func NewAppIdentifierService(repo AppIdentifierRepository) *AppIdentifierService {
	return &AppIdentifierService{
		repo: repo,
	}
}

// SetOnAuditEvent plugs the audit emission seam. Nil-safe.
func (s *AppIdentifierService) SetOnAuditEvent(record auditlog.RecordFunc) {
	s.onAuditEvent = record
}

func validateIdentifier(platform string, identifier string) error {
	if len(identifier) > 255 {
		return validation.Errorf("identifier", "identifier must be at most 255 characters")
	}
	switch platform {
	case PlatformAndroid:
		if !androidPackagePattern.MatchString(identifier) {
			return validation.Errorf("identifier", "%q is not a valid Android application id", identifier)
		}
	case PlatformIOS:
		if !iosBundleIdPattern.MatchString(identifier) {
			return validation.Errorf("identifier", "%q is not a valid iOS bundle identifier", identifier)
		}
	default:
		return validation.Errorf("platform", "platform must be %q or %q", PlatformAndroid, PlatformIOS)
	}
	return nil
}

func (s *AppIdentifierService) CreateAppIdentifier(ctx context.Context, appId string, platform string, identifier string) (string, error) {
	if s.repo == nil {
		return "", store.ErrNotSupportedInStatelessMode
	}
	if err := validateIdentifier(platform, identifier); err != nil {
		return "", err
	}
	identifierId, err := s.repo.InsertAppIdentifier(ctx, appId, platform, identifier)
	if err != nil {
		return "", err
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionAppIdentifierCreated,
		TargetType:    "app_identifier",
		TargetID:      identifierId,
		TargetDisplay: identifier,
		AppID:         appId,
		Metadata:      map[string]any{"platform": platform},
	})
	return identifierId, nil
}

func (s *AppIdentifierService) GetAppIdentifiers(ctx context.Context, appId string) ([]AppIdentifier, error) {
	if s.repo == nil {
		return nil, store.ErrNotSupportedInStatelessMode
	}
	rows, err := s.repo.GetAppIdentifiers(ctx, appId)
	if err != nil {
		return nil, err
	}
	identifiers := make([]AppIdentifier, len(rows))
	for i, row := range rows {
		identifiers[i] = AppIdentifier{
			Id:                    row.Id,
			Platform:              row.Platform,
			Identifier:            row.Identifier,
			HasAndroidCredentials: row.HasAndroidCredentials,
			CreatedAt:             row.CreatedAt.UTC().Format(time.RFC3339),
		}
	}
	return identifiers, nil
}

func (s *AppIdentifierService) DeleteAppIdentifier(ctx context.Context, appId string, identifierId string) error {
	if s.repo == nil {
		return store.ErrNotSupportedInStatelessMode
	}
	// Read before the delete: afterwards there is no row left to name in the
	// audit entry. Best-effort, like the entry itself.
	displayName := identifierId
	var platform string
	if ref, err := s.repo.GetAppIdentifierByID(ctx, appId, identifierId); err == nil && ref != nil {
		displayName = ref.Identifier
		platform = ref.Platform
	}
	if err := s.repo.DeleteAppIdentifier(ctx, appId, identifierId); err != nil {
		return err
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionAppIdentifierDeleted,
		TargetType:    "app_identifier",
		TargetID:      identifierId,
		TargetDisplay: displayName,
		AppID:         appId,
		Metadata:      map[string]any{"platform": platform},
	})
	return nil
}
