package repository_test

import (
	"context"
	"testing"
	"xprem/internal/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdateSourcemapHashRoundTripPostgres(t *testing.T) {
	fixture := newRolloutFixture(t)
	ctx := context.Background()

	created, err := fixture.updates.CreateUpdate(ctx, fixture.appId, 100, rolloutTestDefaultBranch, rolloutTestRuntime, "ios", "abc123", "", nil)
	require.NoError(t, err)

	stored, err := fixture.updates.GetUpdateSourcemapHash(ctx, *created)
	require.NoError(t, err)
	assert.Nil(t, stored)

	require.NoError(t, fixture.updates.StoreUpdateSourcemapHash(ctx, *created, "map-hash"))

	stored, err = fixture.updates.GetUpdateSourcemapHash(ctx, *created)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, "map-hash", *stored)

	details, err := fixture.updates.GetUpdateDetails(ctx, fixture.appId, rolloutTestDefaultBranch, rolloutTestRuntime, created.UpdateId)
	require.NoError(t, err)
	require.NotNil(t, details.SourcemapHash)
	assert.Equal(t, "map-hash", *details.SourcemapHash)
}

func TestStoreUpdateSourcemapHashUnknownUpdatePostgres(t *testing.T) {
	fixture := newRolloutFixture(t)
	ctx := context.Background()
	unknown := types.Update{AppId: fixture.appId, Branch: rolloutTestDefaultBranch, RuntimeVersion: rolloutTestRuntime, UpdateId: "999999"}

	assert.Error(t, fixture.updates.StoreUpdateSourcemapHash(ctx, unknown, "map-hash"))
	stored, err := fixture.updates.GetUpdateSourcemapHash(ctx, unknown)
	require.NoError(t, err)
	assert.Nil(t, stored)
}
