package services

import (
	"context"
	"regexp"
	"strings"
	"time"
	"xprem/internal/auditlog"
	"xprem/internal/crypto"
	"xprem/internal/keyStore"
	"xprem/internal/store"
	"xprem/internal/validation"

	"github.com/google/uuid"
)

// PublicEnvPrefix is prepended at injection time to entries flagged public;
// it is never part of the stored key.
const PublicEnvPrefix = "EXPO_PUBLIC_"

// maxEnvValueBytes caps one value.
const maxEnvValueBytes = 8 * 1024

var envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type EnvironmentRepository interface {
	InsertEnvironment(ctx context.Context, appId string, name string) (string, error)
	ListEnvironments(ctx context.Context, appId string) ([]store.EnvironmentRow, error)
	// GetEnvironmentIdByName returns ErrResourceNotFound for an unknown name.
	GetEnvironmentIdByName(ctx context.Context, appId string, name string) (string, error)
	DeleteEnvironment(ctx context.Context, appId string, name string) error
	UpsertEnvVar(ctx context.Context, environmentId string, key string, isPublic bool, sealedValue string) error
	ListEnvVars(ctx context.Context, appId string) ([]store.EnvVarRow, error)
	GetSealedValue(ctx context.Context, environmentId string, key string) (*string, error)
	DeleteEnvVar(ctx context.Context, environmentId string, key string) error
	SetChannelEnvironment(ctx context.Context, appId string, channelName string, environmentId *string) error
}

