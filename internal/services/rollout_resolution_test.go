package services

// Fake-repo coverage of the progressive rollout decision tree (plan section 9). The
// fakes model the store contract that the Postgres integration tests pin down (an
// active per-update rollout is a row with rollout_percentage set AND checked), so the
// service logic runs in CI without a database. Every harness gets a fresh app id, which
// keeps the process-global lastUpdate cache from leaking state between tests.

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"xprem/config"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/providers/expo"
	"xprem/internal/rollout"
	"xprem/internal/store"
	"xprem/internal/types"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// eventLog records the order of repo mutations so ordering-sensitive flows (revert
// republishes the control BEFORE clearing the rollout) can be asserted.
type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *eventLog) add(event string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

func (l *eventLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

type fakeStoredUpdate struct {
	update            types.Update
	updateType        types.UpdateType
	platform          string
	checked           bool
	rolloutPercentage *int
	controlUpdateId   *string
	updateUUID        string
	publishGroup      *string
}

type fakeUpdateRepo struct {
	mu     sync.Mutex
	rows   []*fakeStoredUpdate
	events *eventLog
}

func (r *fakeUpdateRepo) findRowLocked(appId, branchName, updateId string) *fakeStoredUpdate {
	for _, row := range r.rows {
		if row.update.AppId == appId && row.update.Branch == branchName && row.update.UpdateId == updateId {
			return row
		}
	}
	return nil
}

// latestCheckedLocked mirrors the GetLatestUpdateWithRollout SQL: newest checked row
// for (branch, rtv, platform) by numeric id.
func (r *fakeUpdateRepo) latestCheckedLocked(appId, branchName, runtimeVersion, platform string) *fakeStoredUpdate {
	var latest *fakeStoredUpdate
	var latestId int64 = -1
	for _, row := range r.rows {
		if row.update.AppId != appId || row.update.Branch != branchName ||
			row.update.RuntimeVersion != runtimeVersion || row.platform != platform || !row.checked {
			continue
		}
		id, err := strconv.ParseInt(row.update.UpdateId, 10, 64)
		if err != nil {
			continue
		}
		if id > latestId {
			latestId = id
			latest = row
		}
	}
	return latest
}

func (r *fakeUpdateRepo) appendRowLocked(appId string, updateId int64, branchName, runtimeVersion, platform string, updateType types.UpdateType) *fakeStoredUpdate {
	row := &fakeStoredUpdate{
		update: types.Update{
			AppId:          appId,
			Branch:         branchName,
			RuntimeVersion: runtimeVersion,
			UpdateId:       strconv.FormatInt(updateId, 10),
			CreatedAt:      time.Duration(updateId),
		},
		updateType: updateType,
		platform:   platform,
	}
	r.rows = append(r.rows, row)
	return row
}

func (r *fakeUpdateRepo) MarkUpdateAsChecked(_ context.Context, update types.Update) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	row := r.findRowLocked(update.AppId, update.Branch, update.UpdateId)
	if row == nil {
		return fmt.Errorf("update %s not found", update.UpdateId)
	}
	// The partial unique index uq_updates_active_rollout: at most one row per
	// (branch, rtv, platform) with rollout_percentage set AND checked_at set.
	if row.rolloutPercentage != nil {
		for _, other := range r.rows {
			if other != row && other.update.AppId == row.update.AppId && other.update.Branch == row.update.Branch &&
				other.update.RuntimeVersion == row.update.RuntimeVersion && other.platform == row.platform &&
				other.checked && other.rolloutPercentage != nil {
				return &pgconn.PgError{Code: "23505"}
			}
		}
	}
	row.checked = true
	if r.events != nil {
		r.events.add("markChecked:" + row.update.UpdateId)
	}
	return nil
}

func (r *fakeUpdateRepo) GetUpdateDetails(_ context.Context, _, _, _, _ string) (types.UpdateDetails, error) {
	return types.UpdateDetails{}, nil
}

func (r *fakeUpdateRepo) GetUpdate(_ context.Context, appId, branchName, _, updateId string) (*types.Update, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row := r.findRowLocked(appId, branchName, updateId)
	if row == nil {
		return nil, nil
	}
	updateCopy := row.update
	return &updateCopy, nil
}

func (r *fakeUpdateRepo) GetLatestUpdate(_ context.Context, appId, branchName, runtimeVersion, platform string) (*types.Update, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row := r.latestCheckedLocked(appId, branchName, runtimeVersion, platform)
	if row == nil {
		return nil, nil
	}
	updateCopy := row.update
	return &updateCopy, nil
}

func (r *fakeUpdateRepo) GetLatestUpdateWithRollout(_ context.Context, appId, branchName, runtimeVersion, platform string) (*types.UpdateWithRollout, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row := r.latestCheckedLocked(appId, branchName, runtimeVersion, platform)
	if row == nil {
		return nil, nil
	}
	envelope := &types.UpdateWithRollout{Update: row.update}
	if row.rolloutPercentage != nil {
		pct := *row.rolloutPercentage
		envelope.RolloutPercentage = &pct
	}
	if row.controlUpdateId != nil {
		if control := r.findRowLocked(appId, branchName, *row.controlUpdateId); control != nil {
			controlCopy := control.update
			envelope.Control = &controlCopy
		}
	}
	return envelope, nil
}

func (r *fakeUpdateRepo) GetUpdateByUUID(_ context.Context, appId, updateUUID string) (*types.Update, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, row := range r.rows {
		if row.update.AppId == appId && row.updateUUID == updateUUID && row.checked {
			updateCopy := row.update
			return &updateCopy, nil
		}
	}
	return nil, nil
}

func (r *fakeUpdateRepo) HasActiveRolloutUpdate(_ context.Context, appId, branchName, runtimeVersion string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, row := range r.rows {
		if row.update.AppId == appId && row.update.Branch == branchName && row.update.RuntimeVersion == runtimeVersion &&
			row.checked && row.rolloutPercentage != nil {
			return true, nil
		}
	}
	return false, nil
}

func (r *fakeUpdateRepo) GetUpdateType(_ context.Context, update types.Update) (types.UpdateType, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row := r.findRowLocked(update.AppId, update.Branch, update.UpdateId)
	if row == nil {
		return types.NormalUpdate, fmt.Errorf("update %s not found", update.UpdateId)
	}
	return row.updateType, nil
}

func (r *fakeUpdateRepo) IsUpdateValid(_ context.Context, update types.Update) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row := r.findRowLocked(update.AppId, update.Branch, update.UpdateId)
	return row != nil && row.checked, nil
}

