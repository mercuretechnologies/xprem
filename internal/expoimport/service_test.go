package expoimport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"xprem/config"
	"xprem/internal/bucket"
	"xprem/internal/providers/expo"
	"xprem/internal/services"
	"xprem/internal/store"
	"xprem/internal/types"
	"xprem/internal/validation"

	"github.com/google/uuid"
	"github.com/jarcoal/httpmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const importExpoAppID = "9f0e1d2c-0000-0000-0000-00000000abcd"

func expoAuth(token string) types.Auth {
	return types.Auth{Token: &token}
}

type importFakeAppRepo struct {
	inserted  []store.InsertAppParameters
	deleted   []string
	insertErr error
	// missing makes GetAppByID answer not-found.
	missing bool
}

func (f *importFakeAppRepo) InsertApp(_ context.Context, app store.InsertAppParameters) (string, error) {
	if f.insertErr != nil {
		return "", f.insertErr
	}
	f.inserted = append(f.inserted, app)
	return app.ID, nil
}
func (f *importFakeAppRepo) DeleteAppByID(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}
func (f *importFakeAppRepo) GetApps(_ context.Context) ([]config.AppDescriptor, error) {
	panic("unused")
}
func (f *importFakeAppRepo) UpdateAppNameByID(_ context.Context, _ string, _ string) error {
	panic("unused")
}
func (f *importFakeAppRepo) GetAppByID(_ context.Context, id string) (config.AppConfig, error) {
	if f.missing {
		return config.AppConfig{}, fmt.Errorf("app %s not found", id)
	}
	return config.AppConfig{Id: id, Name: "My Imported App"}, nil
}

type importFakeBranchRepo struct {
	nextId   int64
	byName   map[string]int64
	inserted []string
	upserted []string
}

func (f *importFakeBranchRepo) InsertBranch(_ context.Context, _ string, branchName string) (int64, error) {
	f.nextId++
	if f.byName == nil {
		f.byName = map[string]int64{}
	}
	f.byName[branchName] = f.nextId
	f.inserted = append(f.inserted, branchName)
	return f.nextId, nil
}
func (f *importFakeBranchRepo) GetBranchByName(_ context.Context, _ string, branchName string) (int64, error) {
	id, ok := f.byName[branchName]
	if !ok {
		return 0, fmt.Errorf("branch %q not found", branchName)
	}
	return id, nil
}
func (f *importFakeBranchRepo) UpsertBranchAndRuntimeVersion(_ context.Context, _ string, branchName string, runtimeVersion string) error {
	f.upserted = append(f.upserted, branchName+"@"+runtimeVersion)
	return nil
}
func (f *importFakeBranchRepo) GetUpdateRefsByBranchName(_ context.Context, _ string, _ string) ([]types.UpdateRef, error) {
	panic("unused")
}
func (f *importFakeBranchRepo) DeleteBranchByName(_ context.Context, _ string, _ string) error {
	panic("unused")
}
func (f *importFakeBranchRepo) GetBranches(_ context.Context, _ string) ([]types.BranchMapping, error) {
	panic("unused")
}
func (f *importFakeBranchRepo) GetSurfableBranches(_ context.Context, _ string, _ string, _ types.Platform) ([]types.SurfableBranch, error) {
	panic("unused")
}
func (f *importFakeBranchRepo) GetRuntimeVersionsWithUpdateStats(_ context.Context, _ string, _ string) ([]types.RuntimeVersionWithStats, error) {
	panic("unused")
}
func (f *importFakeBranchRepo) UpdateChannelBranchMapping(_ context.Context, _ string, _ string, _ string) error {
	panic("unused")
}
func (f *importFakeBranchRepo) CreateRuntimeVersion(_ context.Context, _ string, _ string) (int64, error) {
	panic("unused")
}

type importedChannel struct {
	name     string
	branchId *int64
}

type importFakeChannelRepo struct {
	nextId    int64
	inserted  []importedChannel
	insertErr error
}

