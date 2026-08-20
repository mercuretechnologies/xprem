package services

import (
	"context"
	"testing"
	"xprem/internal/auditlog"
	"xprem/internal/crypto"
	"xprem/internal/store"
	"xprem/internal/validation"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	stagingEnvId    = "019a7b2c-0000-4000-8000-00000000000a"
	productionEnvId = "019a7b2c-0000-4000-8000-00000000000b"
)

type fakeSealedEnvVar struct {
	isPublic    bool
	sealedValue string
}

type envScopeKey struct {
	environmentId string
	key           string
}

type fakeEnvironmentRepo struct {
	environments map[string]string // name -> id
	byScopeKey   map[envScopeKey]fakeSealedEnvVar
	channelEnvs  map[string]*string
}

func newFakeEnvironmentRepo() *fakeEnvironmentRepo {
	return &fakeEnvironmentRepo{
		environments: map[string]string{"staging": stagingEnvId, "production": productionEnvId},
		byScopeKey:   map[envScopeKey]fakeSealedEnvVar{},
		channelEnvs:  map[string]*string{"prod-channel": nil},
	}
}

func (f *fakeEnvironmentRepo) InsertEnvironment(_ context.Context, _ string, name string) (string, error) {
	if _, ok := f.environments[name]; ok {
		return "", &store.ErrResourceAlreadyExists{Resource: "environment", Identifier: name}
	}
	id := "id-" + name
	f.environments[name] = id
	return id, nil
}

func (f *fakeEnvironmentRepo) ListEnvironments(_ context.Context, _ string) ([]store.EnvironmentRow, error) {
	rows := make([]store.EnvironmentRow, 0, len(f.environments))
	for name, id := range f.environments {
		rows = append(rows, store.EnvironmentRow{Id: id, Name: name})
	}
	return rows, nil
}

func (f *fakeEnvironmentRepo) GetEnvironmentIdByName(_ context.Context, _ string, name string) (string, error) {
	if id, ok := f.environments[name]; ok {
		return id, nil
	}
	return "", &store.ErrResourceNotFound{Resource: "environment", Identifier: name}
}

func (f *fakeEnvironmentRepo) DeleteEnvironment(_ context.Context, _ string, name string) error {
	id, ok := f.environments[name]
	if !ok {
		return &store.ErrResourceNotFound{Resource: "environment", Identifier: name}
	}
	for _, envId := range f.channelEnvs {
		if envId != nil && *envId == id {
			return &store.ErrEnvironmentHasChannels{EnvironmentName: name}
		}
	}
	delete(f.environments, name)
	return nil
}

func (f *fakeEnvironmentRepo) UpsertEnvVar(_ context.Context, environmentId string, key string, isPublic bool, sealedValue string) error {
	f.byScopeKey[envScopeKey{environmentId, key}] = fakeSealedEnvVar{isPublic: isPublic, sealedValue: sealedValue}
	return nil
}

func (f *fakeEnvironmentRepo) ListEnvVars(_ context.Context, _ string) ([]store.EnvVarRow, error) {
	rows := make([]store.EnvVarRow, 0, len(f.byScopeKey))
	for scopeKey, envVar := range f.byScopeKey {
		rows = append(rows, store.EnvVarRow{EnvironmentId: scopeKey.environmentId, Key: scopeKey.key, IsPublic: envVar.isPublic})
	}
	return rows, nil
}

func (f *fakeEnvironmentRepo) GetSealedValue(_ context.Context, environmentId string, key string) (*string, error) {
	envVar, ok := f.byScopeKey[envScopeKey{environmentId, key}]
	if !ok {
		return nil, nil
	}
	return &envVar.sealedValue, nil
}

func (f *fakeEnvironmentRepo) DeleteEnvVar(_ context.Context, environmentId string, key string) error {
	scopeKey := envScopeKey{environmentId, key}
	if _, ok := f.byScopeKey[scopeKey]; !ok {
		return &store.ErrResourceNotFound{Resource: "env var", Identifier: key}
	}
	delete(f.byScopeKey, scopeKey)
	return nil
}

func (f *fakeEnvironmentRepo) SetChannelEnvironment(_ context.Context, _ string, channelName string, environmentId *string) error {
	if _, ok := f.channelEnvs[channelName]; !ok {
		return &store.ErrResourceNotFound{Resource: "channel", Identifier: channelName}
	}
	f.channelEnvs[channelName] = environmentId
	return nil
}

