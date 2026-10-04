package services

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"xprem/internal/auditlog"
	"xprem/internal/crypto"
	"xprem/internal/repository"
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
	exportApp, exportEnvironment string
	exportCalls                  int
	listCalls, valueCalls        int
	channelApp                   string
	channelLookups               []string
	environments                 map[string]string // name -> id
	byScopeKey                   map[envScopeKey]fakeSealedEnvVar
	channelEnvs                  map[string]*string
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
		return "", &repository.ErrResourceAlreadyExists{Resource: "environment", Identifier: name}
	}
	id := "id-" + name
	f.environments[name] = id
	return id, nil
}

func (f *fakeEnvironmentRepo) ListEnvironments(_ context.Context, _ string) ([]repository.EnvironmentRow, error) {
	f.listCalls++
	rows := make([]repository.EnvironmentRow, 0, len(f.environments))
	for name, id := range f.environments {
		rows = append(rows, repository.EnvironmentRow{Id: id, Name: name})
	}
	return rows, nil
}

func (f *fakeEnvironmentRepo) GetEnvironmentIdByName(_ context.Context, _ string, name string) (string, error) {
	if id, ok := f.environments[name]; ok {
		return id, nil
	}
	return "", &repository.ErrResourceNotFound{Resource: "environment", Identifier: name}
}

func (f *fakeEnvironmentRepo) DeleteEnvironment(_ context.Context, _ string, name string) error {
	id, ok := f.environments[name]
	if !ok {
		return &repository.ErrResourceNotFound{Resource: "environment", Identifier: name}
	}
	for _, envId := range f.channelEnvs {
		if envId != nil && *envId == id {
			return &repository.ErrEnvironmentHasChannels{EnvironmentName: name}
		}
	}
	delete(f.environments, name)
	return nil
}

func (f *fakeEnvironmentRepo) UpsertEnvVar(_ context.Context, environmentId string, key string, isPublic bool, sealedValue string) error {
	f.byScopeKey[envScopeKey{environmentId, key}] = fakeSealedEnvVar{isPublic: isPublic, sealedValue: sealedValue}
	return nil
}

func (f *fakeEnvironmentRepo) ListEnvVars(_ context.Context, _ string) ([]repository.EnvVarRow, error) {
	f.listCalls++
	rows := make([]repository.EnvVarRow, 0, len(f.byScopeKey))
	for scopeKey, envVar := range f.byScopeKey {
		rows = append(rows, repository.EnvVarRow{EnvironmentId: scopeKey.environmentId, Key: scopeKey.key, IsPublic: envVar.isPublic})
	}
	return rows, nil
}

func (f *fakeEnvironmentRepo) GetSealedValue(_ context.Context, environmentId string, key string) (*string, error) {
	f.valueCalls++
	envVar, ok := f.byScopeKey[envScopeKey{environmentId, key}]
	if !ok {
		return nil, nil
	}
	return &envVar.sealedValue, nil
}

func (f *fakeEnvironmentRepo) DeleteEnvVar(_ context.Context, environmentId string, key string) error {
	scopeKey := envScopeKey{environmentId, key}
	if _, ok := f.byScopeKey[scopeKey]; !ok {
		return &repository.ErrResourceNotFound{Resource: "env var", Identifier: key}
	}
	delete(f.byScopeKey, scopeKey)
	return nil
}