func (r *fakeUpdateRepo) CreateUpdate(_ context.Context, appId string, updateId int64, branchName, runtimeVersion, platform, _, _ string, publishGroup *string) (*types.Update, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row := r.appendRowLocked(appId, updateId, branchName, runtimeVersion, platform, types.NormalUpdate)
	row.publishGroup = publishGroup
	if r.events != nil {
		r.events.add("createUpdate:" + row.update.UpdateId)
	}
	updateCopy := row.update
	return &updateCopy, nil
}

func (r *fakeUpdateRepo) CreateUpdateWithRollout(_ context.Context, appId string, updateId int64, branchName, runtimeVersion, platform, _, _ string, rolloutPercentage int, publishGroup *string) (*types.Update, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// The control resolves at insert time to the latest checked update of the same
	// (branch, rtv, platform), exactly like the InsertUpdateWithRollout CTE.
	var controlId *string
	if control := r.latestCheckedLocked(appId, branchName, runtimeVersion, platform); control != nil {
		id := control.update.UpdateId
		controlId = &id
	}
	row := r.appendRowLocked(appId, updateId, branchName, runtimeVersion, platform, types.NormalUpdate)
	row.rolloutPercentage = &rolloutPercentage
	row.controlUpdateId = controlId
	row.publishGroup = publishGroup
	if r.events != nil {
		r.events.add("createUpdateWithRollout:" + row.update.UpdateId)
	}
	updateCopy := row.update
	return &updateCopy, nil
}

func (r *fakeUpdateRepo) CreateRollback(_ context.Context, appId string, updateId int64, branchName, runtimeVersion, platform, _, _ string) (*types.Update, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row := r.appendRowLocked(appId, updateId, branchName, runtimeVersion, platform, types.Rollback)
	if r.events != nil {
		r.events.add("createRollback:" + row.update.UpdateId)
	}
	updateCopy := row.update
	return &updateCopy, nil
}

func (r *fakeUpdateRepo) GetUpdateByBranchNameAndRuntime(_ context.Context, _ string, _ int64, _, _ string) (pgdb.GetUpdateByBranchNameAndRuntimeRow, error) {
	return pgdb.GetUpdateByBranchNameAndRuntimeRow{}, nil
}

func (r *fakeUpdateRepo) GetUpdatesByRunTimeVersionAndBranchName(_ context.Context, _, _, _ string) ([]types.UpdateItem, error) {
	return nil, nil
}

func (r *fakeUpdateRepo) GetUpdateFeed(_ context.Context, _ string, _ types.UpdateFeedQuery) ([]types.UpdateFeedItem, error) {
	return nil, nil
}

// GetUpdatesByPublishGroup mirrors the SQL: checked members of the group on
// (branch, rtv), ordered by numeric id.
func (r *fakeUpdateRepo) GetUpdatesByPublishGroup(_ context.Context, appId, branchName, runtimeVersion, publishGroup string) ([]types.PublishGroupMember, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var members []types.PublishGroupMember
	for _, row := range r.rows {
		if row.update.AppId != appId || row.update.Branch != branchName ||
			row.update.RuntimeVersion != runtimeVersion || !row.checked ||
			row.publishGroup == nil || *row.publishGroup != publishGroup {
			continue
		}
		members = append(members, types.PublishGroupMember{
			UpdateId:   row.update.UpdateId,
			Platform:   row.platform,
			CommitHash: "abc123",
		})
	}
	sort.Slice(members, func(i, j int) bool {
		a, _ := strconv.ParseInt(members[i].UpdateId, 10, 64)
		b, _ := strconv.ParseInt(members[j].UpdateId, 10, 64)
		return a < b
	})
	return members, nil
}

func (r *fakeUpdateRepo) RetrieveUpdateStoredMetadata(_ context.Context, update types.Update) (*types.UpdateStoredMetadata, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row := r.findRowLocked(update.AppId, update.Branch, update.UpdateId)
	if row == nil {
		return nil, fmt.Errorf("update %s not found", update.UpdateId)
	}
	return &types.UpdateStoredMetadata{Platform: row.platform, CommitHash: "abc123", UpdateUUID: row.updateUUID}, nil
}

func (r *fakeUpdateRepo) StoreUpdateUUIDInMetadata(_ context.Context, update types.Update, updateUUID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if row := r.findRowLocked(update.AppId, update.Branch, update.UpdateId); row != nil {
		row.updateUUID = updateUUID
	}
	return nil
}

type fakeRolloutRepo struct {
	updateRepo *fakeUpdateRepo
	events     *eventLog
	// Force the 0-rows result of the guarded UPDATEs to simulate a concurrent end.
	setReturnsZeroRows   bool
	clearReturnsZeroRows bool
}

func (r *fakeRolloutRepo) CreateChannelRollout(_ context.Context, _, _, _, _ string, _ int) (int64, error) {
	return 0, nil
}

func (r *fakeRolloutRepo) GetChannelRollout(_ context.Context, _, _ string) (*types.ChannelRollout, error) {
	return nil, nil
}

func (r *fakeRolloutRepo) UpdateChannelRolloutPercentage(_ context.Context, _, _ string, _ int) (int64, error) {
	return 0, nil
}

func (r *fakeRolloutRepo) DeleteChannelRollout(_ context.Context, _, _ string) (int64, error) {
	return 0, nil
}

func (r *fakeRolloutRepo) PromoteChannelRollout(_ context.Context, _, _ string) (int64, error) {
	return 0, nil
}

func (r *fakeRolloutRepo) GetChannelRolloutsByBranch(_ context.Context, _, _ string) ([]string, error) {
	return nil, nil
}

func (r *fakeRolloutRepo) activeRows(appId, branchName, runtimeVersion string) []*fakeStoredUpdate {
	r.updateRepo.mu.Lock()
	defer r.updateRepo.mu.Unlock()
	var active []*fakeStoredUpdate
	for _, row := range r.updateRepo.rows {
		if row.update.AppId == appId && row.update.Branch == branchName && row.update.RuntimeVersion == runtimeVersion &&
			row.checked && row.rolloutPercentage != nil {
			active = append(active, row)
		}
	}
	sort.Slice(active, func(i, j int) bool { return active[i].platform < active[j].platform })
	return active
}

