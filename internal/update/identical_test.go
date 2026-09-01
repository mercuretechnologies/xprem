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

	assert.False(t, AreUpdatesIdentical(stored, mapping("launch", "a", "a")), "duplicate hash is not the same content as two distinct assets")
	assert.True(t, AreUpdatesIdentical(mapping("launch", "a", "a"), mapping("launch", "a", "a")), "duplicate hashes on both sides")

	withConfig := func(m *types.UpdateAssetMapping, pathHashPairs ...string) *types.UpdateAssetMapping {
		for i := 0; i < len(pathHashPairs); i += 2 {
			m.ConfigFiles = append(m.ConfigFiles, types.ConfigFile{Path: pathHashPairs[i], Hash: pathHashPairs[i+1]})
		}
		return m
	}
	configured := withConfig(mapping("launch", "a", "b"), "metadata.json", "m1", "expoConfig.json", "c1")

	assert.True(t, AreUpdatesIdentical(configured, withConfig(mapping("launch", "a", "b"), "metadata.json", "m1", "expoConfig.json", "c1")),
		"same code and same config files")
	assert.True(t, AreUpdatesIdentical(configured, withConfig(mapping("launch", "a", "b"), "expoConfig.json", "c1", "metadata.json", "m1")),
		"config file order is not content")
	assert.False(t, AreUpdatesIdentical(configured, withConfig(mapping("launch", "a", "b"), "metadata.json", "m1", "expoConfig.json", "c2")),
		"an expo config change is a different update even with an identical bundle")
	assert.False(t, AreUpdatesIdentical(configured, withConfig(mapping("launch", "a", "b"), "metadata.json", "m1")),
		"a config file removed")
	assert.False(t, AreUpdatesIdentical(mapping("launch", "a", "b"), withConfig(mapping("launch", "a", "b"), "expoConfig.json", "c1")),
		"a mapping stored before config files were recorded never refuses a publish")

	assert.False(t, AreUpdatesIdentical(nil, mapping("launch", "a", "b")), "update published before mappings existed")
	assert.False(t, AreUpdatesIdentical(stored, nil), "CLI too old to send a mapping")
	assert.False(t, AreUpdatesIdentical(mapping("", "a"), mapping("", "a")), "a mapping without a bundle is not content")
}
