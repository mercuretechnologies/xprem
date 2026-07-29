// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"fmt"
	"strings"
)

// sqlFragment is SQL text such as a column name, expression, or predicate. It is never a value.
type sqlFragment string

// sqlf is fmt.Sprintf narrowed to fragments.
func sqlf(format string, parts ...sqlFragment) string {
	values := make([]any, len(parts))
	for i, part := range parts {
		values[i] = string(part)
	}
	return fmt.Sprintf(format, values...)
}

// joinFragments is strings.Join for fragments.
func joinFragments(parts []sqlFragment, separator string) sqlFragment {
	text := make([]string, len(parts))
	for i, part := range parts {
		text[i] = string(part)
	}
	return sqlFragment(strings.Join(text, separator))
}