// EnvVar is the dashboard projection of one entry: key and flag, never the
// value.
type EnvVar struct {
	Key       string `json:"key"`
	IsPublic  bool   `json:"isPublic"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

type Environment struct {
	Id        string   `json:"id"`
	Name      string   `json:"name"`
	CreatedAt string   `json:"createdAt"`
	UpdatedAt string   `json:"updatedAt"`
	Vars      []EnvVar `json:"vars"`
}

type EnvironmentService struct {
	repo EnvironmentRepository
	// onAuditEvent is the audit emission seam; nil (community) means env
	// changes leave no events.
	onAuditEvent auditlog.RecordFunc
}

// NewEnvironmentService builds the service; a nil repo (stateless mode) makes
// every method answer ErrNotSupportedInStatelessMode.
func NewEnvironmentService(repo EnvironmentRepository) *EnvironmentService {
	return &EnvironmentService{repo: repo}
}

// SetOnAuditEvent plugs the audit emission seam. Nil-safe.
func (s *EnvironmentService) SetOnAuditEvent(record auditlog.RecordFunc) {
	s.onAuditEvent = record
}

// envVarAAD binds a sealed value to the environment row owning it (see
// keyStore.AppKeyAAD). The app id is canonicalized: every UUID spelling a
// route accepts must seal and unseal identically.
func envVarAAD(appId string, environmentId string, key string) []byte {
	if parsed, err := uuid.Parse(appId); err == nil {
		appId = parsed.String()
	}
	return []byte(appId + "|env_var|" + environmentId + "|" + key)
}

func validateEnvKey(key string) error {
	if !envKeyPattern.MatchString(key) || len(key) > 200 {
		return validation.Errorf("key", "%q is not a valid environment variable name", key)
	}
	if strings.HasPrefix(strings.ToUpper(key), PublicEnvPrefix) {
		return validation.Errorf("key", "do not include the %s prefix, it is added automatically when the entry is public", PublicEnvPrefix)
	}
	return nil
}

// validateEnvironmentName is validation.Name plus a no-surrounding-whitespace
// rule: "production" and "production " must not both exist.
func validateEnvironmentName(name string) error {
	if err := validation.Name("environment", name); err != nil {
		return err
	}
	if strings.TrimSpace(name) != name {
		return validation.Errorf("environment", "must not start or end with whitespace")
	}
	return nil
}

func (s *EnvironmentService) resolveEnvironment(ctx context.Context, appId string, name string) (string, error) {
	if err := validateEnvironmentName(name); err != nil {
		return "", err
	}
	return s.repo.GetEnvironmentIdByName(ctx, appId, name)
}

func (s *EnvironmentService) CreateEnvironment(ctx context.Context, appId string, name string) (string, error) {
	if s.repo == nil {
		return "", store.ErrNotSupportedInStatelessMode
	}
	if err := validateEnvironmentName(name); err != nil {
		return "", err
	}
	id, err := s.repo.InsertEnvironment(ctx, appId, name)
	if err != nil {
		return "", err
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionEnvironmentCreated,
		TargetType:    "environment",
		TargetID:      name,
		TargetDisplay: name,
		AppID:         appId,
		Metadata:      map[string]any{"environment_id": id},
	})
	return id, nil
}

// ListEnvironments returns every environment with its keys (never values).
func (s *EnvironmentService) ListEnvironments(ctx context.Context, appId string) ([]Environment, error) {
	if s.repo == nil {
		return nil, store.ErrNotSupportedInStatelessMode
	}
	rows, err := s.repo.ListEnvironments(ctx, appId)
	if err != nil {
		return nil, err
	}
	varRows, err := s.repo.ListEnvVars(ctx, appId)
	if err != nil {
		return nil, err
	}
	varsByEnvironment := make(map[string][]EnvVar, len(rows))
	for _, row := range varRows {
		varsByEnvironment[row.EnvironmentId] = append(varsByEnvironment[row.EnvironmentId], EnvVar{
			Key:       row.Key,
			IsPublic:  row.IsPublic,
			CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339),
			UpdatedAt: row.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	environments := make([]Environment, len(rows))
	for i, row := range rows {
		vars := varsByEnvironment[row.Id]
		if vars == nil {
			vars = []EnvVar{}
		}
		environments[i] = Environment{
			Id:        row.Id,
			Name:      row.Name,
			CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339),
			UpdatedAt: row.UpdatedAt.UTC().Format(time.RFC3339),
			Vars:      vars,
		}
	}
	return environments, nil
}

func (s *EnvironmentService) DeleteEnvironment(ctx context.Context, appId string, name string) error {
	if s.repo == nil {
		return store.ErrNotSupportedInStatelessMode
	}
	if err := validateEnvironmentName(name); err != nil {
		return err
	}
	if err := s.repo.DeleteEnvironment(ctx, appId, name); err != nil {
		return err
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionEnvironmentDeleted,
		TargetType:    "environment",
		TargetID:      name,
		TargetDisplay: name,
		AppID:         appId,
	})
	return nil
}

func (s *EnvironmentService) SetEnvVar(ctx context.Context, appId string, environmentName string, key string, value string, isPublic bool) error {
	if s.repo == nil {
		return store.ErrNotSupportedInStatelessMode
	}
	if err := validateEnvKey(key); err != nil {
		return err
	}
	if len(value) > maxEnvValueBytes {
		return validation.Errorf("value", "value exceeds the %d KB limit", maxEnvValueBytes/1024)
	}
	environmentId, err := s.resolveEnvironment(ctx, appId, environmentName)
	if err != nil {
		return err
	}
	sealedValue, err := crypto.SealAESGCM([]byte(value), []byte(keyStore.ReadDBKeysMasterKey()), envVarAAD(appId, environmentId, key))
	if err != nil {
		return err
	}
	if err := s.repo.UpsertEnvVar(ctx, environmentId, key, isPublic, sealedValue); err != nil {
		return err
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionEnvVarUpdated,
		TargetType:    "env_var",
		TargetID:      key,
		TargetDisplay: key,
		AppID:         appId,
		Metadata: map[string]any{
			"environment": environmentName,
			"is_public":   isPublic,
		},
	})
	return nil
}

// RevealEnvVar returns the plaintext value; the one read we audit.
func (s *EnvironmentService) RevealEnvVar(ctx context.Context, appId string, environmentName string, key string) (string, error) {
	if s.repo == nil {
		return "", store.ErrNotSupportedInStatelessMode
	}
	if err := validateEnvKey(key); err != nil {
		return "", err
	}
	environmentId, err := s.resolveEnvironment(ctx, appId, environmentName)
	if err != nil {
		return "", err
	}
	sealedValue, err := s.repo.GetSealedValue(ctx, environmentId, key)
	if err != nil {
		return "", err
	}
	if sealedValue == nil {
		return "", &store.ErrResourceNotFound{Resource: "env var", Identifier: key}
	}
	value, err := crypto.UnsealAESGCM(*sealedValue, []byte(keyStore.ReadDBKeysMasterKey()), envVarAAD(appId, environmentId, key))
	if err != nil {
		return "", err
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionEnvVarRevealed,
		TargetType:    "env_var",
		TargetID:      key,
		TargetDisplay: key,
		AppID:         appId,
		Metadata:      map[string]any{"environment": environmentName},
	})
	return string(value), nil
}

func (s *EnvironmentService) DeleteEnvVar(ctx context.Context, appId string, environmentName string, key string) error {
	if s.repo == nil {
		return store.ErrNotSupportedInStatelessMode
	}
	if err := validateEnvKey(key); err != nil {
		return err
	}
	environmentId, err := s.resolveEnvironment(ctx, appId, environmentName)
	if err != nil {
		return err
	}
	if err := s.repo.DeleteEnvVar(ctx, environmentId, key); err != nil {
		return err
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionEnvVarDeleted,
		TargetType:    "env_var",
		TargetID:      key,
		TargetDisplay: key,
		AppID:         appId,
		Metadata:      map[string]any{"environment": environmentName},
	})
	return nil
}

// SetChannelEnvironment points the channel at the named environment; a nil
// name unbinds it.
func (s *EnvironmentService) SetChannelEnvironment(ctx context.Context, appId string, channelName string, environmentName *string) error {
	if s.repo == nil {
		return store.ErrNotSupportedInStatelessMode
	}
	if err := validation.Name("channelName", channelName); err != nil {
		return err
	}
	var environmentId *string
	if environmentName != nil {
		id, err := s.resolveEnvironment(ctx, appId, *environmentName)
		if err != nil {
			return err
		}
		environmentId = &id
	}
	if err := s.repo.SetChannelEnvironment(ctx, appId, channelName, environmentId); err != nil {
		return err
	}
	var metadata map[string]any
	if environmentName != nil {
		metadata = map[string]any{"environment": *environmentName}
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionChannelEnvironmentUpdated,
		TargetType:    "channel",
		TargetID:      channelName,
		TargetDisplay: channelName,
		AppID:         appId,
		Metadata:      metadata,
	})
	invalidateChannelCaches(appId)
	return nil
}
