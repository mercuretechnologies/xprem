package branch

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMatchPattern(t *testing.T) {
	cases := []struct {
		pattern string
		name    string
		want    bool
	}{
		{"production", "production", true},
		{"production", "Production", false}, // branch names are case sensitive
		{"production", "production-eu", false},
		{"*", "anything", true},
		{"*", "", true},
		{"pr-*", "pr-482", true},
		{"pr-*", "pr-", true},
		{"pr-*", "pr", false},
		{"pr-*", "release-pr-482", false},
		{"*-staging", "eu-staging", true},
		{"*-staging", "staging", false},
		{"pr-*-eu", "pr-482-eu", true},
		{"pr-*-eu", "pr-eu", false}, // the two anchors would have to overlap
		{"ab*ba", "aba", false},
		{"ab*ba", "abba", true},
		{"a*b*c", "azzbzzc", true},
		{"a*b*c", "azzc", false},
		// A literal "[" is a branch name, not a character class.
		{"beta[eu]", "beta[eu]", true},
	}
	for _, tc := range cases {
		t.Run(tc.pattern+"/"+tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, MatchPattern(tc.pattern, tc.name))
		})
	}
}

func TestCollapseWildcards(t *testing.T) {
	assert.Equal(t, "*", CollapseWildcards("**"))
	assert.Equal(t, "*", CollapseWildcards("****"))
	assert.Equal(t, "pr-*", CollapseWildcards("pr-**"))
	assert.Equal(t, "pr-*-eu", CollapseWildcards("pr-***-eu"))
	assert.Equal(t, "production", CollapseWildcards("production"))
}