func (f *fakeEnvironmentRepo) SetChannelEnvironment(_ context.Context, _ string, channelName string, environmentId *string) error {
	if _, ok := f.channelEnvs[channelName]; !ok {
		return &repository.ErrResourceNotFound{Resource: "channel", Identifier: channelName}
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
	notFoundErr := (*repository.ErrResourceNotFound)(nil)
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
	notFoundErr := (*repository.ErrResourceNotFound)(nil)
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
	alreadyExists := (*repository.ErrResourceAlreadyExists)(nil)
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
	notFoundErr := (*repository.ErrResourceNotFound)(nil)
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
	inUseErr := (*repository.ErrEnvironmentHasChannels)(nil)
	assert.ErrorAs(t, err, &inUseErr)

	notFoundErr := (*repository.ErrResourceNotFound)(nil)
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
	assert.ErrorIs(t, err, repository.ErrNotSupportedInStatelessMode)
	_, err = service.ListEnvironments(ctx, "app-1")
	assert.ErrorIs(t, err, repository.ErrNotSupportedInStatelessMode)
	assert.ErrorIs(t, service.DeleteEnvironment(ctx, "app-1", "staging"), repository.ErrNotSupportedInStatelessMode)
	assert.ErrorIs(t, service.SetEnvVar(ctx, "app-1", "staging", "K", "v", false), repository.ErrNotSupportedInStatelessMode)
	_, err = service.RevealEnvVar(ctx, "app-1", "staging", "K")
	assert.ErrorIs(t, err, repository.ErrNotSupportedInStatelessMode)
	assert.ErrorIs(t, service.DeleteEnvVar(ctx, "app-1", "staging", "K"), repository.ErrNotSupportedInStatelessMode)
	assert.ErrorIs(t, service.SetChannelEnvironment(ctx, "app-1", "prod-channel", nil), repository.ErrNotSupportedInStatelessMode)
}

func TestExportAuthorizesTheResolvedEnvironment(t *testing.T) {
	setMasterKey(t)
	ctx := context.Background()
	repo := newFakeEnvironmentRepo()
	id := stagingEnvId
	repo.channelEnvs["release"] = &id
	repo.channelEnvs["unbound"] = nil
	env := NewEnvironmentService(repo)
	require.NoError(t, env.SetEnvVar(ctx, "app-1", "staging", "URL", "https://example.test", true))

	var judged []string
	denied := errors.New("denied")
	authorize := func(verdict error) func(string) error {
		return func(environment string) error {
			judged = append(judged, environment)
			return verdict
		}
	}

	got, err := env.ExportVariables(ctx, "app-1", "release", "", authorize(nil))
	require.NoError(t, err)
	assert.Len(t, got.Variables, 1)
	_, err = env.ExportVariables(ctx, "app-1", "release", "", authorize(denied))
	require.ErrorIs(t, err, denied)
	_, err = env.ExportVariables(ctx, "app-1", "", "staging", authorize(denied))
	require.ErrorIs(t, err, denied)
	assert.Equal(t, []string{"staging", "staging", "staging"}, judged, "a channel is judged by the environment it resolves to")

	_, err = env.ExportVariables(ctx, "app-1", "unbound", "", authorize(denied))
	require.NoError(t, err, "a channel without an environment exports nothing to authorize")
	assert.Len(t, judged, 3)
}

func TestBuildEnvironmentSelection(t *testing.T) {
	setMasterKey(t)
	ctx := context.Background()
	repo := newFakeEnvironmentRepo()
	id := stagingEnvId
	repo.channelEnvs["release"] = &id
	repo.channelEnvs["unbound"] = nil
	env := NewEnvironmentService(repo)
	require.NoError(t, env.SetEnvVar(ctx, "app-1", "staging", "URL", "https://example.test", true))
	require.NoError(t, env.SetEnvVar(ctx, "app-1", "staging", "EMPTY", "", false))
	require.NoError(t, env.SetEnvVar(ctx, "app-1", "production", "PRIVATE", "other environment", false))

	for _, tc := range []struct {
		name, channel, environment string
		wantErr                    bool
		count                      int
	}{
		{name: "none"}, {name: "direct", environment: "staging", count: 2}, {name: "channel", channel: "release", count: 2},
		{name: "unbound", channel: "unbound"}, {name: "unknown channel", channel: "foreign", wantErr: true},
		{name: "unknown environment", environment: "foreign", wantErr: true}, {name: "both", channel: "release", environment: "staging", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := repo.exportCalls
			got, err := env.ExportVariables(ctx, "app-1", tc.channel, tc.environment, nil)
			expectedCalls := 1
			if tc.name == "none" || tc.name == "both" {
				expectedCalls = 0
			}
			require.Equal(t, expectedCalls, repo.exportCalls-before)
			if tc.wantErr {
				require.Error(t, err)
				require.Nil(t, got)
				return
			}
			require.NoError(t, err)
			require.Len(t, got.Variables, tc.count)
			if tc.count > 0 {
				require.Equal(t, "staging", *got.Environment)
				require.Equal(t, "https://example.test", got.Variables["EXPO_PUBLIC_URL"])
				require.Contains(t, got.Variables, "EMPTY")
				require.NotContains(t, got.Variables, "PRIVATE")
			} else {
				require.Nil(t, got.Environment)
				require.NotNil(t, got.Variables)
			}
		})
	}
	require.Equal(t, "app-1", repo.channelApp)
	require.Equal(t, []string{"release", "unbound", "foreign"}, repo.channelLookups)
	require.Zero(t, repo.listCalls)
	require.Zero(t, repo.valueCalls)
}

