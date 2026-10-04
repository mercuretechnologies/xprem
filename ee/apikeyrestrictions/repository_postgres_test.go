// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package apikeyrestrictions

import (
	"net/netip"
	"reflect"
	"testing"

	"xprem/internal/database/postgres/pgdb"
)

func pattern(value string) *string { return &value }

// The LEFT JOIN yields one row per rule; a key with no rule yields one
// null-extended row.
func TestFoldAccessRows(t *testing.T) {
	allowed := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	rows := []pgdb.GetApiKeyAccessByAppIDRow{
		// A scoped key: two rules, so two rows repeating the key's columns.
		{ID: 1, AllowedIps: allowed, Pattern: pattern("production"), Actions: []string{"read"}},
		{ID: 1, AllowedIps: allowed, Pattern: pattern("pr-*"), Actions: []string{"read", "publish"}},
		// A key at its default, between two scoped ones: one row, NULL pattern.
		{ID: 2},
		{ID: 3, Pattern: pattern("staging"), Actions: []string{"rollback"}},
	}

	expected := []ApiKeyAccess{
		{ApiKeyID: 1, AllowedIps: allowed, UpdateRules: []UpdateRule{
			{Pattern: "production", Actions: []UpdateAction{UpdateActionRead}},
			{Pattern: "pr-*", Actions: []UpdateAction{UpdateActionRead, UpdateActionPublish}},
		}},
		{ApiKeyID: 2},
		{ApiKeyID: 3, UpdateRules: []UpdateRule{
			{Pattern: "staging", Actions: []UpdateAction{UpdateActionRollback}},
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

// An unknown action can only come from a hand-written row; it is dropped and
// the rule survives with what remains.
func TestFoldAccessRowsDropsUnknownActions(t *testing.T) {
	rows := []pgdb.GetApiKeyAccessByAppIDRow{
		{ID: 1, Pattern: pattern("production"), Actions: []string{"publish", "delete"}},
		{ID: 2, Pattern: pattern("staging"), Actions: []string{"delete"}},
	}
	folded := foldAccessRows(rows)

	if want := []UpdateAction{UpdateActionPublish}; !reflect.DeepEqual(folded[0].UpdateRules[0].Actions, want) {
		t.Fatalf("expected the unknown action dropped, got %+v", folded[0].UpdateRules[0].Actions)
	}
	if len(folded[1].UpdateRules[0].Actions) != 0 {
		t.Fatalf("expected no action to survive, got %+v", folded[1].UpdateRules[0].Actions)
	}
	// A rule left with no action grants nothing.
	for _, action := range AllUpdateActions {
		if AllowsUpdates(folded[1].UpdateRules, "staging", action) {
			t.Fatalf("a rule with no surviving action granted %q", action)
		}
	}
}
