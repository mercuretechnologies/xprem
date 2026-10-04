// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

// Package symbolication builds the index of a source map and resolves
// stack frames through it.
package symbolication

import (
	"context"
	"errors"
	"fmt"
	"math"
)

// ErrInvalidMap reports a file that is not a usable source map. Retrying
// cannot fix it.
var ErrInvalidMap = errors.New("invalid source map")

// Map is a parsed source map.
type Map struct {
	Sources []string
	// SourcesContent is the text of each source, "" when the map carries none.
	SourcesContent []string
	Names          []string
	Ignored        []bool
	Segments       []Segment
}

// Segment maps every generated position from (Line, Column) up to the next
// segment to one original position. Source and Name are NoIndex when the
// segment carries none.
type Segment struct {
	Line   uint32
	Column uint32
	Source uint32
	// OriginalLine and OriginalColumn are zero-based, as the map encodes them.
	OriginalLine   uint32
	OriginalColumn uint32
	Name           uint32
}

// NoIndex is the Source or Name of a segment that names none.
const NoIndex = ^uint32(0)

type rawMap struct {
	Version          int       `json:"version"`
	Sources          []string  `json:"sources"`
	Names            []string  `json:"names"`
	Mappings         string    `json:"mappings"`
	SourcesContent   []*string `json:"sourcesContent"`
	IgnoreList       []int     `json:"ignoreList"`
	GoogleIgnoreList []int     `json:"x_google_ignoreList"`
}

// Parse decodes a source map. Any shape the index cannot use is ErrInvalidMap.
func Parse(data []byte) (*Map, error) {
	return parseMap(context.Background(), data, maxIndexCacheBytes)
}

func parseMap(ctx context.Context, data []byte, maxDecodedBytes int) (*Map, error) {
	raw, budget, err := readMap(ctx, data, maxDecodedBytes)
	if err != nil {
		return nil, err
	}
	segments, err := decodeMappings(ctx, raw.Mappings, len(raw.Sources), len(raw.Names), budget/segmentSize)
	if err != nil {
		return nil, err
	}
	ignored := make([]bool, len(raw.Sources))
	for _, list := range [][]int{raw.IgnoreList, raw.GoogleIgnoreList} {
		for _, index := range list {
			if index >= 0 && index < len(ignored) {
				ignored[index] = true
			}
		}
	}
	contents := make([]string, len(raw.Sources))
	for i, content := range raw.SourcesContent {
		if i < len(contents) && content != nil {
			contents[i] = *content
		}
	}
	return &Map{
		Sources:        raw.Sources,
		SourcesContent: contents,
		Names:          raw.Names,
		Ignored:        ignored,
		Segments:       segments,
	}, nil
}

const base64Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

var base64Values = func() [256]int8 {
	var values [256]int8
	for i := range values {
		values[i] = -1
	}
	for i := 0; i < len(base64Alphabet); i++ {
		values[base64Alphabet[i]] = int8(i)
	}
	return values
}()

// decodeMappings turns the VLQ string into absolute segments, in generated
// order, which is the order the string lists them in: "," ends a segment
// and ";" ends a generated line.
func decodeMappings(ctx context.Context, mappings string, sources, names, maxSegments int) ([]Segment, error) {
	count, err := countSegments(ctx, mappings, maxSegments)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	segments := make([]Segment, 0, count)
	var fields [5]int64
	var totals runningTotals
	line, start := uint32(0), 0
	for i := 0; i <= len(mappings); i++ {
		if i%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if i < len(mappings) && mappings[i] != ',' && mappings[i] != ';' {
			continue
		}
		if encoded := mappings[start:i]; encoded != "" {
			deltas, err := decodeVLQInto(encoded, &fields)
			if err != nil {
				return nil, err
			}
			segment, err := totals.add(deltas, sources, names)
			if err != nil {
				return nil, err
			}
			segment.Line = line
			segments = append(segments, segment)
		}
		if i < len(mappings) && mappings[i] == ';' {
			line++
			totals.column = 0
		}
		start = i + 1
	}
	return segments, nil
}

// decodeVLQ reads the numbers of one segment, such as "SAAS" into 9, 0, 0, 9.
func decodeVLQ(encoded string) ([]int64, error) {
	var fields [5]int64
	return decodeVLQInto(encoded, &fields)
}

// decodeVLQInto reuses five fields instead of allocating for every segment.
func decodeVLQInto(encoded string, fields *[5]int64) ([]int64, error) {
	numbers := fields[:0]
	var value int64
	shift := uint(0)
	for i := 0; i < len(encoded); i++ {
		digit := base64Values[encoded[i]]
		if digit < 0 {
			return nil, fmt.Errorf("%w: unexpected character %q in mappings", ErrInvalidMap, encoded[i])
		}
		value |= int64(digit&31) << shift
		// A digit of 32 or more says the number goes on in the next one.
		if digit&32 != 0 {
			shift += 5
			if shift > 35 {
				return nil, fmt.Errorf("%w: VLQ value too long", ErrInvalidMap)
			}
			continue
		}
		if len(numbers) == 5 {
			return nil, fmt.Errorf("%w: segment with more than 5 fields", ErrInvalidMap)
		}
		// The lowest bit is the sign.
		if value&1 != 0 {
			numbers = append(numbers, -(value >> 1))
		} else {
			numbers = append(numbers, value>>1)
		}
		value, shift = 0, 0
	}
	if shift != 0 {
		return nil, fmt.Errorf("%w: truncated VLQ value", ErrInvalidMap)
	}
	return numbers, nil
}

// runningTotals holds the absolute values each segment's deltas add to.
type runningTotals struct {
	column, source, originalLine, originalColumn, name int64
}

// add applies a segment's deltas and returns the segment they describe.
func (t *runningTotals) add(deltas []int64, sources, names int) (Segment, error) {
	if len(deltas) != 1 && len(deltas) != 4 && len(deltas) != 5 {
		return Segment{}, fmt.Errorf("%w: segment with %d fields", ErrInvalidMap, len(deltas))
	}
	t.column += deltas[0]
	if t.column < 0 || t.column > math.MaxUint32 {
		return Segment{}, fmt.Errorf("%w: position out of range", ErrInvalidMap)
	}
	segment := Segment{Column: uint32(t.column), Source: NoIndex, Name: NoIndex}
	if len(deltas) >= 4 {
		t.source += deltas[1]
		t.originalLine += deltas[2]
		t.originalColumn += deltas[3]
		if t.source < 0 || t.source >= int64(sources) {
			return Segment{}, fmt.Errorf("%w: source index %d out of range", ErrInvalidMap, t.source)
		}
		if t.originalLine < 0 || t.originalColumn < 0 || t.originalLine > math.MaxUint32 || t.originalColumn > math.MaxUint32 {
			return Segment{}, fmt.Errorf("%w: position out of range", ErrInvalidMap)
		}
		segment.Source = uint32(t.source)
		segment.OriginalLine = uint32(t.originalLine)
		segment.OriginalColumn = uint32(t.originalColumn)
	}
	if len(deltas) == 5 {
		t.name += deltas[4]
		if t.name < 0 || t.name >= int64(names) {
			return Segment{}, fmt.Errorf("%w: name index %d out of range", ErrInvalidMap, t.name)
		}
		segment.Name = uint32(t.name)
	}
	return segment, nil
}
