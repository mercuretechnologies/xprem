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

	withPath := func(path, hash string) *types.UpdateAssetMapping {
		result := mapping(hash, "a", "b")
		result.LaunchAsset.Path = path
		return result
	}
	hbcA := "_expo/static/js/ios/index-6ea3d9f1274c0b3ff703609ed56be6a6.hbc"
	hbcB := "_expo/static/js/ios/index-21227ffa62562b8dc144afed29b813a7.hbc"
	assert.True(t, AreUpdatesIdentical(withPath(hbcA, "bytes1"), withPath(hbcA, "bytes2")),
		"same Metro-named hbc is the same content even though Hermes bytes differ per export")
	assert.False(t, AreUpdatesIdentical(withPath(hbcA, "bytes1"), withPath(hbcB, "bytes1")),
		"a different Metro name is different content whatever the bytes say")
	assert.False(t, AreUpdatesIdentical(mapping("launch", "a", "b"), withPath(hbcA, "launch2")),
		"a stored mapping without a path falls back to the byte hash")
	assert.True(t, AreUpdatesIdentical(mapping("launch", "a", "b"), withPath(hbcA, "launch")),
		"path on one side only still matches by hash")
	assert.False(t, AreUpdatesIdentical(withPath("bundles/android-82adadb1fb6e489d04ad95fd79670deb.js", "bytes1"), withPath("bundles/android-82adadb1fb6e489d04ad95fd79670deb.js", "bytes2")),
		"a plain-JS bundle is byte-deterministic: its name is never trusted over its bytes")
	assert.False(t, AreUpdatesIdentical(withPath("_expo/static/js/ios/index.hbc", "bytes1"), withPath("_expo/static/js/ios/index.hbc", "bytes2")),
		"an hbc without a Metro content hash in its name is never trusted by path")
	assert.True(t, AreUpdatesIdentical(withPath("bundles/android-82adadb1fb6e489d04ad95fd79670deb.js", "same"), withPath("bundles/android-82adadb1fb6e489d04ad95fd79670deb.js", "same")),
		"identical plain-JS bundles match by bytes as always")

	assert.False(t, AreUpdatesIdentical(nil, mapping("launch", "a", "b")), "update published before mappings existed")
	assert.False(t, AreUpdatesIdentical(stored, nil), "CLI too old to send a mapping")
	assert.False(t, AreUpdatesIdentical(mapping("", "a"), mapping("", "a")), "a mapping without a bundle is not content")
}