func TestSetEnvVarSealsWithEnvironmentBoundAAD(t *testing.T) {
	setMasterKey(t)
	repo := newFakeEnvironmentRepo()
	service := NewEnvironmentService(repo)
	ctx := context.Background()

	require.NoError(t, service.SetEnvVar(ctx, "app-1", "staging", "API_URL", "https://staging.example.com", true))
	require.NoError(t, service.SetEnvVar(ctx, "app-1", "production", "API_URL", "https://api.example.com", true))

	stagingVar := repo.byScopeKey[envScopeKey{stagingEnvId, "API_URL"}]
	assert.True(t, stagingVar.isPublic)
	value, err := crypto.UnsealAESGCM(stagingVar.sealedValue, []byte(testMasterKey), envVarAAD("app-1", stagingEnvId, "API_URL"))
	require.NoError(t, err)
	assert.Equal(t, "https://staging.example.com", string(value))

	// A blob sealed for one environment does not open under another's scope.
	_, err = crypto.UnsealAESGCM(stagingVar.sealedValue, []byte(testMasterKey), envVarAAD("app-1", productionEnvId, "API_URL"))
	assert.Error(t, err)
}

func TestEnvVarAADCanonicalizesAppId(t *testing.T) {
	uppercase := envVarAAD("019A7B2C-0000-4000-8000-000000000001", stagingEnvId, "API_URL")
	lowercase := envVarAAD("019a7b2c-0000-4000-8000-000000000001", stagingEnvId, "API_URL")
	assert.Equal(t, string(lowercase), string(uppercase))
}

func TestSetEnvVarValidation(t *testing.T) {
	setMasterKey(t)
	service := NewEnvironmentService(newFakeEnvironmentRepo())
	ctx := context.Background()
	var valErr *validation.Error

	assert.ErrorAs(t, service.SetEnvVar(ctx, "app-1", "staging", "1BAD", "v", false), &valErr)
	assert.ErrorAs(t, service.SetEnvVar(ctx, "app-1", "staging", "BAD-DASH", "v", false), &valErr)
	// The prefix is added automatically for public entries; never stored.
	assert.ErrorAs(t, service.SetEnvVar(ctx, "app-1", "staging", "EXPO_PUBLIC_API_URL", "v", true), &valErr)
	assert.ErrorAs(t, service.SetEnvVar(ctx, "app-1", "staging", "expo_public_api_url", "v", true), &valErr)
	assert.ErrorAs(t, service.SetEnvVar(ctx, "app-1", "bad/env", "API_URL", "v", true), &valErr)

	// Unknown environment is a 404, not a silent write elsewhere.
	err := service.SetEnvVar(ctx, "app-1", "nope", "API_URL", "v", true)
	notFoundErr := (*store.ErrResourceNotFound)(nil)
	assert.ErrorAs(t, err, &notFoundErr)

	// An empty value is legitimate.
	assert.NoError(t, service.SetEnvVar(ctx, "app-1", "staging", "EMPTY_ALLOWED", "", false))
}

func TestRevealEnvVarRoundTripsAndAudits(t *testing.T) {
	setMasterKey(t)
	repo := newFakeEnvironmentRepo()
	service := NewEnvironmentService(repo)
	var recorded []auditlog.Event
	service.SetOnAuditEvent(func(_ context.Context, event auditlog.Event) {
		recorded = append(recorded, event)
	})
	ctx := context.Background()

	require.NoError(t, service.SetEnvVar(ctx, "app-1", "staging", "TOKEN", "s3cr3t", false))
	value, err := service.RevealEnvVar(ctx, "app-1", "staging", "TOKEN")
	require.NoError(t, err)
	assert.Equal(t, "s3cr3t", value)

	// Same key in another environment does not exist.
	_, err = service.RevealEnvVar(ctx, "app-1", "production", "TOKEN")
	notFoundErr := (*store.ErrResourceNotFound)(nil)
	assert.ErrorAs(t, err, &notFoundErr)

	require.NoError(t, service.DeleteEnvVar(ctx, "app-1", "staging", "TOKEN"))

	require.Len(t, recorded, 3)
	assert.Equal(t, auditlog.ActionEnvVarUpdated, recorded[0].Action)
	assert.Equal(t, "staging", recorded[0].Metadata["environment"])
	assert.Equal(t, false, recorded[0].Metadata["is_public"])
	assert.Equal(t, auditlog.ActionEnvVarRevealed, recorded[1].Action)
	assert.Equal(t, auditlog.ActionEnvVarDeleted, recorded[2].Action)
	for _, event := range recorded {
		assert.NotContains(t, event.Metadata, "value")
	}
}

