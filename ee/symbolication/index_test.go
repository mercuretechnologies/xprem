// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package symbolication

import (
	"bytes"
	"context"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openIndexOf(t *testing.T, m *Map) *Index {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, WriteIndex(&buf, m))
	index, err := OpenIndex(bytes.NewReader(buf.Bytes()))
	require.NoError(t, err)
	return index
}

func TestLookupResolvesTheCrashColumn(t *testing.T) {
	m, err := parseMap(context.Background(), []byte(cartMap), maxIndexCacheBytes)
	require.NoError(t, err)
	index := openIndexOf(t, m)

	// "at addItem (cart.min.js:1:41)": column 41 is one-based.
	pos, ok, err := index.Lookup(0, 40)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, Position{Source: "cart.js", Line: 3, Column: 11}, pos)

	pos, ok, err = index.Lookup(0, 17)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "cart", pos.Name)

	_, ok, err = index.Lookup(1, 5)
	require.NoError(t, err)
	assert.False(t, ok, "no segment on a line the map does not have")
}

func TestSourceTextIsReadBySpan(t *testing.T) {
	m := &Map{
		Sources:        []string{"a.js", "empty.js", "b.js"},
		SourcesContent: []string{"const a = 1;\nexport default a;\n", "", "let b;"},
		Ignored:        []bool{false, false, true},
		Segments:       []Segment{{Column: 0, Source: 0, Name: NoIndex}, {Column: 20, Source: 2, Name: NoIndex}},
	}
	index := openIndexOf(t, m)
	for i, want := range m.SourcesContent {
		got, err := index.SourceText(i)
		require.NoError(t, err)
		assert.Equal(t, want, got, "source %d", i)
	}
	_, err := index.SourceText(3)
	assert.ErrorIs(t, err, ErrInvalidIndex)

	// The texts sit after the segments: a lookup still reads the right segments.
	pos, ok, err := index.Lookup(0, 25)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "b.js", pos.Source)
}

func TestOpenIndexRefusesForeignFiles(t *testing.T) {
	_, err := OpenIndex(bytes.NewReader([]byte("not an index at all, but long enough for a header.......")))
	assert.ErrorIs(t, err, ErrInvalidIndex)
}

func TestIndexReadersRejectUnsupportedVersions(t *testing.T) {
	m, err := parseMap(context.Background(), []byte(cartMap), maxIndexCacheBytes)
	require.NoError(t, err)
	var buf bytes.Buffer
	require.NoError(t, WriteIndex(&buf, m))
	require.Equal(t, uint32(2), le.Uint32(buf.Bytes()[4:]), "new indexes keep format v2")
	for name, version := range map[string]uint32{"old version": 1, "future version": 3} {
		t.Run(name, func(t *testing.T) {
			stored := append([]byte(nil), buf.Bytes()...)
			le.PutUint32(stored[4:], version)
			_, err := OpenIndex(bytes.NewReader(stored))
			assert.ErrorIs(t, err, ErrInvalidIndex)
			_, err = ReadSegmentCount(bytes.NewReader(stored))
			assert.ErrorIs(t, err, ErrInvalidIndex)
		})
	}
}

// A synthetic map with many fences: every lookup through the index must
// agree with a linear scan of the decoded segments.
func TestLookupAgreesWithLinearScanAcrossFences(t *testing.T) {
	sources := []string{"a.js", "b.js", "node_modules/c.js"}
	names := []string{"f", "g"}
	var segments []Segment
	column := uint32(0)
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 5*fenceStride+17; i++ {
		column += uint32(1 + rng.Intn(40))
		s := Segment{Column: column, Source: uint32(rng.Intn(3)), OriginalLine: uint32(rng.Intn(500)), OriginalColumn: uint32(rng.Intn(80)), Name: NoIndex}
		if rng.Intn(3) == 0 {
			s.Name = uint32(rng.Intn(2))
		}
		if rng.Intn(50) == 0 {
			s.Source, s.Name = NoIndex, NoIndex
		}
		segments = append(segments, s)
	}
	m := &Map{Sources: sources, Names: names, Ignored: []bool{false, false, true}, Segments: segments}
	index := openIndexOf(t, m)
	require.Equal(t, len(segments), int(index.h.SegmentCount))

	for trial := 0; trial < 2000; trial++ {
		target := uint32(rng.Intn(int(column) + 50))
		pos, ok, err := index.Lookup(0, target)
		require.NoError(t, err)
		var want *Segment
		for i := range segments {
			if segments[i].Column <= target {
				want = &segments[i]
			}
		}
		if want == nil || want.Source == NoIndex {
			assert.False(t, ok, "column %d", target)
			continue
		}
		require.True(t, ok, "column %d", target)
		assert.Equal(t, sources[want.Source], pos.Source)
		assert.Equal(t, int(want.OriginalLine)+1, pos.Line)
		assert.Equal(t, int(want.OriginalColumn)+1, pos.Column)
		assert.Equal(t, want.Source == 2, pos.Ignored)
	}

}

// The Hermes map of the example app, when its export is present. Every
// segment of the decoded map must resolve to itself through the index.
func TestHermesExampleMap(t *testing.T) {
	matches, _ := filepath.Glob("../../apps/example-app/dist/_expo/static/js/ios/*.hbc.map")
	if len(matches) == 0 {
		t.Skip("no example-app export")
	}
	data, err := os.ReadFile(matches[0])
	require.NoError(t, err)
	m, err := parseMap(context.Background(), data, maxIndexCacheBytes)
	require.NoError(t, err)
	var raw struct {
		Sources        []string  `json:"sources"`
		SourcesContent []*string `json:"sourcesContent"`
	}
	require.NoError(t, json.Unmarshal(data, &raw))
	assert.Equal(t, len(raw.Sources), len(m.Sources))

	var buf bytes.Buffer
	require.NoError(t, WriteIndex(&buf, m))
	t.Logf("map %d bytes, %d segments, index %d bytes", len(data), len(m.Segments), buf.Len())
	index, err := OpenIndex(bytes.NewReader(buf.Bytes()))
	require.NoError(t, err)
	for i := 0; i < len(m.Segments); i += 97 {
		// Several segments can share a column; the last one wins.
		s := m.Segments[i]
		for i+1 < len(m.Segments) && m.Segments[i+1].Column == s.Column {
			i++
			s = m.Segments[i]
		}
		if s.Source == NoIndex {
			continue
		}
		pos, ok, err := index.Lookup(0, s.Column)
		require.NoError(t, err)
		require.True(t, ok, "segment %d at column %d", i, s.Column)
		assert.Equal(t, m.Sources[s.Source], pos.Source)
		assert.Equal(t, int(s.OriginalLine)+1, pos.Line)
	}
	for _, i := range []int{0, len(m.Sources) / 2, len(m.Sources) - 1} {
		text, err := index.SourceText(i)
		require.NoError(t, err)
		want := ""
		if raw.SourcesContent[i] != nil {
			want = *raw.SourcesContent[i]
		}
		assert.Equal(t, want, text, "source %d", i)
	}
}
