// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

// The vocabulary that keeps data out of the query text. Every read in this
// package builds its SQL by assembling fragments, and the only thing standing
// between a device model called `x' OR 1=1 --` and the query is that filter
// values travel as bound arguments while structure travels as text. That was a
// convention held up by comments; this file makes it a type the compiler
// checks.
package observe

import (
	"fmt"
	"strings"
)

// sqlFragment is SQL text: a column name, an expression, a predicate already
// assembled. Never a value. The distinction is not decorative: a value reaches
// ClickHouse through a bound argument and can say anything, while a fragment
// becomes part of the statement and can say anything only if someone let it.
//
// Converting a plain string into one is deliberately something you have to
// write, so `grep sqlFragment(` lists every place worth reviewing.
type sqlFragment string

// sqlf is fmt.Sprintf narrowed to fragments. Sprintf takes ...any, so a header,
// a query parameter or a device model can land in a %s and nothing complains;
// this signature makes that a compile error instead.
func sqlf(format string, parts ...sqlFragment) string {
	values := make([]any, len(parts))
	for i, part := range parts {
		values[i] = string(part)
	}
	return fmt.Sprintf(format, values...)
}

// joinFragments is strings.Join for fragments, so a list of column names can be
// spliced into a SELECT without a round trip through []string, which is exactly
// where the type would be lost.
func joinFragments(parts []sqlFragment, separator string) sqlFragment {
	text := make([]string, len(parts))
	for i, part := range parts {
		text[i] = string(part)
	}
	return sqlFragment(strings.Join(text, separator))
}
