// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package apikeyrestrictions

import (
	"testing"

	"xprem/internal/validation"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMatchBranchPattern(t *testing.T) {
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
			assert.Equal(t, tc.want, matchBranchPattern(tc.pattern, tc.name))
		})
	}
}

func TestAllowsBranch_NoRulesMeansEveryBranch(t *testing.T) {
	for _, action := range AllActions {
		assert.True(t, AllowsBranch(nil, "production", action))
		assert.True(t, AllowsBranch([]BranchRule{}, "production", action))
	}
}

func TestAllowsBranch_ScopedKey(t *testing.T) {
	rules := []BranchRule{
		{Pattern: "production", Actions: []Action{ActionRead}},
		{Pattern: "staging", Actions: []Action{ActionPublish}},
		{Pattern: "pr-*", Actions: []Action{ActionPublish, ActionRollback}},
	}

	assert.True(t, AllowsBranch(rules, "production", ActionRead))
	assert.False(t, AllowsBranch(rules, "production", ActionPublish))
	assert.False(t, AllowsBranch(rules, "production", ActionRollback))

	assert.True(t, AllowsBranch(rules, "staging", ActionPublish))
	assert.False(t, AllowsBranch(rules, "staging", ActionRollback))

	assert.True(t, AllowsBranch(rules, "pr-482", ActionRollback))
	assert.False(t, AllowsBranch(rules, "develop", ActionRead))
}

func TestAllowsBranch_WriteImpliesRead(t *testing.T) {
	rules := []BranchRule{{Pattern: "staging", Actions: []Action{ActionPublish}}}
	assert.True(t, AllowsBranch(rules, "staging", ActionRead))

	rollbackOnly := []BranchRule{{Pattern: "staging", Actions: []Action{ActionRollback}}}
	assert.True(t, AllowsBranch(rollbackOnly, "staging", ActionRead))
	assert.False(t, AllowsBranch(rollbackOnly, "staging", ActionPublish))
}

func TestAllowsBranch_ScopedKeyIsRefusedWithoutABranch(t *testing.T) {
	rules := []BranchRule{{Pattern: "*", Actions: []Action{ActionPublish}}}
	assert.False(t, AllowsBranch(rules, "", ActionPublish))
	assert.True(t, AllowsBranch(nil, "", ActionPublish))
}

func TestImplies_UnknownActionGrantsNothing(t *testing.T) {
	rules := []BranchRule{{Pattern: "production", Actions: []Action{"delete"}}}
	for _, action := range AllActions {
		assert.False(t, AllowsBranch(rules, "production", action),
			"an unrecognised action must not grant %q", action)
	}
}

func TestNormalizeBranchRules_OrdersAndDeduplicatesActions(t *testing.T) {
	normalized, err := NormalizeBranchRules([]BranchRule{{
		Pattern: "staging",
		Actions: []Action{ActionRollback, ActionRead, ActionRollback},
	}})
	require.NoError(t, err)
	require.Len(t, normalized, 1)
	assert.Equal(t, []Action{ActionRead, ActionRollback}, normalized[0].Actions)
}

func TestNormalizeBranchRules_Rejects(t *testing.T) {
	tooMany := make([]BranchRule, maxBranchRules+1)
	for i := range tooMany {
		tooMany[i] = BranchRule{Pattern: string(rune('a' + i%26)), Actions: []Action{ActionRead}}
	}

	cases := map[string][]BranchRule{
		"empty pattern":     {{Pattern: "", Actions: []Action{ActionRead}}},
		"path separator":    {{Pattern: "feature/x", Actions: []Action{ActionRead}}},
		"control character": {{Pattern: "bad\nname", Actions: []Action{ActionRead}}},
		"no action":         {{Pattern: "staging"}},
		"unknown action":    {{Pattern: "staging", Actions: []Action{"delete"}}},
		"duplicate pattern": {
			{Pattern: "staging", Actions: []Action{ActionRead}},
			{Pattern: "staging", Actions: []Action{ActionPublish}},
		},
		"too many rules": tooMany,
	}
	for name, rules := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := NormalizeBranchRules(rules)
			require.Error(t, err)
			// Must be a validation error so the handler answers 400.
			assert.True(t, validation.IsValidationError(err))
		})
	}
}

func TestNormalizeBranchRules_AcceptsWildcards(t *testing.T) {
	normalized, err := NormalizeBranchRules([]BranchRule{
		{Pattern: "*", Actions: []Action{ActionRead}},
		{Pattern: "pr-*", Actions: []Action{ActionPublish}},
	})
	require.NoError(t, err)
	assert.Len(t, normalized, 2)
}

// "*" and "**" are the same set of branches, so they collapse to one rule.
func TestNormalizeBranchRules_CollapsesWildcardRuns(t *testing.T) {
	normalized, err := NormalizeBranchRules([]BranchRule{
		{Pattern: "a**b", Actions: []Action{ActionRead}},
	})
	require.NoError(t, err)
	assert.Equal(t, "a*b", normalized[0].Pattern)

	_, err = NormalizeBranchRules([]BranchRule{
		{Pattern: "*", Actions: []Action{ActionRead}},
		{Pattern: "***", Actions: []Action{ActionPublish}},
	})
	require.Error(t, err)
	assert.True(t, validation.IsValidationError(err))
}

func TestDescribeBranchRules(t *testing.T) {
	described := describeBranchRules([]BranchRule{
		{Pattern: "pr-*", Actions: []Action{ActionRead, ActionPublish}},
	})
	assert.Equal(t, []string{"pr-*:read+publish"}, described)
}
