// Integration tests for publish_group persistence: the column is written by
// both insert paths (plain publish, rollout publish) and surfaced by the
// branch listing. Same TEST_DATABASE_URL gating as the rollout store tests.
package store_test

import (
	"context"
	"strconv"
	"testing"
	"xprem/internal/types"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// checkedUpdate publishes and checks one update, stamping a stored uuid so the
// listing resolves it without reaching for bucket metadata.
func (f *rolloutFixture) checkedUpdate(t *testing.T, updateId int64, platform types.Platform, publishGroup *string) {
	t.Helper()
	ctx := context.Background()
	created, err := f.updates.CreateUpdate(ctx, f.appId, updateId, rolloutTestDefaultBranch, rolloutTestRuntime, platform, "abc123", "", publishGroup)
	require.NoError(t, err)
	require.NoError(t, f.updates.MarkUpdateAsChecked(ctx, *created))
	require.NoError(t, f.updates.StoreUpdateUUIDInMetadata(ctx, *created, uuid.NewString()))
}