func (r *fakeRolloutRepo) GetActiveRolloutUpdates(_ context.Context, appId, branchName, runtimeVersion string) ([]types.RolloutUpdate, error) {
	rolloutUpdates := make([]types.RolloutUpdate, 0)
	for _, row := range r.activeRows(appId, branchName, runtimeVersion) {
		item := types.RolloutUpdate{
			UpdateId:   row.update.UpdateId,
			Platform:   row.platform,
			Percentage: *row.rolloutPercentage,
			CreatedAt:  time.Unix(0, int64(row.update.CreatedAt)).UTC().Format(time.RFC3339),
		}
		if row.controlUpdateId != nil {
			controlId := *row.controlUpdateId
			item.ControlUpdateId = &controlId
		}
		rolloutUpdates = append(rolloutUpdates, item)
	}
	return rolloutUpdates, nil
}

func (r *fakeRolloutRepo) SetUpdateRolloutPercentage(_ context.Context, appId, branchName, runtimeVersion string, percentage int) (int64, error) {
	if r.setReturnsZeroRows {
		return 0, nil
	}
	active := r.activeRows(appId, branchName, runtimeVersion)
	for _, row := range active {
		pct := percentage
		row.rolloutPercentage = &pct
	}
	if r.events != nil {
		r.events.add(fmt.Sprintf("setRolloutPercentage:%d", percentage))
	}
	return int64(len(active)), nil
}

func (r *fakeRolloutRepo) ClearUpdateRollout(_ context.Context, appId, branchName, runtimeVersion string) (int64, error) {
	if r.clearReturnsZeroRows {
		return 0, nil
	}
	active := r.activeRows(appId, branchName, runtimeVersion)
	for _, row := range active {
		// Mirrors the SQL: control_update_id is retained as the historical marker
		// for the dashboard finished-rollout state; only the percentage is cleared.
		row.rolloutPercentage = nil
	}
	if r.events != nil {
		r.events.add("clearRollout")
	}
	return int64(len(active)), nil
}

type fakeChannelRepo struct {
	mappings map[string]*expo.ChannelMapping
}

func (r *fakeChannelRepo) InsertChannel(_ context.Context, _ string, _ *int64, _ string) (int64, error) {
	return 0, nil
}

func (r *fakeChannelRepo) DeleteChannel(_ context.Context, _, _ string) error { return nil }

func (r *fakeChannelRepo) GetChannelNameByBranchName(_ context.Context, _, _ string) ([]string, error) {
	return nil, nil
}

func (r *fakeChannelRepo) GetChannels(_ context.Context, _ string) ([]types.ChannelMapping, error) {
	return nil, nil
}

func (r *fakeChannelRepo) GetChannelBranchMapping(_ context.Context, _, channelName string) (*expo.ChannelMapping, error) {
	mapping, ok := r.mappings[channelName]
	if !ok {
		return nil, nil
	}
	mappingCopy := *mapping
	if mapping.Rollout != nil {
		rolloutCopy := *mapping.Rollout
		mappingCopy.Rollout = &rolloutCopy
	}
	return &mappingCopy, nil
}

type fakeAppRepo struct{}

func (fakeAppRepo) InsertApp(_ context.Context, _ store.InsertAppParameters) (string, error) {
	return "", nil
}

func (fakeAppRepo) DeleteAppByID(_ context.Context, _ string) error { return nil }

func (fakeAppRepo) GetApps(_ context.Context) ([]config.AppDescriptor, error) { return nil, nil }

func (fakeAppRepo) UpdateAppNameByID(_ context.Context, _, _ string) error { return nil }

func (fakeAppRepo) GetAppByID(_ context.Context, _ string) (config.AppConfig, error) {
	return config.AppConfig{}, nil
}

type fakeBranchRepo struct{}

func (fakeBranchRepo) InsertBranch(_ context.Context, _ pgdb.InsertBranchParams) (int64, error) {
	return 0, nil
}

func (fakeBranchRepo) UpsertBranchAndRuntimeVersion(_ context.Context, _, _, _ string) error {
	return nil
}

func (fakeBranchRepo) GetUpdatedMetadataByBranchName(_ context.Context, _, _ string) ([]pgdb.GetUpdatesMetadataByBranchNameRow, error) {
	return nil, nil
}

func (fakeBranchRepo) DeleteBranchByName(_ context.Context, _, _ string) error { return nil }

func (fakeBranchRepo) GetBranches(_ context.Context, _ string) ([]types.BranchMapping, error) {
	return nil, nil
}

func (fakeBranchRepo) GetRuntimeVersionsWithUpdateStats(_ context.Context, _, _ string) ([]types.RuntimeVersionWithStats, error) {
	return nil, nil
}

func (fakeBranchRepo) UpdateChannelBranchMapping(_ context.Context, _, _, _ string) error {
	return nil
}

func (fakeBranchRepo) CreateRuntimeVersion(_ context.Context, _, _ string) (int64, error) {
	return 0, nil
}

func (fakeBranchRepo) GetBranchByName(_ context.Context, _, _ string) (int64, error) { return 0, nil }

// fakeRolloutBucket satisfies bucket.Bucket for the revert flow: CreateUpdateFrom hands
// back a handle for the copied update without touching any storage.
type fakeRolloutBucket struct{}

func (fakeRolloutBucket) GetBranches(_ string) ([]string, error) { return nil, nil }

func (fakeRolloutBucket) GetRuntimeVersions(_, _ string) ([]types.RuntimeVersionWithStats, error) {
	return nil, nil
}

func (fakeRolloutBucket) GetUpdates(_, _, _ string) ([]types.Update, error) { return nil, nil }

func (fakeRolloutBucket) GetFile(_ types.Update, _ string) (*types.BucketFile, error) {
	return nil, fmt.Errorf("fake bucket stores no files")
}

func (fakeRolloutBucket) RequestUploadUrlForFileUpdate(_, _, _, _, _ string) (string, error) {
	return "", nil
}

func (fakeRolloutBucket) UploadFileIntoUpdate(_ types.Update, _ string, _ io.Reader) error {
	return nil
}

func (fakeRolloutBucket) DeleteUpdateFolder(_, _, _, _ string) error { return nil }

func (fakeRolloutBucket) CreateUpdateFrom(previousUpdate *types.Update, newUpdateId string) (*types.Update, error) {
	return &types.Update{
		AppId:          previousUpdate.AppId,
		Branch:         previousUpdate.Branch,
		RuntimeVersion: previousUpdate.RuntimeVersion,
		UpdateId:       newUpdateId,
	}, nil
}

func (fakeRolloutBucket) RetrieveMigrationHistory() ([]string, error) { return nil, nil }

func (fakeRolloutBucket) ApplyMigration(_ string) error { return nil }

func (fakeRolloutBucket) RemoveMigrationFromHistory(_ string) error { return nil }

