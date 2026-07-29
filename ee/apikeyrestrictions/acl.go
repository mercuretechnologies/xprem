// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package apikeyrestrictions

import (
	"expo-open-ota/internal/validation"
	"fmt"
	"strings"
)

// maxBranchRules bounds the number of rules a single API key may hold.
const maxBranchRules = 50

// Action is what a CLI request does to the branch it names.
type Action string

const (
	ActionRead     Action = "read"
	ActionPublish  Action = "publish"
	ActionRollback Action = "rollback"
)

// AllActions is the catalog, in increasing order of trust.
var AllActions = []Action{ActionRead, ActionPublish, ActionRollback}

func IsValidAction(action string) bool {
	for _, known := range AllActions {
		if string(known) == action {
			return true
		}
	}
	return false
}

// Implies reports whether granting a covers a request for b. ActionPublish and
// ActionRollback both imply ActionRead, but ActionPublish does not imply
// ActionRollback.
func (a Action) Implies(b Action) bool {
	if a == b {
		return true
	}
	return b == ActionRead && (a == ActionPublish || a == ActionRollback)
}

// BranchRule grants a set of actions on every branch matching Pattern, where
// "*" stands for any run of characters. An API key holding no rules reaches
// every branch of its app.
type BranchRule struct {
	Pattern string
	Actions []Action
}

func (rule BranchRule) Allows(branchName string, action Action) bool {
	if !matchBranchPattern(rule.Pattern, branchName) {
		return false
	}
	for _, granted := range rule.Actions {
		if granted.Implies(action) {
			return true
		}
	}
	return false
}

// AllowsBranch is the decision for a whole rule list. An empty branchName is
// refused whenever the key has any rules.
func AllowsBranch(rules []BranchRule, branchName string, action Action) bool {
	if len(rules) == 0 {
		return true
	}
	if branchName == "" {
		return false
	}
	for _, rule := range rules {
		if rule.Allows(branchName, action) {
			return true
		}
	}
	return false
}

// NormalizeBranchRules validates the given rules and returns the form to
// persist, with actions deduplicated and reordered into catalog order.
func NormalizeBranchRules(rules []BranchRule) ([]BranchRule, error) {
	if len(rules) > maxBranchRules {
		return nil, validation.Errorf("rules", "a key cannot hold more than %d access rules", maxBranchRules)
	}
	normalized := make([]BranchRule, 0, len(rules))
	seen := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		if err := validation.NamePattern("pattern", rule.Pattern); err != nil {
			return nil, err
		}
		// "*" and "**" mean the same set of branches, so patterns are collapsed
		// before the duplicate check below.
		pattern := collapseWildcards(rule.Pattern)
		if _, duplicate := seen[pattern]; duplicate {
			return nil, validation.Errorf("pattern", "%q appears in more than one rule; merge them into one", pattern)
		}
		seen[pattern] = struct{}{}
		actions, err := normalizeActions(pattern, rule.Actions)
		if err != nil {
			return nil, err
		}
		normalized = append(normalized, BranchRule{Pattern: pattern, Actions: actions})
	}
	return normalized, nil
}

// collapseWildcards rewrites any run of "*" as a single one.
func collapseWildcards(pattern string) string {
	for strings.Contains(pattern, "**") {
		pattern = strings.ReplaceAll(pattern, "**", "*")
	}
	return pattern
}

func normalizeActions(pattern string, actions []Action) ([]Action, error) {
	granted := make(map[Action]struct{}, len(actions))
	for _, action := range actions {
		if !IsValidAction(string(action)) {
			return nil, validation.Errorf("actions", "unknown action %q on rule %q", string(action), pattern)
		}
		granted[action] = struct{}{}
	}
	if len(granted) == 0 {
		return nil, validation.Errorf("actions", "rule %q grants no action; delete the rule instead", pattern)
	}
	ordered := make([]Action, 0, len(granted))
	for _, action := range AllActions {
		if _, ok := granted[action]; ok {
			ordered = append(ordered, action)
		}
	}
	return ordered, nil
}

// matchBranchPattern matches name against pattern, "*" standing for any run of
// characters, including empty.
func matchBranchPattern(pattern, name string) bool {
	if !strings.Contains(pattern, "*") {
		return pattern == name
	}
	segments := strings.Split(pattern, "*")
	prefix, suffix := segments[0], segments[len(segments)-1]
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return false
	}
	// The anchors must not overlap: "ab*ba" does not match "aba".
	if len(prefix)+len(suffix) > len(name) {
		return false
	}
	rest := name[len(prefix) : len(name)-len(suffix)]
	// Inner segments must appear in order; leftmost occurrence is enough.
	for _, segment := range segments[1 : len(segments)-1] {
		index := strings.Index(rest, segment)
		if index < 0 {
			return false
		}
		rest = rest[index+len(segment):]
	}
	return true
}

// describeBranchRules renders a rule list for the audit trail.
func describeBranchRules(rules []BranchRule) []string {
	described := make([]string, 0, len(rules))
	for _, rule := range rules {
		actions := make([]string, 0, len(rule.Actions))
		for _, action := range rule.Actions {
			actions = append(actions, string(action))
		}
		described = append(described, fmt.Sprintf("%s:%s", rule.Pattern, strings.Join(actions, "+")))
	}
	return described
}
