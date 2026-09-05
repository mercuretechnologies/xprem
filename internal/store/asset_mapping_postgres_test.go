// Integration tests for updates.asset_mapping: the JSONB round trip and the
// NULL a pre-CAS row reads back as. Same TEST_DATABASE_URL gating as the
// rollout store tests.
package store_test

import (
	"context"
	"testing"
	"xprem/internal/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdateAssetMappingRoundTripPostgres(t *testing.T) {
	fixture := newRolloutFixture(t)
	ctx := context.Background()

	created, err := fixture.updates.CreateUpdate(ctx, fixture.appId, 100, rolloutTestDefaultBranch, rolloutTestRuntime, "ios", "abc123", "", nil)
	require.NoError(t, err)

	// A row nobody stored a mapping on reads back nil, which is what tells the
	// publish path to fall back instead of blocking.
	stored, err := fixture.updates.GetUpdateAssetMapping(ctx, *created)
	require.NoError(t, err)
	assert.Nil(t, stored)

	mapping := &types.UpdateAssetMapping{
		LaunchAsset: types.ShapedAsset{Hash: "launch-hash", Key: "launch-key", FileExtension: ".bundle", ContentType: "application/javascript"},
		Assets:      []types.ShapedAsset{{Hash: "asset-hash", Key: "asset-key", FileExtension: ".png", ContentType: "image/png"}},
	}
	require.NoError(t, fixture.updates.StoreUpdateAssetMapping(ctx, *created, mapping))

	stored, err = fixture.updates.GetUpdateAssetMapping(ctx, *created)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, *mapping, *stored)
}

func TestGetUpdateAssetMappingUnknownUpdatePostgres(t *testing.T) {
	fixture := newRolloutFixture(t)

	stored, err := fixture.updates.GetUpdateAssetMapping(context.Background(), types.Update{
		AppId:    fixture.appId,
		Branch:   rolloutTestDefaultBranch,
		UpdateId: "999999",
	})
	require.NoError(t, err)
	assert.Nil(t, stored)
}