type rolloutTestHarness struct {
	appId             string
	events            *eventLog
	updateRepo        *fakeUpdateRepo
	channelRepo       *fakeChannelRepo
	rolloutRepo       *fakeRolloutRepo
	updateService     *UpdateService
	protocolService   *ExpoProtocolService
	deploymentService *DeploymentService
	rolloutService    *RolloutService
}

func newRolloutTestHarness(t *testing.T) *rolloutTestHarness {
	t.Helper()
	events := &eventLog{}
	updateRepo := &fakeUpdateRepo{events: events}
	channelRepo := &fakeChannelRepo{mappings: map[string]*expo.ChannelMapping{}}
	rolloutRepo := &fakeRolloutRepo{updateRepo: updateRepo, events: events}
	updateService := NewUpdateService(updateRepo, nil)
	branchService := NewBranchService(fakeBranchRepo{}, channelRepo, updateRepo, rolloutRepo, fakeRolloutBucket{})
	deploymentService := NewDeploymentService(branchService, updateService, updateRepo, fakeRolloutBucket{})
	return &rolloutTestHarness{
		appId:             uuid.NewString(),
		events:            events,
		updateRepo:        updateRepo,
		channelRepo:       channelRepo,
		rolloutRepo:       rolloutRepo,
		updateService:     updateService,
		protocolService:   NewExpoProtocolService(fakeAppRepo{}, channelRepo, updateRepo, updateService, DefaultBranchRules()),
		deploymentService: deploymentService,
		rolloutService:    NewRolloutService(rolloutRepo, channelRepo, updateRepo, deploymentService),
	}
}

// seedRow describes an update row seeded directly into the fake store, bypassing the
// publish pipeline (and its event log).
type seedRow struct {
	branch     string
	rtv        string
	platform   string
	id         int64
	updateType types.UpdateType
	checked    bool
	percentage int   // 0 means no rollout state
	controlId  int64 // 0 means no control pointer
	uuid       string
}

func (h *rolloutTestHarness) seed(row seedRow) types.Update {
	h.updateRepo.mu.Lock()
	defer h.updateRepo.mu.Unlock()
	stored := h.updateRepo.appendRowLocked(h.appId, row.id, row.branch, row.rtv, row.platform, row.updateType)
	stored.checked = row.checked
	stored.updateUUID = row.uuid
	if row.percentage != 0 {
		pct := row.percentage
		stored.rolloutPercentage = &pct
	}
	if row.controlId != 0 {
		controlId := strconv.FormatInt(row.controlId, 10)
		stored.controlUpdateId = &controlId
	}
	return stored.update
}

// clientIDInBucket returns a deterministic client id that falls inside (or outside) the
// rollout cohort for the given salt and percentage.
func clientIDInBucket(t *testing.T, salt string, percentage int, wantIn bool) string {
	t.Helper()
	for i := 0; i < 100000; i++ {
		clientID := fmt.Sprintf("device-%d", i)
		if rollout.InBucket(clientID, salt, percentage) == wantIn {
			return clientID
		}
	}
	t.Fatalf("no client id found with inBucket=%v for salt %q at %d%%", wantIn, salt, percentage)
	return ""
}

func TestGetLatestUpdateForClientDecisionTree(t *testing.T) {
	ctx := context.Background()
	const branch, rtv, platform = "main", "1", "ios"

	t.Run("no rollout serves the latest update to every device", func(t *testing.T) {
		h := newRolloutTestHarness(t)
		h.seed(seedRow{branch: branch, rtv: rtv, platform: platform, id: 100, checked: true})
		for _, clientID := range []string{"", "any-device"} {
			resolution, err := h.updateService.GetLatestUpdateForClient(ctx, h.appId, branch, rtv, platform, clientID)
			require.NoError(t, err)
			require.NotNil(t, resolution.Update)
			assert.Equal(t, "100", resolution.Update.UpdateId)
			assert.True(t, resolution.BranchHasUpdate)
		}
	})

	t.Run("in-bucket gets the rollout update, out-of-bucket the control", func(t *testing.T) {
		h := newRolloutTestHarness(t)
		h.seed(seedRow{branch: branch, rtv: rtv, platform: platform, id: 100, checked: true})
		h.seed(seedRow{branch: branch, rtv: rtv, platform: platform, id: 200, checked: true, percentage: 40, controlId: 100})
		salt := rollout.UpdateSalt(h.appId, branch, rtv, "200")

		inResolution, err := h.updateService.GetLatestUpdateForClient(ctx, h.appId, branch, rtv, platform, clientIDInBucket(t, salt, 40, true))
		require.NoError(t, err)
		require.NotNil(t, inResolution.Update)
		assert.Equal(t, "200", inResolution.Update.UpdateId)

		outResolution, err := h.updateService.GetLatestUpdateForClient(ctx, h.appId, branch, rtv, platform, clientIDInBucket(t, salt, 40, false))
		require.NoError(t, err)
		require.NotNil(t, outResolution.Update)
		assert.Equal(t, "100", outResolution.Update.UpdateId)
		assert.True(t, outResolution.BranchHasUpdate)
	})

	t.Run("missing client id is never in the rollout", func(t *testing.T) {
		h := newRolloutTestHarness(t)
		h.seed(seedRow{branch: branch, rtv: rtv, platform: platform, id: 100, checked: true})
		// 99 percent still excludes the anonymous device.
		h.seed(seedRow{branch: branch, rtv: rtv, platform: platform, id: 200, checked: true, percentage: 99, controlId: 100})
		resolution, err := h.updateService.GetLatestUpdateForClient(ctx, h.appId, branch, rtv, platform, "")
		require.NoError(t, err)
		require.NotNil(t, resolution.Update)
		assert.Equal(t, "100", resolution.Update.UpdateId)
	})

	t.Run("nil control resolves to noUpdateAvailable without branch fallback", func(t *testing.T) {
		h := newRolloutTestHarness(t)
		h.seed(seedRow{branch: branch, rtv: rtv, platform: platform, id: 200, checked: true, percentage: 40})
		salt := rollout.UpdateSalt(h.appId, branch, rtv, "200")
		resolution, err := h.updateService.GetLatestUpdateForClient(ctx, h.appId, branch, rtv, platform, clientIDInBucket(t, salt, 40, false))
		require.NoError(t, err)
		assert.Nil(t, resolution.Update)
		// The branch DID resolve for the device: callers must not fall back to
		// another branch, the device gets noUpdateAvailable.
		assert.True(t, resolution.BranchHasUpdate)
	})

	t.Run("branch without any checked update reports no resolution", func(t *testing.T) {
		h := newRolloutTestHarness(t)
		resolution, err := h.updateService.GetLatestUpdateForClient(ctx, h.appId, branch, rtv, platform, "any-device")
		require.NoError(t, err)
		assert.Nil(t, resolution.Update)
		assert.False(t, resolution.BranchHasUpdate)
	})

	t.Run("rollout row that is no longer latest is inert", func(t *testing.T) {
		// The revert window: the control has been republished (a newer plain row)
		// while the rollout row still carries its percentage until the clear lands.
		h := newRolloutTestHarness(t)
		h.seed(seedRow{branch: branch, rtv: rtv, platform: platform, id: 100, checked: true})
		h.seed(seedRow{branch: branch, rtv: rtv, platform: platform, id: 200, checked: true, percentage: 40, controlId: 100})
		h.seed(seedRow{branch: branch, rtv: rtv, platform: platform, id: 300, checked: true})
		salt := rollout.UpdateSalt(h.appId, branch, rtv, "200")
		for _, clientID := range []string{clientIDInBucket(t, salt, 40, true), clientIDInBucket(t, salt, 40, false), ""} {
			resolution, err := h.updateService.GetLatestUpdateForClient(ctx, h.appId, branch, rtv, platform, clientID)
			require.NoError(t, err)
			require.NotNil(t, resolution.Update)
			assert.Equal(t, "300", resolution.Update.UpdateId)
		}
	})
}

