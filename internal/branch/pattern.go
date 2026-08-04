package branch

import "strings"

// MatchPattern reports whether name matches pattern, "*" standing for any run
// of characters, empty included. The empty pattern matches no name. Branch
// names cannot contain "*", so a pattern without one is never ambiguous with a
// literal name. Callers normalize with CollapseWildcards first.
func MatchPattern(pattern, name string) bool {
	if pattern == "" {
		return false
	}
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

// CollapseWildcards rewrites any run of "*" as a single one, so that patterns
// naming the same set of branches compare equal.
func CollapseWildcards(pattern string) string {
	for strings.Contains(pattern, "**") {
		pattern = strings.ReplaceAll(pattern, "**", "*")
	}
	return pattern
}