func (f *importFakeChannelRepo) InsertChannel(_ context.Context, _ string, branchId *int64, channelName string) (int64, error) {
	if f.insertErr != nil {
		return 0, f.insertErr
	}
	f.nextId++
	f.inserted = append(f.inserted, importedChannel{name: channelName, branchId: branchId})
	return f.nextId, nil
}
func (f *importFakeChannelRepo) DeleteChannel(_ context.Context, _ string, _ string) error {
	panic("unused")
}
func (f *importFakeChannelRepo) GetChannelNameByBranchName(_ context.Context, _ string, _ string) ([]string, error) {
	panic("unused")
}
func (f *importFakeChannelRepo) GetChannels(_ context.Context, _ string) ([]types.ChannelMapping, error) {
	panic("unused")
}
func (f *importFakeChannelRepo) GetChannelBranchMapping(_ context.Context, _ string, _ string) (*types.ChannelResolution, error) {
	panic("unused")
}
func (f *importFakeChannelRepo) GetBranchSurfing(_ context.Context, _ string, _ string) (*types.BranchSurfing, error) {
	panic("unused")
}
func (f *importFakeChannelRepo) SetBranchSurfing(_ context.Context, _ string, _ string, _ types.BranchSurfing) error {
	panic("unused")
}

func importService(t *testing.T, appRepo *importFakeAppRepo, branchRepo *importFakeBranchRepo, channelRepo *importFakeChannelRepo) *Service {
	t.Helper()
	t.Setenv("DB_URL", "postgres://stub")
	appService := services.NewAppService(appRepo)
	branchService := services.NewBranchService(branchRepo, channelRepo, nil, nil, nil)
	channelService := services.NewChannelService(branchRepo, channelRepo)
	return NewService(appService, branchService, channelService, nil, nil, nil)
}

// No jobs client: tests run the job body directly or stop before the enqueue.
func historyImportService(t *testing.T, branchRepo *importFakeBranchRepo, updateRepo services.UpdateRepository, historyBucket bucket.Bucket) *Service {
	t.Helper()
	t.Setenv("DB_URL", "postgres://stub")
	appService := services.NewAppService(&importFakeAppRepo{})
	branchService := services.NewBranchService(branchRepo, &importFakeChannelRepo{}, nil, nil, nil)
	channelService := services.NewChannelService(branchRepo, &importFakeChannelRepo{})
	return NewService(appService, branchService, channelService, updateRepo, nil, historyBucket)
}

func awsKeysConfig() config.KeysConfig {
	return config.KeysConfig{
		Mode:            config.KeysModeAWSSM,
		PublicSecretId:  "public-secret",
		PrivateSecretId: "private-secret",
	}
}

func mockExpoProjectStructure(t *testing.T) {
	t.Helper()
	httpmock.RegisterResponder("POST", "https://api.expo.dev/graphql",
		func(req *http.Request) (*http.Response, error) {
			switch req.Header.Get("operationName") {
			case "FetchExpoProjectStructure":
				return httpmock.NewJsonResponse(http.StatusOK, map[string]interface{}{
					"data": map[string]interface{}{
						"app": map[string]interface{}{
							"byId": map[string]interface{}{
								"id":   importExpoAppID,
								"name": "My Imported App",
								"updateBranches": []map[string]interface{}{
									{"id": "b1", "name": "production"},
									{"id": "b2", "name": "preview"},
									{"id": "b3", "name": "bad*branch"},
								},
								"updateChannels": []map[string]interface{}{
									{"id": "c1", "name": "production", "branchMapping": `{"version":0,"data":[{"branchId":"b1","branchMappingLogic":"true"}]}`},
									{"id": "c2", "name": "staging", "branchMapping": `{"version":0,"data":[{"branchId":"b3","branchMappingLogic":"true"}]}`},
									{"id": "c3", "name": "orphan", "branchMapping": `{"version":0,"data":[]}`},
								},
							},
						},
					},
				})
			case "FetchExpoAccountApps":
				return httpmock.NewJsonResponse(http.StatusOK, map[string]interface{}{
					"data": map[string]interface{}{
						"me": map[string]interface{}{
							"id": "user-1",
							"accounts": []map[string]interface{}{
								{
									"id":   "account-1",
									"name": "my-org",
									"apps": []map[string]interface{}{
										{"id": importExpoAppID, "name": "My Imported App", "fullName": "@my-org/my-imported-app"},
									},
								},
								{
									"id":   "account-2",
									"name": "empty-org",
									"apps": []map[string]interface{}{},
								},
							},
						},
					},
				})
			}
			return httpmock.NewStringResponse(http.StatusNotFound, "unknown operation"), nil
		})
}

