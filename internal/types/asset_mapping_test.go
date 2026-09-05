package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdateAssetMappingScanValue(t *testing.T) {
	original := UpdateAssetMapping{
		LaunchAsset: ShapedAsset{Hash: "h", Key: "k", FileExtension: ".hbc", ContentType: "application/javascript"},
		Assets:      []ShapedAsset{{Hash: "a", Key: "b", FileExtension: ".png", ContentType: "image/png"}},
	}
	raw, err := original.Value()
	require.NoError(t, err)

	var scanned UpdateAssetMapping
	require.NoError(t, scanned.Scan(raw))
	assert.Equal(t, original, scanned)

	var empty UpdateAssetMapping
	require.NoError(t, empty.Scan(nil))
	assert.Equal(t, UpdateAssetMapping{}, empty)
}
