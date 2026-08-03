package branch

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The stored default for a channel that never opened branch surfing. It must
// name nothing, or a first careless click exposes the whole app.
func TestEmptyPatternMatchesNothing(t *testing.T) {
	for _, name := range []string{"production", "pr-482", "*", "a"} {
		assert.False(t, MatchPattern("", name), name)
	}
}
