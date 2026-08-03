package update

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

const validUUID = "9f1c1d2e-0000-4000-8000-000000000001"

// The count cap stops at five VALID entries, so filler never trips it: only the
// input cap keeps a header of nothing but invalid entries from being walked in
// full. A valid id placed past the boundary must not be reached.
func TestParseFailedUpdateIDsBoundsTheInput(t *testing.T) {
	raw := strings.Repeat("not-a-uuid,", maxFailedUpdateIDsRaw) + validUUID
	assert.Empty(t, ParseFailedUpdateIDs(raw))

	assert.Equal(t, []string{validUUID}, ParseFailedUpdateIDs(`"`+validUUID+`"`))
}