func TestImportAppCopiesStructure(t *testing.T) {
	httpmock.Activate()
	defer httpmock.DeactivateAndReset()
	mockExpoProjectStructure(t)

	appRepo := &importFakeAppRepo{}
	branchRepo := &importFakeBranchRepo{}
	channelRepo := &importFakeChannelRepo{}
	service := importService(t, appRepo, branchRepo, channelRepo)

	result, err := service.ImportApp(context.Background(), expoAuth("token"), importExpoAppID, awsKeysConfig(), 0)

	require.NoError(t, err)
	assert.Equal(t, importExpoAppID, result.AppId)
	assert.Equal(t, "My Imported App", result.Name)
	assert.Equal(t, 2, result.BranchCount)
	assert.Equal(t, 3, result.ChannelCount)
	// bad*branch is skipped (reserved wildcard); staging is created unmapped.
	require.Len(t, result.Skipped, 1)
	assert.Contains(t, result.Skipped[0], "bad*branch")
	require.Len(t, result.Warnings, 1)
	assert.Contains(t, result.Warnings[0], `channel "staging"`)

	require.Len(t, appRepo.inserted, 1)
	assert.Equal(t, importExpoAppID, appRepo.inserted[0].ID)
	assert.Equal(t, "My Imported App", appRepo.inserted[0].Name)

	assert.Equal(t, []string{"production", "preview"}, branchRepo.inserted)

	require.Len(t, channelRepo.inserted, 3)
	assert.Equal(t, "production", channelRepo.inserted[0].name)
	require.NotNil(t, channelRepo.inserted[0].branchId)
	assert.Equal(t, branchRepo.byName["production"], *channelRepo.inserted[0].branchId)
	assert.Equal(t, "staging", channelRepo.inserted[1].name)
	assert.Nil(t, channelRepo.inserted[1].branchId)
	assert.Equal(t, "orphan", channelRepo.inserted[2].name)
	assert.Nil(t, channelRepo.inserted[2].branchId)
}

// The Expo project id is reused as the local app id, so non-canonical forms
// must collapse to the lowercase UUID the row is keyed and sealed under.
func TestImportAppCanonicalizesUppercaseId(t *testing.T) {
	httpmock.Activate()
	defer httpmock.DeactivateAndReset()
	mockExpoProjectStructure(t)

	appRepo := &importFakeAppRepo{}
	service := importService(t, appRepo, &importFakeBranchRepo{}, &importFakeChannelRepo{})

	result, err := service.ImportApp(context.Background(), expoAuth("token"), "9F0E1D2C-0000-0000-0000-00000000ABCD", awsKeysConfig(), 0)

	require.NoError(t, err)
	assert.Equal(t, importExpoAppID, result.AppId)
}

func TestImportAppRejectsNonUUIDProjectId(t *testing.T) {
	service := importService(t, &importFakeAppRepo{}, &importFakeBranchRepo{}, &importFakeChannelRepo{})

	_, err := service.ImportApp(context.Background(), expoAuth("token"), "@my-org/my-app", awsKeysConfig(), 0)

	require.Error(t, err)
	assert.True(t, validation.IsValidationError(err))
}

func TestImportRequiresControlPlane(t *testing.T) {
	service := importService(t, &importFakeAppRepo{}, &importFakeBranchRepo{}, &importFakeChannelRepo{})
	t.Setenv("DB_URL", "")

	_, err := service.ListImportableApps(context.Background(), expoAuth("token"))
	require.ErrorIs(t, err, store.ErrNotSupportedInStatelessMode)
	_, err = service.PreviewImport(context.Background(), expoAuth("token"), importExpoAppID)
	require.ErrorIs(t, err, store.ErrNotSupportedInStatelessMode)
	_, err = service.ImportApp(context.Background(), expoAuth("token"), importExpoAppID, awsKeysConfig(), 0)
	require.ErrorIs(t, err, store.ErrNotSupportedInStatelessMode)
}

func TestImportAppSurfacesExpoErrors(t *testing.T) {
	httpmock.Activate()
	defer httpmock.DeactivateAndReset()
	httpmock.RegisterResponder("POST", "https://api.expo.dev/graphql",
		func(req *http.Request) (*http.Response, error) {
			return httpmock.NewJsonResponse(http.StatusOK, map[string]interface{}{
				"errors": []map[string]interface{}{{"message": "UNAUTHORIZED"}},
			})
		})

	service := importService(t, &importFakeAppRepo{}, &importFakeBranchRepo{}, &importFakeChannelRepo{})

	_, err := service.ImportApp(context.Background(), expoAuth("bad-token"), importExpoAppID, awsKeysConfig(), 0)

	var apiErr *expo.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusBadRequest, apiErr.StatusHint)
	assert.Contains(t, apiErr.Message, "UNAUTHORIZED")
}

