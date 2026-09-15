package services

import (
	"context"
	"math/big"
	"regexp"
	"strings"
	"time"
	"xprem/internal/auditlog"
	"xprem/internal/cache"
	"xprem/internal/dashboard"
	"xprem/internal/store"
	"xprem/internal/types"
	"xprem/internal/validation"
)

var androidPackagePattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*(\.[a-zA-Z][a-zA-Z0-9_]*)+$`)
var iosBundleIdPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]*$`)

type AppIdentifierRepository interface {
	InsertAppIdentifier(ctx context.Context, appId string, platform types.Platform, identifier string) (string, error)
	GetAppIdentifiers(ctx context.Context, appId string) ([]store.AppIdentifierRow, error)
	GetAppIdentifierByID(ctx context.Context, appId string, identifierId string) (*store.AppIdentifierRef, error)
	GetAppIdentifierByPlatformAndIdentifier(ctx context.Context, appId string, platform types.Platform, identifier string) (*store.AppIdentifierRef, error)
	DeleteAppIdentifier(ctx context.Context, appId string, identifierId string) error
	SetBuildNumber(ctx context.Context, appId string, identifierId string, buildNumber string) error
	AllocateBuildNumber(ctx context.Context, appId string, identifierId string, next func(types.Platform, string) (string, error)) (*store.AppIdentifierRef, error)
}

// AppIdentifier is the dashboard projection of one store identity.
type AppIdentifier struct {
	Id                    string         `json:"id"`
	Platform              types.Platform `json:"platform"`
	Identifier            string         `json:"identifier"`
	BuildNumber           string         `json:"buildNumber"`
	HasAndroidCredentials bool           `json:"hasAndroidCredentials"`
	HasIosCredentials     bool           `json:"hasIosCredentials"`
	CreatedAt             string         `json:"createdAt"`
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

func validateIdentifier(platform types.Platform, identifier string) error {
	if len(identifier) > 255 {
		return validation.Errorf("identifier", "identifier must be at most 255 characters")
	}
	switch platform {
	case types.PlatformAndroid:
		if !androidPackagePattern.MatchString(identifier) {
			return validation.Errorf("identifier", "%q is not a valid Android application id", identifier)
		}
	case types.PlatformIOS:
		if !iosBundleIdPattern.MatchString(identifier) {
			return validation.Errorf("identifier", "%q is not a valid iOS bundle identifier", identifier)
		}
	default:
		return validation.Errorf("platform", "platform must be %q or %q", types.PlatformAndroid, types.PlatformIOS)
	}
	return nil
}

func (s *AppIdentifierService) CreateAppIdentifier(ctx context.Context, appId string, platform types.Platform, identifier string) (string, error) {
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

// GetAppIdentifiers lists app identifiers with their platform signing-credential readiness.
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
			BuildNumber:           row.BuildNumber,
			HasAndroidCredentials: row.HasAndroidCredentials,
			HasIosCredentials:     row.HasIosCredentials,
			CreatedAt:             row.CreatedAt.UTC().Format(time.RFC3339),
		}
	}
	return identifiers, nil
}

// SetBuildNumber overwrites the store build counter, the manual escape hatch
// when it drifts from what the store actually holds.
func (s *AppIdentifierService) SetBuildNumber(ctx context.Context, appId string, identifierId string, buildNumber string) error {
	if s.repo == nil {
		return store.ErrNotSupportedInStatelessMode
	}
	ref, err := s.repo.GetAppIdentifierByID(ctx, appId, identifierId)
	if err != nil {
		return err
	}
	if ref == nil {
		return &store.ErrResourceNotFound{Resource: "app identifier", Identifier: identifierId}
	}
	if err := validation.BuildNumber(ref.Platform, buildNumber); err != nil {
		return err
	}
	if err := s.repo.SetBuildNumber(ctx, appId, identifierId, buildNumber); err != nil {
		return err
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionAppIdentifierBuildNumberSet,
		TargetType:    "app_identifier",
		TargetID:      identifierId,
		TargetDisplay: ref.Identifier,
		AppID:         appId,
		Metadata: map[string]any{
			"platform": ref.Platform,
			"from":     ref.BuildNumber,
			"to":       buildNumber,
		},
	})
	return nil
}

func (s *AppIdentifierService) DeleteAppIdentifier(ctx context.Context, appId string, identifierId string) error {
	if s.repo == nil {
		return store.ErrNotSupportedInStatelessMode
	}
	// Read before the delete: afterwards there is no row left to name in the
	// audit entry. Best-effort, like the entry itself.
	displayName := identifierId
	var platform types.Platform
	if ref, err := s.repo.GetAppIdentifierByID(ctx, appId, identifierId); err == nil && ref != nil {
		displayName = ref.Identifier
		platform = ref.Platform
	}
	if err := s.repo.DeleteAppIdentifier(ctx, appId, identifierId); err != nil {
		return err
	}
	cache.GetCache().Delete(dashboard.ComputeGetApiKeyAccessCacheKey(appId))
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

// AllocateBuildNumber reserves the next build number of an identifier.
func (s *AppIdentifierService) AllocateBuildNumber(ctx context.Context, appId string, identifierId string) (string, error) {
	if s.repo == nil {
		return "", store.ErrNotSupportedInStatelessMode
	}
	ref, err := s.repo.AllocateBuildNumber(ctx, appId, identifierId, s.nextBuildNumber)
	if err != nil {
		return "", err
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionAppIdentifierBuildNumberAllocated,
		TargetType:    "app_identifier",
		TargetID:      ref.Id,
		TargetDisplay: ref.Identifier,
		AppID:         appId,
		Metadata: map[string]any{
			"platform":    ref.Platform,
			"buildNumber": ref.BuildNumber,
			"from":        ref.PreviousBuildNumber,
			"to":          ref.BuildNumber,
		},
	})
	return ref.BuildNumber, nil
}

// nextBuildNumber runs against the current counter while the repository holds
// the identifier row lock; errors abort the transaction without a reservation.
func (s *AppIdentifierService) nextBuildNumber(platform types.Platform, current string) (string, error) {
	if err := validation.BuildNumber(platform, current); err != nil {
		return "", err
	}
	// Only the last iOS component changes; the prefix is preserved verbatim.
	prefix, component := "", current
	if platform == types.PlatformIOS {
		if dot := strings.LastIndexByte(current, '.'); dot >= 0 {
			prefix, component = current[:dot+1], current[dot+1:]
		}
	}
	// Validation guarantees a decimal component. big.Int keeps it exact beyond
	// int64 without imposing Android's versionCode limit on iOS.
	number, _ := new(big.Int).SetString(component, 10)
	if platform == types.PlatformAndroid && number.Cmp(big.NewInt(validation.MaxAndroidBuildNumber)) >= 0 {
		return "", store.ErrBuildNumberExhausted
	}
	return prefix + number.Add(number, big.NewInt(1)).String(), nil
}