func TestEnvironmentLifecycleAndListing(t *testing.T) {
	setMasterKey(t)
	repo := newFakeEnvironmentRepo()
	service := NewEnvironmentService(repo)
	var recorded []auditlog.Event
	service.SetOnAuditEvent(func(_ context.Context, event auditlog.Event) {
		recorded = append(recorded, event)
	})
	ctx := context.Background()

	var valErr *validation.Error
	_, err := service.CreateEnvironment(ctx, "app-1", "bad/name")
	assert.ErrorAs(t, err, &valErr)
	// "production" and "production " must not both exist.
	_, err = service.CreateEnvironment(ctx, "app-1", "production ")
	assert.ErrorAs(t, err, &valErr)

	id, err := service.CreateEnvironment(ctx, "app-1", "preview")
	require.NoError(t, err)
	assert.NotEmpty(t, id)
	_, err = service.CreateEnvironment(ctx, "app-1", "preview")
	alreadyExists := (*store.ErrResourceAlreadyExists)(nil)
	assert.ErrorAs(t, err, &alreadyExists)

	require.NoError(t, service.SetEnvVar(ctx, "app-1", "preview", "API_URL", "https://preview.example.com", true))
	environments, err := service.ListEnvironments(ctx, "app-1")
	require.NoError(t, err)
	require.Len(t, environments, 3)
	for _, environment := range environments {
		// Never null in the JSON, even for an empty environment.
		assert.NotNil(t, environment.Vars)
		if environment.Name == "preview" {
			require.Len(t, environment.Vars, 1)
			assert.Equal(t, "API_URL", environment.Vars[0].Key)
			assert.True(t, environment.Vars[0].IsPublic)
		} else {
			assert.Empty(t, environment.Vars)
		}
	}

	require.NoError(t, service.DeleteEnvironment(ctx, "app-1", "preview"))
	err = service.DeleteEnvironment(ctx, "app-1", "preview")
	notFoundErr := (*store.ErrResourceNotFound)(nil)
	assert.ErrorAs(t, err, &notFoundErr)

	require.Len(t, recorded, 3)
	assert.Equal(t, auditlog.ActionEnvironmentCreated, recorded[0].Action)
	assert.Equal(t, "preview", recorded[0].TargetID)
	assert.Equal(t, auditlog.ActionEnvVarUpdated, recorded[1].Action)
	assert.Equal(t, auditlog.ActionEnvironmentDeleted, recorded[2].Action)
}

func TestSetChannelEnvironment(t *testing.T) {
	repo := newFakeEnvironmentRepo()
	service := NewEnvironmentService(repo)
	var recorded []auditlog.Event
	service.SetOnAuditEvent(func(_ context.Context, event auditlog.Event) {
		recorded = append(recorded, event)
	})
	ctx := context.Background()
	production := "production"

	require.NoError(t, service.SetChannelEnvironment(ctx, "app-1", "prod-channel", &production))
	require.NotNil(t, repo.channelEnvs["prod-channel"])
	assert.Equal(t, productionEnvId, *repo.channelEnvs["prod-channel"])

	// A bound environment cannot be deleted.
	err := service.DeleteEnvironment(ctx, "app-1", "production")
	inUseErr := (*store.ErrEnvironmentHasChannels)(nil)
	assert.ErrorAs(t, err, &inUseErr)

	notFoundErr := (*store.ErrResourceNotFound)(nil)
	unknown := "nope"
	assert.ErrorAs(t, service.SetChannelEnvironment(ctx, "app-1", "prod-channel", &unknown), &notFoundErr)
	assert.ErrorAs(t, service.SetChannelEnvironment(ctx, "app-1", "no-channel", &production), &notFoundErr)

	// nil unbinds.
	require.NoError(t, service.SetChannelEnvironment(ctx, "app-1", "prod-channel", nil))
	assert.Nil(t, repo.channelEnvs["prod-channel"])
	require.NoError(t, service.DeleteEnvironment(ctx, "app-1", "production"))

	require.Len(t, recorded, 3)
	assert.Equal(t, auditlog.ActionChannelEnvironmentUpdated, recorded[0].Action)
	assert.Equal(t, "prod-channel", recorded[0].TargetID)
	assert.Equal(t, "production", recorded[0].Metadata["environment"])
	assert.Equal(t, auditlog.ActionChannelEnvironmentUpdated, recorded[1].Action)
	assert.NotContains(t, recorded[1].Metadata, "environment")
	assert.Equal(t, auditlog.ActionEnvironmentDeleted, recorded[2].Action)
}

func TestEnvironmentsUnsupportedInStatelessMode(t *testing.T) {
	service := NewEnvironmentService(nil)
	ctx := context.Background()
	_, err := service.CreateEnvironment(ctx, "app-1", "staging")
	assert.ErrorIs(t, err, store.ErrNotSupportedInStatelessMode)
	_, err = service.ListEnvironments(ctx, "app-1")
	assert.ErrorIs(t, err, store.ErrNotSupportedInStatelessMode)
	assert.ErrorIs(t, service.DeleteEnvironment(ctx, "app-1", "staging"), store.ErrNotSupportedInStatelessMode)
	assert.ErrorIs(t, service.SetEnvVar(ctx, "app-1", "staging", "K", "v", false), store.ErrNotSupportedInStatelessMode)
	_, err = service.RevealEnvVar(ctx, "app-1", "staging", "K")
	assert.ErrorIs(t, err, store.ErrNotSupportedInStatelessMode)
	assert.ErrorIs(t, service.DeleteEnvVar(ctx, "app-1", "staging", "K"), store.ErrNotSupportedInStatelessMode)
	assert.ErrorIs(t, service.SetChannelEnvironment(ctx, "app-1", "prod-channel", nil), store.ErrNotSupportedInStatelessMode)
}
