// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package apikeyrestrictions

import (
	"expo-open-ota/internal/validation"
	"fmt"
	"strings"
)

// maxBranchRules bounds one key's rule list: every CLI request walks it, and a
// key needing fifty rules wanted a wildcard.
const maxBranchRules = 50

// Action is what a CLI request does to the branch it names.
type Action string

const (
	ActionRead     Action = "read"
	ActionPublish  Action = "publish"
	ActionRollback Action = "rollback" // rollback and republish: no working copy needed
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

// Implies reports whether granting a covers a request for b. Both writes cover
// a read of the same branch: eoas lists a branch's runtime versions before
// publishing to it, so publish alone would fail mid-command on a route the
// operator never thought to grant.
//
// publish does NOT cover rollback, deliberately, even though it is the more
// powerful grant. Shipping new code and putting an already-retired update back
// in front of the fleet are separate decisions, and a CI token that only ever
// needs the first should not silently hold the second.
//
// Spelled out rather than written as "a == b || b == ActionRead", which never
// looks at a and therefore lets ANY string in a rule grant read. Nothing
// unknown reaches here today (NormalizeBranchRules refuses it on write,
// toActions drops it on read), but that guarantee lives in another package
// while this is where the decision is made.
func (a Action) Implies(b Action) bool {
	if a == b {
		return true
	}
	return b == ActionRead && (a == ActionPublish || a == ActionRollback)
}

// BranchRule grants a set of actions on every branch matching Pattern, where
// "*" stands for any run of characters. An API key holds a list of them, and
// holding none means it reaches every branch of its app: the default a key has
// always had, and all a community deployment ever sees.
//
// Allow-list only, no deny form: rules that both grant and revoke need a
// precedence order, which is what nobody reads correctly at the third rule.
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
// refused for a scoped key: nothing matches branch rules, and on an allow-list
// that reads as no. The routing table makes it unreachable anyway
// (internal/router/access.go refuses a token route without a branch), so this
// is the backstop.
func AllowsBranch(rules []BranchRule, branchName string, action Action) bool {
	if len(rules) == 0 {
		return true
	}
	for _, rule := range rules {
		if branchName != "" && rule.Allows(branchName, action) {
			return true
		}
	}
	return false
}

// NormalizeBranchRules validates what the dashboard sent and returns the form
// to persist: actions deduplicated and reordered into the catalog order, so
// two equivalent lists are stored identically and the audit trail does not
// report a change that is none. Errors are validation errors, hence 400.
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
		// "*" and "**" mean the same set of branches, so they are stored the
		// same way. Without this the duplicate check below compares strings
		// that differ while the rules do not, and the key ends up with two
		// grants a reader has to union in their head.
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
		// A rule granting nothing reads as a restriction while being none:
		// deleting the rule says the same thing, clearly.
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
// characters, empty included. Branch names cannot contain "*"
// (validation.Name), so a pattern is never ambiguous with a literal.
//
// Written out rather than delegated to path.Match, which would also read "?"
// and character classes: a branch may be named "beta[eu]", and path.Match
// answers ErrBadPattern on it.
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
	// Inner segments in order. Leftmost occurrence is enough: they are
	// literals separated by gaps that absorb anything.
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