func TestBuildEnvironmentAuditAndDecryptionFailure(t *testing.T) {
	setMasterKey(t)
	repo := newFakeEnvironmentRepo()
	env := NewEnvironmentService(repo)
	ctx := context.Background()
	require.NoError(t, env.SetEnvVar(ctx, "app-1", "staging", "TOKEN", "secret sentinel", false))
	var events []auditlog.Event
	env.SetOnAuditEvent(func(_ context.Context, event auditlog.Event) { events = append(events, event) })
	got, err := env.ExportVariables(ctx, "app-1", "", "staging", nil)
	require.NoError(t, err)
	require.Equal(t, "secret sentinel", got.Variables["TOKEN"])
	require.Len(t, events, 1)
	require.Equal(t, auditlog.ActionEnvVarRevealed, events[0].Action)
	encoded, err := json.Marshal(events)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "secret sentinel")
	repo.byScopeKey[envScopeKey{stagingEnvId, "TOKEN"}] = fakeSealedEnvVar{sealedValue: "corrupt"}
	got, err = env.ExportVariables(ctx, "app-1", "", "staging", nil)
	require.Error(t, err)
	require.Nil(t, got)
	require.Len(t, events, 1)
}

func (f *fakeEnvironmentRepo) ResolveEnvironmentVariables(_ context.Context, appID, channel, environment string) (*repository.ResolvedEnvironment, error) {
	f.exportCalls++
	f.exportApp = appID
	resolved := &repository.ResolvedEnvironment{Variables: []repository.SealedEnvVar{}}
	if channel != "" {
		f.channelApp = appID
		f.channelLookups = append(f.channelLookups, channel)
		id, found := f.channelEnvs[channel]
		if !found {
			return nil, &repository.ErrResourceNotFound{Resource: "channel", Identifier: channel}
		}
		if id == nil {
			return resolved, nil
		}
		for name, candidate := range f.environments {
			if candidate == *id {
				environment = name
				break
			}
		}
	}
	id, found := f.environments[environment]
	if !found {
		return nil, &repository.ErrResourceNotFound{Resource: "environment", Identifier: environment}
	}
	resolved.ID, resolved.Name = id, &environment
	f.exportEnvironment = id
	for scope, variable := range f.byScopeKey {
		if scope.environmentId == id {
			resolved.Variables = append(resolved.Variables, repository.SealedEnvVar{Key: scope.key, IsPublic: variable.isPublic, SealedValue: variable.sealedValue})
		}
	}
	return resolved, nil
}

func TestExportVariablesEmptyEnvironmentAndTargetedReads(t *testing.T) {
	setMasterKey(t)
	repo := newFakeEnvironmentRepo()
	service := NewEnvironmentService(repo)
	got, err := service.ExportVariables(context.Background(), "app-1", "", "staging", nil)
	require.NoError(t, err)
	require.Equal(t, "staging", *got.Environment)
	require.Empty(t, got.Variables)
	require.NotNil(t, got.Variables)
	require.Equal(t, "app-1", repo.exportApp)
	require.Equal(t, stagingEnvId, repo.exportEnvironment)
	require.Equal(t, 1, repo.exportCalls)
	require.Zero(t, repo.listCalls)
	require.Zero(t, repo.valueCalls)
}

func TestExportVariablesRejectsForeignCiphertext(t *testing.T) {
	setMasterKey(t)
	for _, tc := range []struct{ name, app, environment string }{
		{"another app", "other-app", stagingEnvId},
		{"another environment", "app-1", productionEnvId},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeEnvironmentRepo()
			sealed, err := crypto.SealAESGCM([]byte("secret sentinel"), []byte(testMasterKey), envVarAAD(tc.app, tc.environment, "TOKEN"))
			require.NoError(t, err)
			repo.byScopeKey[envScopeKey{stagingEnvId, "TOKEN"}] = fakeSealedEnvVar{sealedValue: sealed}
			service := NewEnvironmentService(repo)
			events := 0
			service.SetOnAuditEvent(func(context.Context, auditlog.Event) { events++ })
			got, err := service.ExportVariables(context.Background(), "app-1", "", "staging", nil)
			require.Error(t, err)
			require.Nil(t, got)
			require.Zero(t, events)
		})
	}
}