func TestResolveManifestBundleServesRollbackControl(t *testing.T) {
	ctx := context.Background()
	h := newRolloutTestHarness(t)
	h.channelRepo.mappings["production"] = &expo.ChannelMapping{Id: "1", BranchName: "main"}
	// A rollout published on top of a rollback (the progressive hotfix case): the
	// out-of-bucket cohort must receive the rollback directive, so the update type
	// dispatch has to run on the served update, not on the latest row.
	h.seed(seedRow{branch: "main", rtv: "1", platform: "ios", id: 100, updateType: types.Rollback, checked: true})
	h.seed(seedRow{branch: "main", rtv: "1", platform: "ios", id: 200, checked: true, percentage: 40, controlId: 100})
	salt := rollout.UpdateSalt(h.appId, "main", "1", "200")

	params := ManifestRequestParams{
		RequestID:       "test",
		AppID:           h.appId,
		ChannelName:     "production",
		Platform:        "ios",
		RuntimeVersion:  "1",
		ProtocolVersion: 1,
	}

	params.ClientID = clientIDInBucket(t, salt, 40, false)
	outResult, err := h.protocolService.ResolveManifestBundle(ctx, params)
	require.NoError(t, err)
	require.NotNil(t, outResult.Update)
	assert.Equal(t, "100", outResult.Update.UpdateId)
	assert.Equal(t, types.Rollback, outResult.UpdateType)
	assert.Equal(t, "main", outResult.BranchName)

	params.ClientID = clientIDInBucket(t, salt, 40, true)
	inResult, err := h.protocolService.ResolveManifestBundle(ctx, params)
	require.NoError(t, err)
	require.NotNil(t, inResult.Update)
	assert.Equal(t, "200", inResult.Update.UpdateId)
	assert.Equal(t, types.NormalUpdate, inResult.UpdateType)
}

func TestResolveManifestBundleChannelRollout(t *testing.T) {
	ctx := context.Background()
	const channelSalt = "channel-rollout-salt-uuid"

	newChannelRolloutHarness := func(t *testing.T) *rolloutTestHarness {
		h := newRolloutTestHarness(t)
		h.channelRepo.mappings["production"] = &expo.ChannelMapping{
			Id:         "1",
			BranchName: "main",
			Rollout:    &expo.ChannelRolloutInfo{ID: channelSalt, BranchName: "beta", Percentage: 30},
		}
		h.seed(seedRow{branch: "main", rtv: "1", platform: "ios", id: 100, checked: true})
		return h
	}

	baseParams := func(h *rolloutTestHarness, clientID string) ManifestRequestParams {
		return ManifestRequestParams{
			RequestID:       "test",
			AppID:           h.appId,
			ChannelName:     "production",
			Platform:        "ios",
			RuntimeVersion:  "1",
			ProtocolVersion: 1,
			ClientID:        clientID,
		}
	}

	t.Run("in-bucket device is served the rollout branch and attributed to it", func(t *testing.T) {
		h := newChannelRolloutHarness(t)
		h.seed(seedRow{branch: "beta", rtv: "1", platform: "ios", id: 200, checked: true})

		inResult, err := h.protocolService.ResolveManifestBundle(ctx, baseParams(h, clientIDInBucket(t, channelSalt, 30, true)))
		require.NoError(t, err)
		require.NotNil(t, inResult.Update)
		assert.Equal(t, "200", inResult.Update.UpdateId)
		// The served branch drives the expo-manifest-filters header and the metrics
		// attribution: it must be the rollout branch for the in-bucket cohort.
		assert.Equal(t, "beta", inResult.BranchName)

		outResult, err := h.protocolService.ResolveManifestBundle(ctx, baseParams(h, clientIDInBucket(t, channelSalt, 30, false)))
		require.NoError(t, err)
		require.NotNil(t, outResult.Update)
		assert.Equal(t, "100", outResult.Update.UpdateId)
		assert.Equal(t, "main", outResult.BranchName)

		anonymousResult, err := h.protocolService.ResolveManifestBundle(ctx, baseParams(h, ""))
		require.NoError(t, err)
		require.NotNil(t, anonymousResult.Update)
		assert.Equal(t, "main", anonymousResult.BranchName)
	})

	t.Run("runtime version fallback when the rollout branch has no update", func(t *testing.T) {
		h := newChannelRolloutHarness(t)
		result, err := h.protocolService.ResolveManifestBundle(ctx, baseParams(h, clientIDInBucket(t, channelSalt, 30, true)))
		require.NoError(t, err)
		require.NotNil(t, result.Update)
		assert.Equal(t, "100", result.Update.UpdateId)
		assert.Equal(t, "main", result.BranchName)
	})

	t.Run("unchecked updates on the rollout branch do not count", func(t *testing.T) {
		h := newChannelRolloutHarness(t)
		h.seed(seedRow{branch: "beta", rtv: "1", platform: "ios", id: 200, checked: false})
		result, err := h.protocolService.ResolveManifestBundle(ctx, baseParams(h, clientIDInBucket(t, channelSalt, 30, true)))
		require.NoError(t, err)
		require.NotNil(t, result.Update)
		assert.Equal(t, "100", result.Update.UpdateId)
		assert.Equal(t, "main", result.BranchName)
	})
}

