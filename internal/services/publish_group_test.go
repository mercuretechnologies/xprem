// Service-level tests for publish-group-wide operations: the fan-out of one
// group rollback or republish into per-platform rows sharing a fresh
// server-minted group. SQL persistence is covered by the store integration
// tests; here the fake repo exercises the orchestration.
package services

import (
	"context"
	"strings"
	"testing"

	"xprem/internal/bucket"
	"xprem/internal/types"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedGrouped seeds one row and stamps it with a publish group, as if a
// group-aware CLI had published it.
func (h *rolloutTestHarness) seedGrouped(row seedRow, publishGroup string) types.Update {
	update := h.seed(row)
	h.updateRepo.mu.Lock()
	defer h.updateRepo.mu.Unlock()
	if stored := h.updateRepo.findRowLocked(update.AppId, update.Branch, update.RuntimeVersion, update.UpdateId); stored != nil {
		stored.publishGroup = &publishGroup
	}
	return update
}

// rowsInGroup returns the fake rows stamped with the given group, keyed by platform.
func (h *rolloutTestHarness) rowsInGroup(publishGroup string) map[types.Platform]*fakeStoredUpdate {
	h.updateRepo.mu.Lock()
	defer h.updateRepo.mu.Unlock()
	rows := map[types.Platform]*fakeStoredUpdate{}
	for _, row := range h.updateRepo.rows {
		if row.publishGroup != nil && *row.publishGroup == publishGroup {
			rows[row.platform] = row
		}
	}
	return rows
}

func TestRepublishPublishGroup(t *testing.T) {
	ctx := context.Background()
	const branch, rtv = "main", "1"

	t.Run("republishes every member on its platform under one new group", func(t *testing.T) {
		h := newRolloutTestHarness(t)
		sourceGroup := uuid.NewString()
		h.seedGrouped(seedRow{branch: branch, rtv: rtv, platform: "ios", id: 100, checked: true, uuid: uuid.NewString()}, sourceGroup)
		h.seedGrouped(seedRow{branch: branch, rtv: rtv, platform: "android", id: 200, checked: true, uuid: uuid.NewString()}, sourceGroup)

		result, err := h.deploymentService.RepublishPublishGroup(ctx, h.appId, branch, rtv, sourceGroup)
		require.NoError(t, err)
		assert.NotEqual(t, sourceGroup, result.PublishGroup)
		require.Len(t, result.Updates, 2)

		created := h.rowsInGroup(result.PublishGroup)
		require.Len(t, created, 2)
		for _, platform := range []types.Platform{types.PlatformIOS, types.PlatformAndroid} {
			require.Contains(t, created, platform)
			assert.Equal(t, types.NormalUpdate, created[platform].updateType)
			assert.True(t, created[platform].checked)
			// New rows, not the sources restamped.
			assert.NotContains(t, []string{"100", "200"}, created[platform].update.UpdateId)
		}
	})

	t.Run("a group of rollback markers is refused", func(t *testing.T) {
		h := newRolloutTestHarness(t)
		markerGroup := uuid.NewString()
		h.seedGrouped(seedRow{branch: branch, rtv: rtv, platform: "ios", id: 100, updateType: types.Rollback, checked: true}, markerGroup)

		_, err := h.deploymentService.RepublishPublishGroup(ctx, h.appId, branch, rtv, markerGroup)
		var rErr *RepublishError
		require.ErrorAs(t, err, &rErr)
	})

	t.Run("unknown group answers ErrPublishGroupNotFound", func(t *testing.T) {
		h := newRolloutTestHarness(t)
		_, err := h.deploymentService.RepublishPublishGroup(ctx, h.appId, branch, rtv, uuid.NewString())
		require.ErrorIs(t, err, ErrPublishGroupNotFound)
	})

	t.Run("fails fast on the first bad member and keeps the completed ones", func(t *testing.T) {
		h := newRolloutTestHarness(t)
		sourceGroup := uuid.NewString()
		// Members run in id order: the valid iOS update republishes first, then
		// the degenerate rollback marker in the same group refuses.
		h.seedGrouped(seedRow{branch: branch, rtv: rtv, platform: "ios", id: 100, checked: true, uuid: uuid.NewString()}, sourceGroup)
		h.seedGrouped(seedRow{branch: branch, rtv: rtv, platform: "android", id: 200, updateType: types.Rollback, checked: true}, sourceGroup)

		_, err := h.deploymentService.RepublishPublishGroup(ctx, h.appId, branch, rtv, sourceGroup)
		var rErr *RepublishError
		require.ErrorAs(t, err, &rErr)
		assert.Contains(t, err.Error(), "android")

		// The partial-completion contract: the platform that succeeded before
		// the failure stays published.
		var created int
		for _, event := range h.events.snapshot() {
			if strings.HasPrefix(event, "createUpdate:") {
				created++
			}
		}
		assert.Equal(t, 1, created)
	})

	t.Run("active rollout blocks the group republish", func(t *testing.T) {
		h := newRolloutTestHarness(t)
		sourceGroup := uuid.NewString()
		h.seedGrouped(seedRow{branch: branch, rtv: rtv, platform: "ios", id: 100, checked: true, uuid: uuid.NewString()}, sourceGroup)
		h.seed(seedRow{branch: branch, rtv: rtv, platform: "ios", id: 300, checked: true, percentage: 20, controlId: 100})

		_, err := h.deploymentService.RepublishPublishGroup(ctx, h.appId, branch, rtv, sourceGroup)
		require.ErrorIs(t, err, ErrActiveRolloutBlocksPublish)
	})
}

// TestRequestUploadURLsStampsPublishGroup covers the publish path plumbing: the
// CLI-minted group reaches the created row, through both the plain insert and
// the rollout insert.
func TestRequestUploadURLsStampsPublishGroup(t *testing.T) {
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_BUCKET_BASE_PATH", t.TempDir())
	t.Setenv("BASE_URL", "http://localhost:3000")
	t.Setenv("JWT_SECRET", "test_jwt_secret")
	bucket.ResetBucketInstance()
	t.Cleanup(bucket.ResetBucketInstance)

	ctx := context.Background()
	h := newRolloutTestHarness(t)
	group := uuid.NewString()

	_, err := h.deploymentService.RequestUploadURLs(ctx, RequestUploadURLParams{
		RequestID:      "test",
		AppID:          h.appId,
		BranchName:     "main",
		Platform:       "ios",
		RuntimeVersion: "1",
		Files:          hashedUploads("bundle.js"),
		PublishGroupID: &group,
	})
	require.NoError(t, err)

	pct := 25
	_, err = h.deploymentService.RequestUploadURLs(ctx, RequestUploadURLParams{
		RequestID:         "test",
		AppID:             h.appId,
		BranchName:        "main",
		Platform:          "android",
		RuntimeVersion:    "1",
		Files:             hashedUploads("bundle.js"),
		RolloutPercentage: &pct,
		PublishGroupID:    &group,
	})
	require.NoError(t, err)

	rows := h.rowsInGroup(group)
	require.Len(t, rows, 2)
	assert.Nil(t, rows["ios"].rolloutPercentage)
	require.NotNil(t, rows["android"].rolloutPercentage)
	assert.Equal(t, 25, *rows["android"].rolloutPercentage)
}

// TestPublishGroupRolloutActivatesEveryPlatform pins the sequential worst case
// of one grouped rollout publish: iOS's rollout is already ACTIVE when
// Android's activation lands. Every activation guard is scoped per platform,
// so the second platform of the same run must not be blocked by the first.
func TestPublishGroupRolloutActivatesEveryPlatform(t *testing.T) {
	ctx := context.Background()
	h := newRolloutTestHarness(t)
	group := uuid.NewString()

	ios, err := h.updateRepo.CreateUpdateWithRollout(ctx, h.appId, 100, "main", "1", "ios", "abc123", "", 10, &group)
	require.NoError(t, err)
	android, err := h.updateRepo.CreateUpdateWithRollout(ctx, h.appId, 200, "main", "1", "android", "abc123", "", 10, &group)
	require.NoError(t, err)

	require.NoError(t, h.deploymentService.MarkUpdateAsChecked(ctx, *ios, types.NormalUpdate))
	require.NoError(t, h.deploymentService.MarkUpdateAsChecked(ctx, *android, types.NormalUpdate))

	active, err := h.updateRepo.HasActiveRolloutUpdate(ctx, h.appId, "main", "1")
	require.NoError(t, err)
	assert.True(t, active)

	rows := h.rowsInGroup(group)
	require.Len(t, rows, 2)
	for _, platform := range []types.Platform{types.PlatformIOS, types.PlatformAndroid} {
		require.Contains(t, rows, platform)
		assert.True(t, rows[platform].checked)
		require.NotNil(t, rows[platform].rolloutPercentage)
		assert.Equal(t, 10, *rows[platform].rolloutPercentage)
	}
}
