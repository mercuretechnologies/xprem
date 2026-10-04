// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package symbolication

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// esbuild's map of a ten-line file; the decoded table was checked by hand.
const cartMap = `{
  "version": 3,
  "sources": ["cart.js"],
  "mappings": "AAAA,SAAS,QAAQA,EAAMC,EAAM,CAC3B,GAAI,CAACA,EAAK,MACR,MAAM,IAAI,MAAM,mBAAmB,EAErC,OAAAD,EAAK,MAAQA,EAAK,MAAQC,EAAK,MACxBD,CACT,CAEA,MAAM,KAAO,CAAE,MAAO,CAAE,EACxB,QAAQ,KAAM,CAAE,KAAM,OAAQ,CAAC",
  "names": ["cart", "item"]
}`

func TestParseDecodesSegments(t *testing.T) {
	m, err := Parse([]byte(cartMap))
	require.NoError(t, err)
	require.Len(t, m.Segments, 36)
	assert.Equal(t, Segment{Column: 17, Source: 0, OriginalLine: 0, OriginalColumn: 17, Name: 0}, m.Segments[2], "third segment names cart")
	assert.Equal(t, Segment{Column: 22, Source: 0, OriginalLine: 1, OriginalColumn: 2, Name: NoIndex}, m.Segments[5], "a negative column delta moves back to the start of line 2")
}

// The generated column restarts at each ";", the other totals carry on.
func TestParseRestartsTheColumnOnEachGeneratedLine(t *testing.T) {
	m, err := Parse([]byte(`{"version":3,"sources":["a.js"],"names":[],"mappings":"AAAA,SAAS;IAAI"}`))
	require.NoError(t, err)
	require.Len(t, m.Segments, 3)
	assert.Equal(t, Segment{Line: 1, Column: 4, Source: 0, OriginalLine: 0, OriginalColumn: 13, Name: NoIndex}, m.Segments[2])

	index := openIndexOf(t, m)
	pos, ok, err := index.Lookup(1, 6)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, 14, pos.Column)
}

func TestDecodeVLQ(t *testing.T) {
	for encoded, want := range map[string][]int64{
		"AAAA":  {0, 0, 0, 0},
		"SAAS":  {9, 0, 0, 9},
		"QAAQA": {8, 0, 0, 8, 0},
		"D":     {-1},
		"3B":    {-27},
		"6rqS":  {300221},
	} {
		got, err := decodeVLQ(encoded)
		require.NoError(t, err, encoded)
		assert.Equal(t, want, got, encoded)
	}
	for _, broken := range []string{"g", "A!", "gggggggggA"} {
		_, err := decodeVLQ(broken)
		assert.ErrorIs(t, err, ErrInvalidMap, broken)
	}
}

func TestParseRefusesUnusableMaps(t *testing.T) {
	for name, data := range map[string]string{
		"not json":        `{`,
		"bom":             "\xef\xbb\xbf" + cartMap,
		"version 2":       `{"version":2,"sources":[],"names":[],"mappings":"AAAA"}`,
		"no mappings":     `{"version":3,"sources":[],"names":[]}`,
		"bad character":   `{"version":3,"sources":["a"],"names":[],"mappings":"AA!A"}`,
		"source overflow": `{"version":3,"sources":["a"],"names":[],"mappings":"AEAA"}`,
		"truncated vlq":   `{"version":3,"sources":["a"],"names":[],"mappings":"g"}`,
		"column overflow": `{"version":3,"sources":["a"],"names":[],"mappings":"ggggggIAAA"}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(data))
			assert.ErrorIs(t, err, ErrInvalidMap)
		})
	}
}