func TestPublishRepublishRollbackBlockedDuringActiveRollout(t *testing.T) {
	ctx := context.Background()
	h := newRolloutTestHarness(t)
	control := h.seed(seedRow{branch: "main", rtv: "1", platform: "ios", id: 100, checked: true})
	h.seed(seedRow{branch: "main", rtv: "1", platform: "ios", id: 200, checked: true, percentage: 20, controlId: 100})

	_, err := h.deploymentService.RequestUploadURLs(ctx, RequestUploadURLParams{
		RequestID:      "test",
		AppID:          h.appId,
		BranchName:     "main",
		Platform:       "ios",
		RuntimeVersion: "1",
		FileNames:      []string{"bundle.js"},
	})
	assert.ErrorIs(t, err, ErrActiveRolloutBlocksPublish)

	_, err = h.deploymentService.CreateRollback(ctx, h.appId, "ios", "", "1", "main", "")
	assert.ErrorIs(t, err, ErrActiveRolloutBlocksPublish)

	_, err = h.deploymentService.RepublishUpdate(ctx, &control, "ios", "", nil)
	assert.ErrorIs(t, err, ErrActiveRolloutBlocksPublish)

	// A branch without an active rollout is not affected by the guard.
	h.seed(seedRow{branch: "other", rtv: "1", platform: "ios", id: 100, checked: true})
	_, err = h.deploymentService.CreateRollback(ctx, h.appId, "ios", "", "1", "other", "")
	assert.NoError(t, err)
}

func TestUncheckedRolloutRowIsInert(t *testing.T) {
	// The dedupe-406 path abandons an unchecked update row; its rollout fields must
	// neither influence device resolution nor block subsequent publishes.
	ctx := context.Background()
	h := newRolloutTestHarness(t)
	control := h.seed(seedRow{branch: "main", rtv: "1", platform: "ios", id: 100, checked: true})

	_, err := h.updateRepo.CreateUpdateWithRollout(ctx, h.appId, 200, "main", "1", "ios", "abc123", "", 25, nil)
	require.NoError(t, err)

	salt := rollout.UpdateSalt(h.appId, "main", "1", "200")
	for _, clientID := range []string{clientIDInBucket(t, salt, 25, true), clientIDInBucket(t, salt, 25, false), ""} {
		resolution, err := h.updateService.GetLatestUpdateForClient(ctx, h.appId, "main", "1", "ios", clientID)
		require.NoError(t, err)
		require.NotNil(t, resolution.Update)
		assert.Equal(t, "100", resolution.Update.UpdateId)
	}

	// The publish guard stays open: the abandoned row has no active rollout.
	_, err = h.deploymentService.RepublishUpdate(ctx, &control, "ios", "", nil)
	assert.NoError(t, err)
}

func TestMarkUpdateAsCheckedMapsUniqueViolationToRolloutConflict(t *testing.T) {
	// The transactional close of the publish race: a second rollout update reaching
	// checked state on the same (branch, rtv, platform) violates the partial unique
	// index, which must surface as the 409 sentinel rather than a raw SQL error.
	ctx := context.Background()
	h := newRolloutTestHarness(t)
	h.seed(seedRow{branch: "main", rtv: "1", platform: "ios", id: 100, checked: true})
	h.seed(seedRow{branch: "main", rtv: "1", platform: "ios", id: 200, checked: true, percentage: 20, controlId: 100})

	racingUpdate, err := h.updateRepo.CreateUpdateWithRollout(ctx, h.appId, 300, "main", "1", "ios", "abc123", "", 30, nil)
	require.NoError(t, err)

	err = h.deploymentService.MarkUpdateAsChecked(ctx, *racingUpdate, types.NormalUpdate)
	assert.ErrorIs(t, err, ErrActiveRolloutBlocksPublish)
}

func TestRevertUpdateRollout(t *testing.T) {
	ctx := context.Background()

	t.Run("claims the rollout first, then republishes the control", func(t *testing.T) {
		h := newRolloutTestHarness(t)
		h.seed(seedRow{branch: "main", rtv: "1", platform: "ios", id: 100, checked: true})
		h.seed(seedRow{branch: "main", rtv: "1", platform: "ios", id: 200, checked: true, percentage: 20, controlId: 100})

		preMutationRows, err := h.rolloutService.RevertUpdateRollout(ctx, h.appId, "main", "1", nil)
		require.NoError(t, err)
		require.Len(t, preMutationRows, 1)
		assert.Equal(t, "200", preMutationRows[0].UpdateId)
		assert.Equal(t, 20, preMutationRows[0].Percentage)

		events := h.events.snapshot()
		require.Len(t, events, 3)
		// The clear is the atomic claim: it must land before any side effect so a
		// concurrent finish or second revert loses the race cleanly (0 rows, 409)
		// instead of republishing the control over a state it did not read.
		assert.Equal(t, "clearRollout", events[0], "the clear is the atomic claim and must land first")
		assert.True(t, strings.HasPrefix(events[1], "createUpdate:"), "revert must republish the control as a plain update, got %q", events[1])
		assert.True(t, strings.HasPrefix(events[2], "markChecked:"), "got %q", events[2])

		// The republished copy is now the latest update, carries no rollout linkage,
		// and every cohort converges on it.
		envelope, err := h.updateRepo.GetLatestUpdateWithRollout(ctx, h.appId, "main", "1", "ios")
		require.NoError(t, err)
		require.NotNil(t, envelope)
		assert.Nil(t, envelope.RolloutPercentage)
		assert.Nil(t, envelope.Control)
		assert.Equal(t, strings.TrimPrefix(events[1], "createUpdate:"), envelope.UpdateId)
		assert.NotEqual(t, "200", envelope.UpdateId)
		assert.NotEqual(t, "100", envelope.UpdateId)
	})

	t.Run("nil control reverts by recreating a rollback to embedded", func(t *testing.T) {
		h := newRolloutTestHarness(t)
		h.seed(seedRow{branch: "main", rtv: "1", platform: "ios", id: 200, checked: true, percentage: 20})

		_, err := h.rolloutService.RevertUpdateRollout(ctx, h.appId, "main", "1", nil)
		require.NoError(t, err)

		events := h.events.snapshot()
		require.Len(t, events, 3)
		assert.Equal(t, "clearRollout", events[0])
		assert.True(t, strings.HasPrefix(events[1], "createRollback:"), "got %q", events[1])

		latestUpdate, err := h.updateRepo.GetLatestUpdate(ctx, h.appId, "main", "1", "ios")
		require.NoError(t, err)
		require.NotNil(t, latestUpdate)
		latestType, err := h.updateRepo.GetUpdateType(ctx, *latestUpdate)
		require.NoError(t, err)
		assert.Equal(t, types.Rollback, latestType)
	})

	t.Run("rollback control reverts by recreating a rollback, not republishing", func(t *testing.T) {
		h := newRolloutTestHarness(t)
		h.seed(seedRow{branch: "main", rtv: "1", platform: "ios", id: 100, updateType: types.Rollback, checked: true})
		h.seed(seedRow{branch: "main", rtv: "1", platform: "ios", id: 200, checked: true, percentage: 20, controlId: 100})

		_, err := h.rolloutService.RevertUpdateRollout(ctx, h.appId, "main", "1", nil)
		require.NoError(t, err)

		events := h.events.snapshot()
		require.Len(t, events, 3)
		assert.True(t, strings.HasPrefix(events[1], "createRollback:"), "a rollback control has no files to copy, got %q", events[1])
	})

	t.Run("stale expectedUpdateId is a conflict", func(t *testing.T) {
		h := newRolloutTestHarness(t)
		h.seed(seedRow{branch: "main", rtv: "1", platform: "ios", id: 100, checked: true})
		h.seed(seedRow{branch: "main", rtv: "1", platform: "ios", id: 200, checked: true, percentage: 20, controlId: 100})

		staleUpdateId := "999"
		_, err := h.rolloutService.RevertUpdateRollout(ctx, h.appId, "main", "1", &staleUpdateId)
		requestErr := (*RolloutRequestError)(nil)
		require.ErrorAs(t, err, &requestErr)
		assert.Equal(t, 409, requestErr.Status)
		// Nothing was republished or cleared.
		assert.Empty(t, h.events.snapshot())
	})

	t.Run("no active rollout is a not found", func(t *testing.T) {
		h := newRolloutTestHarness(t)
		_, err := h.rolloutService.RevertUpdateRollout(ctx, h.appId, "main", "1", nil)
		requestErr := (*RolloutRequestError)(nil)
		require.ErrorAs(t, err, &requestErr)
		assert.Equal(t, 404, requestErr.Status)
	})
}

