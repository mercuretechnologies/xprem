package test

import (
	"testing"
	"xprem/internal/types"
	"xprem/internal/update"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A resolved update whose metadata.json is absent from storage (phantom row
// after a storage switch, partial upload) must surface as a typed error, not
// as an empty metadata that later masquerades as "platform not supported".
func TestGetMetadataMissingReturnsSentinel(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	phantom := types.Update{AppId: "test-app-id", Branch: "branch-1", RuntimeVersion: "1", UpdateId: "999999"}
	_, err := update.GetMetadata(phantom)
	require.ErrorIs(t, err, update.ErrUpdateMetadataMissing)
	assert.Contains(t, err.Error(), "test-app-id/branch-1/1/999999")
}
