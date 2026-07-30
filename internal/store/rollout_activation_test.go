// Activation-time invariants of per-update rollouts: a rollout's control_update_id is
// captured at insert time but only goes live at MarkUpdateAsChecked, one bundle upload later.
// Needs a real Postgres and skips without TEST_DATABASE_URL:
//
//	docker run -d --name eoo-pg -e POSTGRES_PASSWORD=test -p 55432:5432 postgres:16-alpine
//	TEST_DATABASE_URL="postgres://postgres:test@localhost:55432/postgres?sslmode=disable" go test ./internal/store/
package store_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRolloutActivationRejectsStaleControl verifies a rollout must not activate with
// the control it captured at insert time if a plain update completed in between.
func TestRolloutActivationRejectsStaleControl(t *testing.T) {
	fixture := newRolloutFixture(t)
	ctx := context.Background()

	// The update everyone is running before either publish starts.
	fixture.createUpdate(t, rolloutTestDefaultBranch, 4000, "ios", true)

	// A plain publish's row exists, unchecked, while its bundle uploads.
	plainUpdate := fixture.createUpdate(t, rolloutTestDefaultBranch, 4100, "ios", false)

	// A rollout publish starts while that upload is still running; its control is
	// resolved against a branch where 4100 is not yet visible.
	rolloutUpdate := fixture.createRolloutUpdate(t, rolloutTestDefaultBranch, 4200, "ios", 10)

	// The plain upload finishes first and is accepted: no rollout is active yet.
	require.NoError(t, fixture.updates.MarkUpdateAsChecked(ctx, plainUpdate))

	// Two outcomes are acceptable here: refused activation, or activation with a control
	// resolved at activation time. What must never happen is the control captured at insert time.
	rolloutErr := fixture.updates.MarkUpdateAsChecked(ctx, rolloutUpdate)

	envelope, err := fixture.updates.GetLatestUpdateWithRollout(ctx, fixture.appId, rolloutTestDefaultBranch, rolloutTestRuntime, "ios")
	require.NoError(t, err)
	require.NotNil(t, envelope)

	if rolloutErr != nil {
		assert.Equal(t, "4100", envelope.UpdateId, "the plain publish stays live when the rollout is refused")
		assert.Nil(t, envelope.RolloutPercentage, "no rollout may be active after a refused activation")
		return
	}

	require.Equal(t, "4200", envelope.UpdateId, "the rollout update is the one being served")
	require.NotNil(t, envelope.RolloutPercentage)
	require.Equal(t, 10, *envelope.RolloutPercentage)

	// The out-of-bucket cohort must receive the update that was live when the rollout activated (4100).
	require.NotNil(t, envelope.Control, "out-of-bucket devices must have a control update")
	assert.Equal(t, "4100", envelope.Control.UpdateId,
		"control must reflect the branch at activation time; 4000 here means publish 4100 succeeded but is served to nobody")
}