func TestSetUpdateRolloutPercentageProgression(t *testing.T) {
	ctx := context.Background()

	seedActiveRollout := func(t *testing.T) *rolloutTestHarness {
		h := newRolloutTestHarness(t)
		h.seed(seedRow{branch: "main", rtv: "1", platform: "ios", id: 100, checked: true})
		h.seed(seedRow{branch: "main", rtv: "1", platform: "ios", id: 200, checked: true, percentage: 20, controlId: 100})
		return h
	}

	requireRequestError := func(t *testing.T, err error, wantStatus int) *RolloutRequestError {
		t.Helper()
		requestErr := (*RolloutRequestError)(nil)
		require.ErrorAs(t, err, &requestErr)
		assert.Equal(t, wantStatus, requestErr.Status)
		return requestErr
	}

	t.Run("increase progresses and returns the pre-mutation rows", func(t *testing.T) {
		h := seedActiveRollout(t)
		preMutationRows, err := h.rolloutService.SetUpdateRolloutPercentage(ctx, h.appId, "main", "1", 50, nil)
		require.NoError(t, err)
		require.Len(t, preMutationRows, 1)
		assert.Equal(t, 20, preMutationRows[0].Percentage)

		activeRollouts, err := h.rolloutService.GetUpdateRollout(ctx, h.appId, "main", "1")
		require.NoError(t, err)
		require.Len(t, activeRollouts, 1)
		assert.Equal(t, 50, activeRollouts[0].Percentage)
	})

	t.Run("decrease and no-op are rejected", func(t *testing.T) {
		h := seedActiveRollout(t)
		_, err := h.rolloutService.SetUpdateRolloutPercentage(ctx, h.appId, "main", "1", 10, nil)
		requestErr := requireRequestError(t, err, 400)
		assert.Contains(t, requestErr.Message, "can only increase")

		_, err = h.rolloutService.SetUpdateRolloutPercentage(ctx, h.appId, "main", "1", 20, nil)
		requireRequestError(t, err, 400)
	})

	t.Run("out of range percentages are rejected", func(t *testing.T) {
		h := seedActiveRollout(t)
		_, err := h.rolloutService.SetUpdateRolloutPercentage(ctx, h.appId, "main", "1", 0, nil)
		requireRequestError(t, err, 400)
		_, err = h.rolloutService.SetUpdateRolloutPercentage(ctx, h.appId, "main", "1", 101, nil)
		requireRequestError(t, err, 400)
	})

	t.Run("100 ends the rollout", func(t *testing.T) {
		h := seedActiveRollout(t)
		preMutationRows, err := h.rolloutService.SetUpdateRolloutPercentage(ctx, h.appId, "main", "1", 100, nil)
		require.NoError(t, err)
		require.Len(t, preMutationRows, 1)
		assert.Equal(t, 20, preMutationRows[0].Percentage)

		activeRollouts, err := h.rolloutService.GetUpdateRollout(ctx, h.appId, "main", "1")
		require.NoError(t, err)
		assert.Empty(t, activeRollouts)

		_, err = h.rolloutService.SetUpdateRolloutPercentage(ctx, h.appId, "main", "1", 60, nil)
		requireRequestError(t, err, 404)
	})

	t.Run("expectedUpdateId guards against stale tabs", func(t *testing.T) {
		h := seedActiveRollout(t)
		matchingUpdateId := "200"
		_, err := h.rolloutService.SetUpdateRolloutPercentage(ctx, h.appId, "main", "1", 60, &matchingUpdateId)
		require.NoError(t, err)

		staleUpdateId := "999"
		_, err = h.rolloutService.SetUpdateRolloutPercentage(ctx, h.appId, "main", "1", 70, &staleUpdateId)
		requireRequestError(t, err, 409)
	})

	t.Run("zero rows from a concurrent end is a conflict", func(t *testing.T) {
		h := seedActiveRollout(t)
		h.rolloutRepo.setReturnsZeroRows = true
		_, err := h.rolloutService.SetUpdateRolloutPercentage(ctx, h.appId, "main", "1", 60, nil)
		requireRequestError(t, err, 409)

		h.rolloutRepo.setReturnsZeroRows = false
		h.rolloutRepo.clearReturnsZeroRows = true
		_, err = h.rolloutService.SetUpdateRolloutPercentage(ctx, h.appId, "main", "1", 100, nil)
		requireRequestError(t, err, 409)
	})
}