func TestImportAppReportsUnknownProject(t *testing.T) {
	httpmock.Activate()
	defer httpmock.DeactivateAndReset()
	httpmock.RegisterResponder("POST", "https://api.expo.dev/graphql",
		func(req *http.Request) (*http.Response, error) {
			return httpmock.NewJsonResponse(http.StatusOK, map[string]interface{}{
				"data": map[string]interface{}{"app": map[string]interface{}{"byId": nil}},
			})
		})

	service := importService(t, &importFakeAppRepo{}, &importFakeBranchRepo{}, &importFakeChannelRepo{})

	_, err := service.ImportApp(context.Background(), expoAuth("token"), importExpoAppID, awsKeysConfig(), 0)

	var apiErr *expo.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusNotFound, apiErr.StatusHint)
}

func TestImportAppPropagatesAlreadyExists(t *testing.T) {
	httpmock.Activate()
	defer httpmock.DeactivateAndReset()
	mockExpoProjectStructure(t)

	appRepo := &importFakeAppRepo{insertErr: &store.ErrResourceAlreadyExists{Resource: "app", Identifier: importExpoAppID}}
	service := importService(t, appRepo, &importFakeBranchRepo{}, &importFakeChannelRepo{})

	_, err := service.ImportApp(context.Background(), expoAuth("token"), importExpoAppID, awsKeysConfig(), 0)

	alreadyExists := (*store.ErrResourceAlreadyExists)(nil)
	require.ErrorAs(t, err, &alreadyExists)
	assert.Empty(t, appRepo.deleted)
}

func TestImportAppRollsBackOnHardFailure(t *testing.T) {
	httpmock.Activate()
	defer httpmock.DeactivateAndReset()
	mockExpoProjectStructure(t)

	appRepo := &importFakeAppRepo{}
	channelRepo := &importFakeChannelRepo{insertErr: errors.New("connection lost")}
	service := importService(t, appRepo, &importFakeBranchRepo{}, channelRepo)

	_, err := service.ImportApp(context.Background(), expoAuth("token"), importExpoAppID, awsKeysConfig(), 0)

	require.Error(t, err)
	require.Len(t, appRepo.inserted, 1)
	assert.Equal(t, []string{importExpoAppID}, appRepo.deleted)
}

func TestPreviewImportBuildsPlan(t *testing.T) {
	httpmock.Activate()
	defer httpmock.DeactivateAndReset()
	mockExpoProjectStructure(t)

	appRepo := &importFakeAppRepo{missing: true}
	service := importService(t, appRepo, &importFakeBranchRepo{}, &importFakeChannelRepo{})

	plan, err := service.PreviewImport(context.Background(), expoAuth("token"), importExpoAppID)

	require.NoError(t, err)
	assert.Empty(t, plan.Conflict)
	assert.Equal(t, importExpoAppID, plan.AppId)
	assert.Equal(t, "My Imported App", plan.Name)
	assert.Equal(t, "My Imported App", plan.ExpoName)

	require.Len(t, plan.Branches, 3)
	assert.Empty(t, plan.Branches[0].SkipReason)
	assert.Empty(t, plan.Branches[1].SkipReason)
	assert.NotEmpty(t, plan.Branches[2].SkipReason, "bad*branch must be flagged before the import runs")

	require.Len(t, plan.Channels, 3)
	assert.Equal(t, "production", plan.Channels[0].MappedBranch)
	// staging maps to the skipped branch: still created, but unmapped.
	assert.Empty(t, plan.Channels[1].SkipReason)
	assert.Empty(t, plan.Channels[1].MappedBranch)
	assert.Contains(t, plan.Channels[1].Warning, "bad*branch")
	assert.Empty(t, plan.Channels[2].MappedBranch)

	assert.Empty(t, appRepo.inserted)
}

func TestBuildImportPlanSkipsReservedBranchName(t *testing.T) {
	plan := buildImportPlan(uuid.MustParse(importExpoAppID), &expo.ProjectStructure{Name: "App", Branches: []string{"cas"}})
	require.Len(t, plan.Branches, 1)
	assert.Contains(t, plan.Branches[0].SkipReason, "reserved")
}

func TestBuildImportPlanEmptySlices(t *testing.T) {
	plan := buildImportPlan(uuid.MustParse(importExpoAppID), &expo.ProjectStructure{Name: "Empty"})
	require.NotNil(t, plan.Branches)
	require.NotNil(t, plan.Channels)
	assert.Empty(t, plan.Branches)
	assert.Empty(t, plan.Channels)
}

