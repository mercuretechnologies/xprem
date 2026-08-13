package services

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"
	"xprem/internal/auditlog"
	"xprem/internal/crypto"
	"xprem/internal/database"
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

type EnvVarRepository interface {
	UpsertEnvVar(ctx context.Context, appId string, branchId int64, key string, isPublic bool, sealedValue string) error
	ListEnvVars(ctx context.Context, appId string) ([]store.EnvVarRow, error)
	GetSealedValue(ctx context.Context, appId string, branchId int64, key string) (*string, error)
	DeleteEnvVar(ctx context.Context, appId string, branchId int64, key string) error
}

// BranchResolver is the slice of the branch repository the env service needs.
type BranchResolver interface {
	GetBranchByName(ctx context.Context, appId string, branchName string) (int64, error)
}

// EnvVar is the dashboard projection of one entry: branch, key and flag,
// never the value.
type EnvVar struct {
	Key       string `json:"key"`
	IsPublic  bool   `json:"isPublic"`
	Branch    string `json:"branch"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

type EnvVarService struct {
	repo     EnvVarRepository
	branches BranchResolver
	// onAuditEvent is the audit emission seam; nil (community) means env
	// changes leave no events.
	onAuditEvent auditlog.RecordFunc
}

// NewEnvVarService builds the service; a nil repo (stateless mode) makes
// every method answer ErrNotSupportedInStatelessMode.
func NewEnvVarService(repo EnvVarRepository, branches BranchResolver) *EnvVarService {
	return &EnvVarService{
		repo:     repo,
		branches: branches,
	}
}

// SetOnAuditEvent plugs the audit emission seam. Nil-safe.
func (s *EnvVarService) SetOnAuditEvent(record auditlog.RecordFunc) {
	s.onAuditEvent = record
}

// envVarAAD binds a sealed value to the branch row owning it (see
// keyStore.AppKeyAAD). The app id is canonicalized: every UUID spelling a
// route accepts must seal and unseal identically.
func envVarAAD(appId string, branchId int64, key string) []byte {
	if parsed, err := uuid.Parse(appId); err == nil {
		appId = parsed.String()
	}
	return []byte(appId + "|branch_env|" + strconv.FormatInt(branchId, 10) + "|" + key)
}

// resolveBranch maps the branch name to its id; unknown branches are a 404,
// never a silent write elsewhere.
func (s *EnvVarService) resolveBranch(ctx context.Context, appId string, branchName string) (int64, error) {
	if err := validation.Name("branch", branchName); err != nil {
		return 0, err
	}
	branchId, err := s.branches.GetBranchByName(ctx, appId, branchName)
	if err != nil {
		if database.IsNoRows(err) {
			return 0, &store.ErrResourceNotFound{Resource: "branch", Identifier: branchName}
		}
		return 0, err
	}
	return branchId, nil
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

func (s *EnvVarService) SetEnvVar(ctx context.Context, appId string, branchName string, key string, value string, isPublic bool) error {
	if s.repo == nil {
		return store.ErrNotSupportedInStatelessMode
	}
	if err := validateEnvKey(key); err != nil {
		return err
	}
	if len(value) > maxEnvValueBytes {
		return validation.Errorf("value", "value exceeds the %d KB limit", maxEnvValueBytes/1024)
	}
	branchId, err := s.resolveBranch(ctx, appId, branchName)
	if err != nil {
		return err
	}
	sealedValue, err := crypto.SealAESGCM([]byte(value), []byte(keyStore.ReadDBKeysMasterKey()), envVarAAD(appId, branchId, key))
	if err != nil {
		return err
	}
	if err := s.repo.UpsertEnvVar(ctx, appId, branchId, key, isPublic, sealedValue); err != nil {
		return err
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionEnvVarUpdated,
		TargetType:    "env_var",
		TargetID:      key,
		TargetDisplay: key,
		AppID:         appId,
		Metadata: map[string]any{
			"branch":    branchName,
			"is_public": isPublic,
		},
	})
	return nil
}

func (s *EnvVarService) ListEnvVars(ctx context.Context, appId string) ([]EnvVar, error) {
	if s.repo == nil {
		return nil, store.ErrNotSupportedInStatelessMode
	}
	rows, err := s.repo.ListEnvVars(ctx, appId)
	if err != nil {
		return nil, err
	}
	envVars := make([]EnvVar, len(rows))
	for i, row := range rows {
		envVars[i] = EnvVar{
			Key:       row.Key,
			IsPublic:  row.IsPublic,
			Branch:    row.BranchName,
			CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339),
			UpdatedAt: row.UpdatedAt.UTC().Format(time.RFC3339),
		}
	}
	return envVars, nil
}

// RevealEnvVar returns the plaintext value; the one read we audit.
func (s *EnvVarService) RevealEnvVar(ctx context.Context, appId string, branchName string, key string) (string, error) {
	if s.repo == nil {
		return "", store.ErrNotSupportedInStatelessMode
	}
	if err := validateEnvKey(key); err != nil {
		return "", err
	}
	branchId, err := s.resolveBranch(ctx, appId, branchName)
	if err != nil {
		return "", err
	}
	sealedValue, err := s.repo.GetSealedValue(ctx, appId, branchId, key)
	if err != nil {
		return "", err
	}
	if sealedValue == nil {
		return "", &store.ErrResourceNotFound{Resource: "env var", Identifier: key}
	}
	value, err := crypto.UnsealAESGCM(*sealedValue, []byte(keyStore.ReadDBKeysMasterKey()), envVarAAD(appId, branchId, key))
	if err != nil {
		return "", err
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionEnvVarRevealed,
		TargetType:    "env_var",
		TargetID:      key,
		TargetDisplay: key,
		AppID:         appId,
		Metadata:      map[string]any{"branch": branchName},
	})
	return string(value), nil
}

func (s *EnvVarService) DeleteEnvVar(ctx context.Context, appId string, branchName string, key string) error {
	if s.repo == nil {
		return store.ErrNotSupportedInStatelessMode
	}
	if err := validateEnvKey(key); err != nil {
		return err
	}
	branchId, err := s.resolveBranch(ctx, appId, branchName)
	if err != nil {
		return err
	}
	if err := s.repo.DeleteEnvVar(ctx, appId, branchId, key); err != nil {
		return err
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionEnvVarDeleted,
		TargetType:    "env_var",
		TargetID:      key,
		TargetDisplay: key,
		AppID:         appId,
		Metadata:      map[string]any{"branch": branchName},
	})
	return nil
}
