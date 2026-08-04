package branch

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The stored default for a channel that never opened branch surfing. It must
// name nothing, or a first careless click exposes the whole app.
func TestEmptyPatternMatchesNothing(t *testing.T) {
	for _, name := range []string{"production", "pr-482", "*", "a", ""} {
		assert.False(t, MatchPattern("", name), name)
	}
}

// The empty name too: without the early return, the literal comparison below
// makes the empty pattern match it, which is the one case where the
// deny-by-default default would let something through.
func TestEmptyPatternMatchesTheEmptyNameEither(t *testing.T) {
	assert.False(t, MatchPattern("", ""))
}