func TestPreviewImportReportsConflict(t *testing.T) {
	httpmock.Activate()
	defer httpmock.DeactivateAndReset()
	mockExpoProjectStructure(t)

	service := importService(t, &importFakeAppRepo{}, &importFakeBranchRepo{}, &importFakeChannelRepo{})

	plan, err := service.PreviewImport(context.Background(), expoAuth("token"), importExpoAppID)

	require.NoError(t, err)
	assert.Contains(t, plan.Conflict, "already exists")
}

func TestImportAppValidatesHistoryLimit(t *testing.T) {
	appRepo := &importFakeAppRepo{}
	service := importService(t, appRepo, &importFakeBranchRepo{}, &importFakeChannelRepo{})

	_, err := service.ImportApp(context.Background(), expoAuth("token"), importExpoAppID, awsKeysConfig(), MaxHistoryImportGroups+1)

	require.Error(t, err)
	assert.True(t, validation.IsValidationError(err))
	assert.Empty(t, appRepo.inserted)
}

// When the history job cannot start, the whole import fails and the app is
// rolled back.
func TestImportAppRollsBackWhenHistoryJobCannotStart(t *testing.T) {
	httpmock.Activate()
	defer httpmock.DeactivateAndReset()
	mockExpoProjectStructure(t)

	appRepo := &importFakeAppRepo{}
	service := importService(t, appRepo, &importFakeBranchRepo{}, &importFakeChannelRepo{})

	_, err := service.ImportApp(context.Background(), expoAuth("token"), importExpoAppID, awsKeysConfig(), 10)

	require.ErrorContains(t, err, "failed to start the update history import")
	require.Len(t, appRepo.inserted, 1)
	assert.Equal(t, []string{importExpoAppID}, appRepo.deleted)
}

func TestListImportableApps(t *testing.T) {
	httpmock.Activate()
	defer httpmock.DeactivateAndReset()
	mockExpoProjectStructure(t)

	service := importService(t, &importFakeAppRepo{}, &importFakeBranchRepo{}, &importFakeChannelRepo{})

	accounts, err := service.ListImportableApps(context.Background(), expoAuth("token"))

	require.NoError(t, err)
	require.Len(t, accounts, 2)
	assert.Equal(t, "my-org", accounts[0].AccountName)
	require.Len(t, accounts[0].Apps, 1)
	assert.Equal(t, importExpoAppID, accounts[0].Apps[0].Id)
	// Non-nil even when empty: the dashboard reads apps.length on every account.
	require.NotNil(t, accounts[1].Apps)
	assert.Empty(t, accounts[1].Apps)

	blank := "  "
	_, err = service.ListImportableApps(context.Background(), types.Auth{Token: &blank})
	assert.True(t, validation.IsValidationError(err))

	_, err = service.ListImportableApps(context.Background(), types.Auth{})
	assert.True(t, validation.IsValidationError(err))
}

// The Expo API caps the apps field at 50 per call; more apps are read page by page.
func TestListImportableAppsPaginatesPastFiftyApps(t *testing.T) {
	httpmock.Activate()
	defer httpmock.DeactivateAndReset()
	httpmock.RegisterResponder("POST", "https://api.expo.dev/graphql",
		func(req *http.Request) (*http.Response, error) {
			var body struct {
				Variables struct {
					Offset int `json:"offset"`
					Limit  int `json:"limit"`
				} `json:"variables"`
			}
			require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
			require.LessOrEqual(t, body.Variables.Limit, 50)
			pageSize := body.Variables.Limit
			if body.Variables.Offset >= 50 {
				pageSize = 10
			}
			apps := make([]map[string]interface{}, 0, pageSize)
			for i := 0; i < pageSize; i++ {
				id := fmt.Sprintf("app-%d", body.Variables.Offset+i)
				apps = append(apps, map[string]interface{}{"id": id, "name": id, "fullName": "@my-org/" + id})
			}
			return httpmock.NewJsonResponse(http.StatusOK, map[string]interface{}{
				"data": map[string]interface{}{
					"me": map[string]interface{}{
						"id": "user-1",
						"accounts": []map[string]interface{}{
							{"id": "account-1", "name": "my-org", "apps": apps},
						},
					},
				},
			})
		})

	service := importService(t, &importFakeAppRepo{}, &importFakeBranchRepo{}, &importFakeChannelRepo{})

	accounts, err := service.ListImportableApps(context.Background(), expoAuth("token"))

	require.NoError(t, err)
	require.Len(t, accounts, 1)
	require.Len(t, accounts[0].Apps, 60)
	assert.Equal(t, "app-0", accounts[0].Apps[0].Id)
	assert.Equal(t, "app-59", accounts[0].Apps[59].Id)
}
