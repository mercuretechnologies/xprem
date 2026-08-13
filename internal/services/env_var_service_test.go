// Service-level tests for env vars: key validation (incl. the EXPO_PUBLIC_
// prefix refusal), sealing with the branch-bound AAD (app id canonicalized),
// branch resolution, the audited reveal and the stateless refusal. SQL
// persistence (unique constraint, cascades) is covered by the store tests.
package services

import (
	"context"
	"strconv"
	"testing"
	"xprem/internal/auditlog"
	"xprem/internal/crypto"
	"xprem/internal/store"
	"xprem/internal/validation"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeEnvVarRepo struct {
	// keyed by "<branchId>|<key>"
	byScopeKey map[string]fakeSealedEnvVar
}

// fakeBranchResolver mirrors the pgx not-found contract of the real branch
// repository.
type fakeBranchResolver struct {
	branches map[string]int64
}

func (f *fakeBranchResolver) GetBranchByName(_ context.Context, _ string, branchName string) (int64, error) {
	if id, ok := f.branches[branchName]; ok {
		return id, nil
	}
	return 0, pgx.ErrNoRows
}

type fakeSealedEnvVar struct {
	isPublic    bool
	sealedValue string
}

func newFakeEnvVarRepo() *fakeEnvVarRepo {
	return &fakeEnvVarRepo{
		byScopeKey: map[string]fakeSealedEnvVar{},
	}
}

func newFakeBranchResolver() *fakeBranchResolver {
	return &fakeBranchResolver{branches: map[string]int64{"staging": 7, "production": 8}}
}

func envScopeKey(branchId int64, key string) string {
	return strconv.FormatInt(branchId, 10) + "|" + key
}

func (f *fakeEnvVarRepo) UpsertEnvVar(_ context.Context, _ string, branchId int64, key string, isPublic bool, sealedValue string) error {
	f.byScopeKey[envScopeKey(branchId, key)] = fakeSealedEnvVar{isPublic: isPublic, sealedValue: sealedValue}
	return nil
}

func (f *fakeEnvVarRepo) ListEnvVars(_ context.Context, _ string) ([]store.EnvVarRow, error) {
	return nil, nil
}

func (f *fakeEnvVarRepo) GetSealedValue(_ context.Context, _ string, branchId int64, key string) (*string, error) {
	envVar, ok := f.byScopeKey[envScopeKey(branchId, key)]
	if !ok {
		return nil, nil
	}
	return &envVar.sealedValue, nil
}

func (f *fakeEnvVarRepo) DeleteEnvVar(_ context.Context, _ string, branchId int64, key string) error {
	scopeKey := envScopeKey(branchId, key)
	if _, ok := f.byScopeKey[scopeKey]; !ok {
		return &store.ErrResourceNotFound{Resource: "env var", Identifier: key}
	}
	delete(f.byScopeKey, scopeKey)
	return nil
}

func TestSetEnvVarSealsWithBranchBoundAAD(t *testing.T) {
	setMasterKey(t)
	repo := newFakeEnvVarRepo()
	service := NewEnvVarService(repo, newFakeBranchResolver())
	ctx := context.Background()

	require.NoError(t, service.SetEnvVar(ctx, "app-1", "staging", "API_URL", "https://staging.example.com", true))
	require.NoError(t, service.SetEnvVar(ctx, "app-1", "production", "API_URL", "https://api.example.com", true))

	stagingVar := repo.byScopeKey["7|API_URL"]
	assert.True(t, stagingVar.isPublic)
	value, err := crypto.UnsealAESGCM(stagingVar.sealedValue, []byte(testMasterKey), envVarAAD("app-1", 7, "API_URL"))
	require.NoError(t, err)
	assert.Equal(t, "https://staging.example.com", string(value))

	// A blob sealed for one branch does not open under another branch's scope.
	_, err = crypto.UnsealAESGCM(stagingVar.sealedValue, []byte(testMasterKey), envVarAAD("app-1", 8, "API_URL"))
	assert.Error(t, err)
}

func TestEnvVarAADCanonicalizesAppId(t *testing.T) {
	uppercase := envVarAAD("019A7B2C-0000-4000-8000-000000000001", 7, "API_URL")
	lowercase := envVarAAD("019a7b2c-0000-4000-8000-000000000001", 7, "API_URL")
	assert.Equal(t, string(lowercase), string(uppercase))
}

func TestSetEnvVarValidation(t *testing.T) {
	setMasterKey(t)
	service := NewEnvVarService(newFakeEnvVarRepo(), newFakeBranchResolver())
	ctx := context.Background()
	var valErr *validation.Error

	assert.ErrorAs(t, service.SetEnvVar(ctx, "app-1", "staging", "1BAD", "v", false), &valErr)
	assert.ErrorAs(t, service.SetEnvVar(ctx, "app-1", "staging", "BAD-DASH", "v", false), &valErr)
	// The prefix is added automatically for public entries; never stored.
	assert.ErrorAs(t, service.SetEnvVar(ctx, "app-1", "staging", "EXPO_PUBLIC_API_URL", "v", true), &valErr)
	assert.ErrorAs(t, service.SetEnvVar(ctx, "app-1", "staging", "expo_public_api_url", "v", true), &valErr)

	// Unknown branch is a 404, not a silent write elsewhere.
	err := service.SetEnvVar(ctx, "app-1", "nope", "API_URL", "v", true)
	notFoundErr := (*store.ErrResourceNotFound)(nil)
	assert.ErrorAs(t, err, &notFoundErr)

	// An empty value is legitimate.
	assert.NoError(t, service.SetEnvVar(ctx, "app-1", "staging", "EMPTY_ALLOWED", "", false))
}

func TestRevealEnvVarRoundTripsAndAudits(t *testing.T) {
	setMasterKey(t)
	repo := newFakeEnvVarRepo()
	service := NewEnvVarService(repo, newFakeBranchResolver())
	var recorded []auditlog.Event
	service.SetOnAuditEvent(func(_ context.Context, event auditlog.Event) {
		recorded = append(recorded, event)
	})
	ctx := context.Background()

	require.NoError(t, service.SetEnvVar(ctx, "app-1", "staging", "TOKEN", "s3cr3t", false))
	value, err := service.RevealEnvVar(ctx, "app-1", "staging", "TOKEN")
	require.NoError(t, err)
	assert.Equal(t, "s3cr3t", value)

	// Same key on another branch does not exist.
	_, err = service.RevealEnvVar(ctx, "app-1", "production", "TOKEN")
	notFoundErr := (*store.ErrResourceNotFound)(nil)
	assert.ErrorAs(t, err, &notFoundErr)

	require.NoError(t, service.DeleteEnvVar(ctx, "app-1", "staging", "TOKEN"))

	require.Len(t, recorded, 3)
	assert.Equal(t, auditlog.ActionEnvVarUpdated, recorded[0].Action)
	assert.Equal(t, "staging", recorded[0].Metadata["branch"])
	assert.Equal(t, false, recorded[0].Metadata["is_public"])
	assert.Equal(t, auditlog.ActionEnvVarRevealed, recorded[1].Action)
	assert.Equal(t, auditlog.ActionEnvVarDeleted, recorded[2].Action)
	for _, event := range recorded {
		assert.NotContains(t, event.Metadata, "value")
	}
}

func TestEnvVarsUnsupportedInStatelessMode(t *testing.T) {
	service := NewEnvVarService(nil, nil)
	ctx := context.Background()
	assert.ErrorIs(t, service.SetEnvVar(ctx, "app-1", "staging", "K", "v", false), store.ErrNotSupportedInStatelessMode)
	_, err := service.ListEnvVars(ctx, "app-1")
	assert.ErrorIs(t, err, store.ErrNotSupportedInStatelessMode)
	_, err = service.RevealEnvVar(ctx, "app-1", "staging", "K")
	assert.ErrorIs(t, err, store.ErrNotSupportedInStatelessMode)
	assert.ErrorIs(t, service.DeleteEnvVar(ctx, "app-1", "staging", "K"), store.ErrNotSupportedInStatelessMode)
}