func TestRolloutServiceRequiresControlPlane(t *testing.T) {
	// Stateless wiring leaves the rollout repository nil: every operation must refuse
	// with the control-plane sentinel the handlers map to a 400.
	ctx := context.Background()
	service := NewRolloutService(nil, nil, nil, nil)

	_, err := service.StartChannelRollout(ctx, "app", "production", "beta", 10)
	assert.ErrorIs(t, err, ErrRolloutsRequireControlPlane)
	_, err = service.UpdateChannelRolloutPercentage(ctx, "app", "production", 50)
	assert.ErrorIs(t, err, ErrRolloutsRequireControlPlane)
	assert.ErrorIs(t, service.EndChannelRollout(ctx, "app", "production", ChannelRolloutOutcomePromote), ErrRolloutsRequireControlPlane)
	_, err = service.GetUpdateRollout(ctx, "app", "main", "1")
	assert.ErrorIs(t, err, ErrRolloutsRequireControlPlane)
	_, err = service.SetUpdateRolloutPercentage(ctx, "app", "main", "1", 50, nil)
	assert.ErrorIs(t, err, ErrRolloutsRequireControlPlane)
	_, err = service.RevertUpdateRollout(ctx, "app", "main", "1", nil)
	assert.ErrorIs(t, err, ErrRolloutsRequireControlPlane)
}

func TestResolveAssetUpdateTiers(t *testing.T) {
	ctx := context.Background()

	newAssetHarness := func(t *testing.T) (*rolloutTestHarness, *expo.ChannelMapping, string, string) {
		h := newRolloutTestHarness(t)
		h.channelRepo.mappings["production"] = &expo.ChannelMapping{Id: "1", BranchName: "main"}
		h.seed(seedRow{branch: "main", rtv: "1", platform: "ios", id: 100, checked: true, uuid: "11111111-1111-1111-1111-111111111111"})
		h.seed(seedRow{branch: "main", rtv: "1", platform: "ios", id: 200, checked: true, percentage: 40, controlId: 100, uuid: "22222222-2222-2222-2222-222222222222"})
		branchMap, err := h.channelRepo.GetChannelBranchMapping(ctx, h.appId, "production")
		require.NoError(t, err)
		salt := rollout.UpdateSalt(h.appId, "main", "1", "200")
		return h, branchMap, clientIDInBucket(t, salt, 40, true), clientIDInBucket(t, salt, 40, false)
	}

	baseParams := func(h *rolloutTestHarness, clientID string) AssetResolutionParams {
		return AssetResolutionParams{
			RequestID:      "test",
			AppID:          h.appId,
			ChannelName:    "production",
			AssetName:      "bundle.js",
			RuntimeVersion: "1",
			Platform:       "ios",
			ClientID:       clientID,
		}
	}

	t.Run("no hints takes the same decision as the manifest", func(t *testing.T) {
		t.Setenv("DB_URL", "postgres://rollout-tier-tests")
		h, branchMap, inClient, outClient := newAssetHarness(t)

		branchName, servedUpdate, err := h.protocolService.resolveAssetUpdate(ctx, baseParams(h, inClient), branchMap)
		require.NoError(t, err)
		require.NotNil(t, servedUpdate)
		assert.Equal(t, "main", branchName)
		assert.Equal(t, "200", servedUpdate.UpdateId)

		_, servedUpdate, err = h.protocolService.resolveAssetUpdate(ctx, baseParams(h, outClient), branchMap)
		require.NoError(t, err)
		require.NotNil(t, servedUpdate)
		assert.Equal(t, "100", servedUpdate.UpdateId)
	})

	t.Run("Expo-Requested-Update-ID pins the exact update", func(t *testing.T) {
		t.Setenv("DB_URL", "postgres://rollout-tier-tests")
		h, branchMap, _, outClient := newAssetHarness(t)

		// An out-of-bucket device that is downloading the rollout update (its manifest
		// decision happened before a progression) must still get its assets.
		params := baseParams(h, outClient)
		params.RequestedUpdateID = "22222222-2222-2222-2222-222222222222"
		branchName, servedUpdate, err := h.protocolService.resolveAssetUpdate(ctx, params, branchMap)
		require.NoError(t, err)
		require.NotNil(t, servedUpdate)
		assert.Equal(t, "main", branchName)
		assert.Equal(t, "200", servedUpdate.UpdateId)

		// An unknown UUID falls through to the rule engine decision.
		params.RequestedUpdateID = "33333333-3333-3333-3333-333333333333"
		_, servedUpdate, err = h.protocolService.resolveAssetUpdate(ctx, params, branchMap)
		require.NoError(t, err)
		require.NotNil(t, servedUpdate)
		assert.Equal(t, "100", servedUpdate.UpdateId)
	})

	t.Run("updateId and branch query params pin the update on allowed branches only", func(t *testing.T) {
		t.Setenv("DB_URL", "postgres://rollout-tier-tests")
		h, branchMap, _, outClient := newAssetHarness(t)

		params := baseParams(h, outClient)
		params.Branch = "main"
		params.UpdateID = "200"
		branchName, servedUpdate, err := h.protocolService.resolveAssetUpdate(ctx, params, branchMap)
		require.NoError(t, err)
		require.NotNil(t, servedUpdate)
		assert.Equal(t, "main", branchName)
		assert.Equal(t, "200", servedUpdate.UpdateId)

		// A branch the channel cannot serve is ignored, resolution falls through.
		params.Branch = "not-served-by-this-channel"
		_, servedUpdate, err = h.protocolService.resolveAssetUpdate(ctx, params, branchMap)
		require.NoError(t, err)
		require.NotNil(t, servedUpdate)
		assert.Equal(t, "100", servedUpdate.UpdateId)
	})

	t.Run("stateless mode ignores the pinning hints entirely", func(t *testing.T) {
		h, branchMap, _, outClient := newAssetHarness(t)

		params := baseParams(h, outClient)
		params.Branch = "main"
		params.UpdateID = "200"
		params.RequestedUpdateID = "22222222-2222-2222-2222-222222222222"
		_, servedUpdate, err := h.protocolService.resolveAssetUpdate(ctx, params, branchMap)
		require.NoError(t, err)
		require.NotNil(t, servedUpdate)
		assert.Equal(t, "100", servedUpdate.UpdateId)
	})
}
