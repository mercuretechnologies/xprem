// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package symbolication

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"
)

var errMapBudget = errors.New("source map exceeds decoder memory budget")

func mapBudgetError() error {
	return fmt.Errorf("%w: %w", ErrInvalidMap, errMapBudget)
}

// readMap validates and counts array entries before allocating their decoded
// slices. The input length covers decoded string bytes and the mappings;
// additional charges cover their headers, pointer arrays and per-source state.
// This budget bounds the decoded representation, separately from the raw map
// and serialized index limits. RawMessage copies remain bounded by maxMapSize.
func readMap(ctx context.Context, data []byte, maxDecodedBytes int) (rawMap, int, error) {
	if err := ctx.Err(); err != nil {
		return rawMap{}, 0, err
	}
	if len(data) > maxMapSize || len(data) > maxDecodedBytes {
		return rawMap{}, 0, mapBudgetError()
	}
	// JSON's replacement of invalid UTF-8 could expand string bytes beyond
	// the input charge. Source maps must carry valid UTF-8 JSON.
	if !utf8.Valid(data) {
		return rawMap{}, 0, fmt.Errorf("%w: invalid UTF-8", ErrInvalidMap)
	}
	var encoded struct {
		Version          int             `json:"version"`
		Sources          json.RawMessage `json:"sources"`
		Names            json.RawMessage `json:"names"`
		Mappings         string          `json:"mappings"`
		SourcesContent   json.RawMessage `json:"sourcesContent"`
		IgnoreList       json.RawMessage `json:"ignoreList"`
		GoogleIgnoreList json.RawMessage `json:"x_google_ignoreList"`
	}
	if err := json.Unmarshal(data, &encoded); err != nil {
		return rawMap{}, 0, fmt.Errorf("%w: %v", ErrInvalidMap, err)
	}
	if err := ctx.Err(); err != nil {
		return rawMap{}, 0, err
	}
	if encoded.Version != 3 {
		return rawMap{}, 0, fmt.Errorf("%w: version %d, expected 3", ErrInvalidMap, encoded.Version)
	}
	if encoded.Mappings == "" {
		return rawMap{}, 0, fmt.Errorf("%w: no mappings", ErrInvalidMap)
	}
	budget := maxDecodedBytes - len(data)
	var counts [5]int
	// Charges use the 64-bit Go layout, conservatively covering 32-bit builds.
	arrays := []struct {
		data     []byte
		perEntry int
	}{
		// Two string headers (path and output content) and the ignored flag.
		{encoded.Sources, 33},
		{encoded.Names, 16},
		// Raw content pointers and the string headers they point to.
		{encoded.SourcesContent, 24},
		{encoded.IgnoreList, 8},
		{encoded.GoogleIgnoreList, 8},
	}
	for i, array := range arrays {
		count, err := countArrayEntries(ctx, array.data, budget/array.perEntry)
		if err != nil {
			return rawMap{}, 0, err
		}
		counts[i] = count
		budget -= count * array.perEntry
	}
	if err := ctx.Err(); err != nil {
		return rawMap{}, 0, err
	}
	// The counts have been checked collectively. Exact capacities keep JSON's
	// append growth from exceeding the decoder budget.
	raw := rawMap{Version: encoded.Version, Mappings: encoded.Mappings}
	if counts[0] > 0 {
		raw.Sources = make([]string, 0, counts[0])
	}
	if counts[1] > 0 {
		raw.Names = make([]string, 0, counts[1])
	}
	if counts[2] > 0 {
		raw.SourcesContent = make([]*string, 0, counts[2])
	}
	if counts[3] > 0 {
		raw.IgnoreList = make([]int, 0, counts[3])
	}
	if counts[4] > 0 {
		raw.GoogleIgnoreList = make([]int, 0, counts[4])
	}
	for i, target := range []any{&raw.Sources, &raw.Names, &raw.SourcesContent, &raw.IgnoreList, &raw.GoogleIgnoreList} {
		if err := ctx.Err(); err != nil {
			return rawMap{}, 0, err
		}
		if len(arrays[i].data) > 0 {
			if err := json.Unmarshal(arrays[i].data, target); err != nil {
				return rawMap{}, 0, fmt.Errorf("%w: %v", ErrInvalidMap, err)
			}
		}
	}
	return raw, budget, nil
}

// countArrayEntries only counts top-level entries in JSON already validated
// by Unmarshal. Nested values are counted as one entry; typed decoding still
// rejects them when the source-map field requires a string or integer.
func countArrayEntries(ctx context.Context, data []byte, maximum int) (int, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return 0, ctx.Err()
	}
	if data[0] != '[' {
		return 0, fmt.Errorf("%w: expected an array", ErrInvalidMap)
	}
	if len(bytes.TrimSpace(data[1:len(data)-1])) == 0 {
		return 0, ctx.Err()
	}
	count := 1
	if count > maximum {
		return 0, mapBudgetError()
	}
	depth, quoted, escaped := 0, false, false
	for i, b := range data {
		if i%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
		}
		if quoted {
			if escaped {
				escaped = false
			} else if b == '\\' {
				escaped = true
			} else if b == '"' {
				quoted = false
			}
			continue
		}
		switch b {
		case '"':
			quoted = true
		case '[', '{':
			depth++
		case ']', '}':
			depth--
		case ',':
			if depth == 1 {
				count++
				if count > maximum {
					return 0, mapBudgetError()
				}
			}
		}
	}
	return count, nil
}

// countSegments rejects an expanded table before allocating its backing array.
func countSegments(ctx context.Context, mappings string, maximum int) (int, error) {
	count, inSegment := 0, false
	for i := 0; i < len(mappings); i++ {
		if i%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
		}
		if mappings[i] == ',' || mappings[i] == ';' {
			inSegment = false
		} else if !inSegment {
			count++
			if count > maximum {
				return 0, mapBudgetError()
			}
			inSegment = true
		}
	}
	return count, nil
}
