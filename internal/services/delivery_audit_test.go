package services

import (
	"context"
	"expo-open-ota/internal/auditlog"
	"expo-open-ota/internal/types"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The shared fakeRolloutRepo answers 0 rows on the channel-rollout writes;
// this wrapper makes them succeed so the service-level emissions can run.
type channelRolloutCapableRepo struct{ *fakeRolloutRepo }

func (r channelRolloutCapableRepo) CreateChannelRollout(_ context.Context, _, _, _, _ string, _ int) (int64, error) {
	return 1, nil
}
func (r channelRolloutCapableRepo) UpdateChannelRolloutPercentage(_ context.Context, _, _ string, _ int) (int64, error) {
	return 1, nil
}
func (r channelRolloutCapableRepo) PromoteChannelRollout(_ context.Context, _, _ string) (int64, error) {
	return 1, nil
}
func (r channelRolloutCapableRepo) DeleteChannelRollout(_ context.Context, _, _ string) (int64, error) {
	return 1, nil
}

func cliPublishCtx() context.Context {
	return WithCliAuth(context.Background(),
		CliCredential{AppID: "app-1", KeyID: 42, KeyName: "ci-production"})
}

func TestUpdateRolloutEventsAndInternalSplit(t *testing.T) {
	ctx := adminManagementCtx()
	h := newRolloutTestHarness(t)
	recorder := &fakeAuditRecorder{}
	// Both seams live: proves the revert reports as a rollout event and never
	// as the CLI-facing update.rollback/update.republished, even though it
	// republishes through the deployment service internally.
	h.rolloutService.SetOnAuditEvent(recorder.Record)
	h.deploymentService.SetOnAuditEvent(recorder.Record)
	h.seed(seedRow{branch: "main", rtv: "1", platform: "ios", id: 100, checked: true})
	h.seed(seedRow{branch: "main", rtv: "1", platform: "ios", id: 200, checked: true, percentage: 20, controlId: 100})

	_, err := h.rolloutService.SetUpdateRolloutPercentage(ctx, h.appId, "main", "1", 50, nil)
	require.NoError(t, err)
	require.Len(t, recorder.events, 1)
	set := recorder.events[0]
	assert.Equal(t, auditlog.ActionUpdateRolloutSet, set.Action)
	assert.Equal(t, "main", set.TargetID)
	assert.Equal(t, h.appId, set.AppID)
	assert.Equal(t, map[string]any{"runtime_version": "1", "percentage": 50}, set.Metadata)

	_, err = h.rolloutService.RevertUpdateRollout(ctx, h.appId, "main", "1", nil)
	require.NoError(t, err)
	require.Len(t, recorder.events, 2)
	assert.Equal(t, auditlog.ActionUpdateRolloutReverted, recorder.events[1].Action)
	for _, event := range recorder.events {
		assert.NotEqual(t, auditlog.ActionUpdateRollback, event.Action)
		assert.NotEqual(t, auditlog.ActionUpdateRepublished, event.Action)
	}
}

func TestCliRollbackAndRepublishEmitAuditEvents(t *testing.T) {
	h := newRolloutTestHarness(t)
	recorder := &fakeAuditRecorder{}
	h.deploymentService.SetOnAuditEvent(recorder.Record)
	h.seed(seedRow{branch: "main", rtv: "1", platform: "ios", id: 100, checked: true})

	rollback, err := h.deploymentService.CreateRollback(cliPublishCtx(), h.appId, "ios", "abc1234", "1", "main", "")
	require.NoError(t, err)
	require.Len(t, recorder.events, 1)
	rolledBack := recorder.events[0]
	assert.Equal(t, auditlog.ActionUpdateRollback, rolledBack.Action)
	// The CLI credential is the actor: the compromised-key investigation path.
	assert.Equal(t, auditlog.ActorAPIKey, rolledBack.ActorType)
	assert.Equal(t, "42", rolledBack.ActorID)
	assert.Equal(t, "ci-production", rolledBack.ActorDisplay)
	assert.Equal(t, rollback.UpdateId, rolledBack.TargetID)
	assert.Equal(t, h.appId, rolledBack.AppID)
	assert.Equal(t, map[string]any{
		"branch": "main", "runtime_version": "1", "platform": "ios", "commit_hash": "abc1234",
	}, rolledBack.Metadata)

	_, err = h.deploymentService.RepublishUpdate(cliPublishCtx(),
		&types.Update{AppId: h.appId, Branch: "main", RuntimeVersion: "1", UpdateId: "100"}, "ios", "def5678", nil)
	require.NoError(t, err)
	require.Len(t, recorder.events, 2)
	republished := recorder.events[1]
	assert.Equal(t, auditlog.ActionUpdateRepublished, republished.Action)
	assert.Equal(t, "100", republished.Metadata["source_update_id"])
}

func TestChannelRolloutEventsEmitAuditEvents(t *testing.T) {
	ctx := adminManagementCtx()
	h := newRolloutTestHarness(t)
	recorder := &fakeAuditRecorder{}
	rolloutService := NewRolloutService(channelRolloutCapableRepo{h.rolloutRepo},
		h.channelRepo, h.updateRepo, h.deploymentService)
	rolloutService.SetOnAuditEvent(recorder.Record)

	_, err := rolloutService.StartChannelRollout(ctx, h.appId, "production", "canary", 20)
	require.NoError(t, err)
	require.Len(t, recorder.events, 1)
	started := recorder.events[0]
	assert.Equal(t, auditlog.ActionChannelRolloutStarted, started.Action)
	assert.Equal(t, "production", started.TargetID)
	assert.Equal(t, map[string]any{"branch": "canary", "percentage": 20}, started.Metadata)

	_, err = rolloutService.UpdateChannelRolloutPercentage(ctx, h.appId, "production", 40)
	require.NoError(t, err)
	require.Len(t, recorder.events, 2)
	assert.Equal(t, auditlog.ActionChannelRolloutUpdated, recorder.events[1].Action)
	assert.Equal(t, map[string]any{"percentage": 40}, recorder.events[1].Metadata)

	require.NoError(t, rolloutService.EndChannelRollout(ctx, h.appId, "production", ChannelRolloutOutcomePromote))
	require.Len(t, recorder.events, 3)
	assert.Equal(t, auditlog.ActionChannelRolloutEnded, recorder.events[2].Action)
	assert.Equal(t, map[string]any{"result": "promote"}, recorder.events[2].Metadata)
}
