// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package apikeyrestrictions

import (
	"net/netip"
	"reflect"
	"testing"

	"expo-open-ota/internal/database/postgres/pgdb"
)

func pattern(value string) *string { return &value }

// The LEFT JOIN yields one row per rule, and a key with no rule yields one
// null-extended row. Folding that back is the only logic in the store.
func TestFoldAccessRows(t *testing.T) {
	allowed := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	rows := []pgdb.GetApiKeyAccessByAppIDRow{
		// A scoped key: two rules, so two rows repeating the key's columns.
		{ID: 1, AllowedIps: allowed, AllowBranchCreation: true, Pattern: pattern("production"), Actions: []string{"read"}},
		{ID: 1, AllowedIps: allowed, AllowBranchCreation: true, Pattern: pattern("pr-*"), Actions: []string{"read", "publish"}},
		// A key at its default, between two scoped ones: one row, NULL pattern.
		{ID: 2, AllowBranchCreation: false},
		{ID: 3, AllowBranchCreation: true, Pattern: pattern("staging"), Actions: []string{"rollback"}},
	}

	expected := []ApiKeyAccess{
		{ApiKeyID: 1, AllowedIps: allowed, AllowBranchCreation: true, BranchRules: []BranchRule{
			{Pattern: "production", Actions: []Action{ActionRead}},
			{Pattern: "pr-*", Actions: []Action{ActionRead, ActionPublish}},
		}},
		{ApiKeyID: 2, AllowBranchCreation: false},
		{ApiKeyID: 3, AllowBranchCreation: true, BranchRules: []BranchRule{
			{Pattern: "staging", Actions: []Action{ActionRollback}},
		}},
	}
	if got := foldAccessRows(rows); !reflect.DeepEqual(got, expected) {
		t.Fatalf("unexpected fold:\n got %+v\nwant %+v", got, expected)
	}
}

func TestFoldAccessRowsOnNoRows(t *testing.T) {
	if got := foldAccessRows(nil); len(got) != 0 {
		t.Fatalf("expected no entries, got %+v", got)
	}
}

// An action string the catalog does not know can only come from a hand-written
// row. Dropping it is what keeps a future release from deciding what it means;
// the rule survives with whatever it legitimately grants, and a rule left with
// nothing grants nothing.
func TestFoldAccessRowsDropsUnknownActions(t *testing.T) {
	rows := []pgdb.GetApiKeyAccessByAppIDRow{
		{ID: 1, Pattern: pattern("production"), Actions: []string{"publish", "delete"}},
		{ID: 2, Pattern: pattern("staging"), Actions: []string{"delete"}},
	}
	folded := foldAccessRows(rows)

	if want := []Action{ActionPublish}; !reflect.DeepEqual(folded[0].BranchRules[0].Actions, want) {
		t.Fatalf("expected the unknown action dropped, got %+v", folded[0].BranchRules[0].Actions)
	}
	if len(folded[1].BranchRules[0].Actions) != 0 {
		t.Fatalf("expected no action to survive, got %+v", folded[1].BranchRules[0].Actions)
	}
	// And a rule left with no action grants nothing, on any action.
	for _, action := range AllActions {
		if AllowsBranch(folded[1].BranchRules, "staging", action) {
			t.Fatalf("a rule with no surviving action granted %q", action)
		}
	}
}
