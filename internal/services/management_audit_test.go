package services

import (
	"context"
	"testing"
	"xprem/config"
	"xprem/internal/auditlog"
	"xprem/internal/store"
	"xprem/internal/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The management fakes only carry what the audited paths touch; everything
// else answers empty.

type fakeMgmtAppRepo struct {
	apps map[string]config.AppConfig
}

func (f *fakeMgmtAppRepo) InsertApp(_ context.Context, params store.InsertAppParameters) (string, error) {
	f.apps[params.ID] = config.AppConfig{Id: params.ID, Name: params.Name}
	return params.ID, nil
}
func (f *fakeMgmtAppRepo) DeleteAppByID(_ context.Context, id string) error {
	delete(f.apps, id)
	return nil
}
func (f *fakeMgmtAppRepo) GetApps(_ context.Context) ([]config.AppDescriptor, error) { return nil, nil }
func (f *fakeMgmtAppRepo) UpdateAppNameByID(_ context.Context, id string, newName string) error {
	app := f.apps[id]
	app.Name = newName
	f.apps[id] = app
	return nil
}
func (f *fakeMgmtAppRepo) UpdateAppGitURLByID(_ context.Context, id string, gitURL string) error {
	app := f.apps[id]
	app.GitURL = gitURL
	f.apps[id] = app
	return nil
}
func (f *fakeMgmtAppRepo) GetAppByID(_ context.Context, id string) (config.AppConfig, error) {
	app, ok := f.apps[id]
	if !ok {
		return config.AppConfig{}, &store.ErrResourceNotFound{Resource: "app", Identifier: id}
	}
	return app, nil
}

type fakeMgmtChannelRepo struct{}

func (f *fakeMgmtChannelRepo) InsertChannel(_ context.Context, _ string, _ *int64, _ string) (int64, error) {
	return 11, nil
}
func (f *fakeMgmtChannelRepo) DeleteChannel(_ context.Context, _ string, _ string) error { return nil }
func (f *fakeMgmtChannelRepo) GetChannelNameByBranchName(_ context.Context, _ string, _ string) ([]string, error) {
	return nil, nil
}
func (f *fakeMgmtChannelRepo) GetChannels(_ context.Context, _ string) ([]types.ChannelMapping, error) {
	return nil, nil
}
func (f *fakeMgmtChannelRepo) GetChannelBranchMapping(_ context.Context, _ string, _ string) (*types.ChannelResolution, error) {
	return nil, nil
}
func (f *fakeMgmtChannelRepo) GetBranchSurfing(_ context.Context, _ string, _ string) (*types.BranchSurfing, error) {
	return &types.BranchSurfing{}, nil
}
func (f *fakeMgmtChannelRepo) SetBranchSurfing(_ context.Context, _ string, _ string, _ types.BranchSurfing) error {
	return nil
}

type fakeMgmtBranchRepo struct{}

func (f *fakeMgmtBranchRepo) GetSurfableBranches(_ context.Context, _ string, _ string, _ types.Platform) ([]types.SurfableBranch, error) {
	return nil, nil
}

func (f *fakeMgmtBranchRepo) InsertBranch(_ context.Context, _, _ string) (int64, error) {
	return 7, nil
}
func (f *fakeMgmtBranchRepo) UpsertBranchAndRuntimeVersion(_ context.Context, _ string, _ string, _ string) error {
	return nil
}
func (f *fakeMgmtBranchRepo) GetUpdateRefsByBranchName(_ context.Context, _ string, _ string) ([]types.UpdateRef, error) {
	return nil, nil
}
func (f *fakeMgmtBranchRepo) DeleteBranchByName(_ context.Context, _ string, _ string) error {
	return nil
}
func (f *fakeMgmtBranchRepo) GetBranches(_ context.Context, _ string) ([]types.BranchMapping, error) {
	return nil, nil
}
func (f *fakeMgmtBranchRepo) GetRuntimeVersionsWithUpdateStats(_ context.Context, _ string, _ string) ([]types.RuntimeVersionWithStats, error) {
	return nil, nil
}
func (f *fakeMgmtBranchRepo) UpdateChannelBranchMapping(_ context.Context, _ string, _ string, _ string) error {
	return nil
}
func (f *fakeMgmtBranchRepo) CreateRuntimeVersion(_ context.Context, _ string, _ string) (int64, error) {
	return 0, nil
}
func (f *fakeMgmtBranchRepo) GetBranchByName(_ context.Context, _ string, _ string) (int64, error) {
	return 3, nil
}

func adminManagementCtx() context.Context {
	return WithPrincipal(context.Background(),
		&DashboardPrincipal{UserId: "admin-1", Email: "admin@example.com"})
}

func TestAppLifecycleEmitsAuditEvents(t *testing.T) {
	// Database keys mode is only creatable in DB mode with a master key to
	// seal the app's signing keys; both are env facts ValidateKeys checks.
	t.Setenv("DB_URL", "postgres://fake")
	t.Setenv("DB_KEYS_MASTER_KEY_B64", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	recorder := &fakeAuditRecorder{}
	appService := NewAppService(&fakeMgmtAppRepo{apps: map[string]config.AppConfig{}})
	appService.SetOnAuditEvent(recorder.Record)
	ctx := adminManagementCtx()

	appId, err := appService.CreateApp(ctx, "My App", config.KeysConfig{Mode: config.KeysModeDatabase})
	require.NoError(t, err)
	require.Len(t, recorder.events, 1)
	created := recorder.events[0]
	assert.Equal(t, auditlog.ActionAppCreated, created.Action)
	assert.Equal(t, "admin-1", created.ActorID)
	assert.Equal(t, appId, created.TargetID)
	assert.Equal(t, "My App", created.TargetDisplay)
	assert.Equal(t, appId, created.AppID)
	assert.Equal(t, map[string]any{"keys_mode": "database"}, created.Metadata)

	// The rename carries both names; an idempotent rename emits nothing.
	app, err := appService.GetAppByID(ctx, appId)
	require.NoError(t, err)
	require.NoError(t, appService.UpdateAppName(ctx, app, "Renamed App"))
	require.Len(t, recorder.events, 2)
	renamed := recorder.events[1]
	assert.Equal(t, auditlog.ActionAppRenamed, renamed.Action)
	assert.Equal(t, map[string]any{"name": "Renamed App", "previous_name": "My App"}, renamed.Metadata)

	app, err = appService.GetAppByID(ctx, appId)
	require.NoError(t, err)
	require.NoError(t, appService.UpdateAppName(ctx, app, "Renamed App"))
	require.Len(t, recorder.events, 2)

	app, err = appService.GetAppByID(ctx, appId)
	require.NoError(t, err)
	require.NoError(t, appService.UpdateAppGitURL(ctx, app, "https://github.com/acme/mobile.git/"))
	require.Len(t, recorder.events, 3)
	gitURLUpdated := recorder.events[2]
	assert.Equal(t, auditlog.ActionAppGitURLUpdated, gitURLUpdated.Action)
	assert.Equal(t, map[string]any{
		"git_url":          "https://github.com/acme/mobile",
		"previous_git_url": "",
	}, gitURLUpdated.Metadata)

	app, err = appService.GetAppByID(ctx, appId)
	require.NoError(t, err)
	require.NoError(t, appService.UpdateAppGitURL(ctx, app, "https://github.com/acme/mobile"))
	require.Len(t, recorder.events, 3)

	require.NoError(t, appService.UpdateAppGitURL(ctx, app, ""))
	require.Len(t, recorder.events, 4)
	assert.Equal(t, auditlog.ActionAppGitURLUpdated, recorder.events[3].Action)

	// The deletion entry still names the app.
	app, err = appService.GetAppByID(ctx, appId)
	require.NoError(t, err)
	require.NoError(t, appService.DeleteApp(ctx, app))
	require.Len(t, recorder.events, 5)
	assert.Equal(t, auditlog.ActionAppDeleted, recorder.events[4].Action)
	assert.Equal(t, "Renamed App", recorder.events[4].TargetDisplay)
}

func TestChannelEventsEmitAuditEvents(t *testing.T) {
	recorder := &fakeAuditRecorder{}
	channelService := NewChannelService(&fakeMgmtBranchRepo{}, &fakeMgmtChannelRepo{})
	channelService.SetOnAuditEvent(recorder.Record)
	ctx := adminManagementCtx()
	branch := "main"

	_, err := channelService.CreateChannel(ctx, "app-1", &branch, "production")
	require.NoError(t, err)
	require.Len(t, recorder.events, 1)
	created := recorder.events[0]
	assert.Equal(t, auditlog.ActionChannelCreated, created.Action)
	assert.Equal(t, "production", created.TargetID)
	assert.Equal(t, "app-1", created.AppID)
	assert.Equal(t, map[string]any{"channel_id": "11", "branch": "main"}, created.Metadata)

	require.NoError(t, channelService.DeleteChannel(ctx, "production", "app-1"))
	require.Len(t, recorder.events, 2)
	assert.Equal(t, auditlog.ActionChannelDeleted, recorder.events[1].Action)
}

func TestBranchEventsEmitAuditEvents(t *testing.T) {
	recorder := &fakeAuditRecorder{}
	branchService := NewBranchService(&fakeMgmtBranchRepo{}, &fakeMgmtChannelRepo{}, nil, nil, nil)
	branchService.SetOnAuditEvent(recorder.Record)
	ctx := adminManagementCtx()

	_, err := branchService.CreateBranch(ctx, "app-1", "main")
	require.NoError(t, err)
	require.Len(t, recorder.events, 1)
	assert.Equal(t, auditlog.ActionBranchCreated, recorder.events[0].Action)
	assert.Equal(t, "main", recorder.events[0].TargetID)
	assert.Equal(t, map[string]any{"branch_id": "7"}, recorder.events[0].Metadata)

	require.NoError(t, branchService.DeleteBranch(ctx, "main", "app-1"))
	require.Len(t, recorder.events, 2)
	assert.Equal(t, auditlog.ActionBranchDeleted, recorder.events[1].Action)

	require.NoError(t, branchService.UpdateChannelBranchMapping(ctx, "app-1", "11", "production", "7"))
	require.Len(t, recorder.events, 3)
	mapped := recorder.events[2]
	assert.Equal(t, auditlog.ActionChannelBranchMapped, mapped.Action)
	assert.Equal(t, "production", mapped.TargetID)
	assert.Equal(t, map[string]any{"channel_id": "11", "branch_id": "7"}, mapped.Metadata)
}

func TestManagementEventsResolveCliActor(t *testing.T) {
	// The publish paths authenticate with app-scoped API keys, not dashboard
	// sessions: the shared actor resolution must name them honestly.
	recorder := &fakeAuditRecorder{}
	branchService := NewBranchService(&fakeMgmtBranchRepo{}, &fakeMgmtChannelRepo{}, nil, nil, nil)
	branchService.SetOnAuditEvent(recorder.Record)

	// A named DB-mode key resolves to its identity, like the principal email.
	namedCtx := WithCliAuth(context.Background(),
		CliCredential{AppID: "app-1", KeyID: 42, KeyName: "ci-production"})
	_, err := branchService.CreateBranch(namedCtx, "app-1", "main")
	require.NoError(t, err)
	require.Len(t, recorder.events, 1)
	assert.Equal(t, auditlog.ActorAPIKey, recorder.events[0].ActorType)
	assert.Equal(t, "42", recorder.events[0].ActorID)
	assert.Equal(t, "ci-production", recorder.events[0].ActorDisplay)

	// A nameless credential (stateless Expo token) falls back to the app scope.
	_, err = branchService.CreateBranch(WithCliAuth(context.Background(), CliCredential{AppID: "app-1"}), "app-1", "main")
	require.NoError(t, err)
	require.Len(t, recorder.events, 2)
	assert.Empty(t, recorder.events[1].ActorID)
	assert.Equal(t, "api key (app app-1)", recorder.events[1].ActorDisplay)
}
