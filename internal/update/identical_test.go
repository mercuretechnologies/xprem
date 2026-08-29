package update

import (
	"testing"
	"xprem/internal/types"

	"github.com/stretchr/testify/assert"
)

func mapping(launch string, assets ...string) *types.UpdateAssetMapping {
	shaped := make([]types.ShapedAsset, 0, len(assets))
	for _, hash := range assets {
		shaped = append(shaped, types.ShapedAsset{Hash: hash})
	}
	return &types.UpdateAssetMapping{
		LaunchAsset: types.ShapedAsset{Hash: launch},
		Assets:      shaped,
	}
}

func TestAreUpdatesIdentical(t *testing.T) {
	stored := mapping("launch", "a", "b")

	assert.True(t, AreUpdatesIdentical(stored, mapping("launch", "a", "b")))
	assert.True(t, AreUpdatesIdentical(stored, mapping("launch", "b", "a")), "asset order is not content")

	assert.False(t, AreUpdatesIdentical(stored, mapping("launch2", "a", "b")), "bundle changed")
	assert.False(t, AreUpdatesIdentical(stored, mapping("launch", "a", "c")), "asset changed")
	assert.False(t, AreUpdatesIdentical(stored, mapping("launch", "a")), "asset removed")
	assert.False(t, AreUpdatesIdentical(stored, mapping("launch", "a", "b", "c")), "asset added")

	assert.False(t, AreUpdatesIdentical(nil, mapping("launch", "a", "b")), "update published before mappings existed")
	assert.False(t, AreUpdatesIdentical(stored, nil), "CLI too old to send a mapping")
	assert.False(t, AreUpdatesIdentical(mapping("", "a"), mapping("", "a")), "a mapping without a bundle is not content")
}
