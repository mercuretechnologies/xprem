// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package symbolication

import (
	"bytes"
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"xprem/internal/types"

	"github.com/riverqueue/river"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRejectsExpandedMappingsWithinRawLimit(t *testing.T) {
	data := []byte(`{"version":3,"mappings":"` + strings.Repeat("A,", 2048) + `"}`)
	// The raw input fits, but even its valid one-field segments do not fit
	// a 32 KiB decoded budget. Reject before making the segment table.
	m, err := parseMap(context.Background(), data, 32<<10)
	require.Nil(t, m)
	assert.ErrorIs(t, err, ErrInvalidMap)
	assert.ErrorIs(t, err, errMapBudget)
}

func TestParseDefaultBudgetRejectsExpansionBeforeAllocatingSegments(t *testing.T) {
	const segments = maxIndexCacheBytes/segmentSize + 1
	data := []byte(`{"version":3,"mappings":"` + strings.Repeat("A,", segments) + `"}`)
	require.Less(t, len(data), maxMapSize)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	m, err := parseMap(context.Background(), data, maxIndexCacheBytes)
	runtime.ReadMemStats(&after)
	require.Nil(t, m)
	assert.ErrorIs(t, err, errMapBudget)
	assert.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(3*len(data)), "the oversized segment table must never be allocated")

	store := newFakeStore()
	store.maps[testHash] = data
	service, indexes := newTestService(store)
	err = runJob(t, service, indexes, 1)
	var permanent *river.JobCancelError
	require.ErrorAs(t, err, &permanent)
	assert.Equal(t, types.SourcemapIndexCancelled, indexes.record.Status)
	assert.True(t, strings.HasPrefix(indexes.record.Reason, types.SourcemapIndexReasonIndexTooLarge+":"), indexes.record.Reason)
	assert.Empty(t, store.indexes)
}

func TestParseBudgetsAllArrayEntriesBeforeDecoding(t *testing.T) {
	for _, field := range []struct {
		name     string
		value    string
		perEntry int
	}{
		{"sources", `"a.js"`, 33},
		{"names", `"name"`, 16},
		{"sourcesContent", `"code"`, 24},
		{"ignoreList", `0`, 8},
		{"x_google_ignoreList", `0`, 8},
	} {
		t.Run(field.name, func(t *testing.T) {
			const entries = 1024
			values := strings.TrimSuffix(strings.Repeat(field.value+",", entries), ",")
			data := []byte(fmt.Sprintf(`{"version":3,"mappings":"A","%s":[%s]}`, field.name, values))
			limit := len(data) + (entries-1)*field.perEntry
			_, err := parseMap(context.Background(), data, limit)
			assert.ErrorIs(t, err, errMapBudget)

			// Exactly enough room for the array and one segment is accepted.
			_, err = parseMap(context.Background(), data, len(data)+entries*field.perEntry+segmentSize)
			require.NoError(t, err)
		})
	}
}

func TestParseArrayCountsRespectEscapedStringsAndNullContent(t *testing.T) {
	data := []byte(`{"version":3,"sources":["a,\"[file].js","b.js"],"names":["x,y"],"sourcesContent":[null,"code,[text]"],"ignoreList":[1],"mappings":"AAAAA"}`)
	m, err := parseMap(context.Background(), data, maxIndexCacheBytes)
	require.NoError(t, err)
	assert.Equal(t, []string{"a,\"[file].js", "b.js"}, m.Sources)
	assert.Equal(t, []string{"", "code,[text]"}, m.SourcesContent)
	assert.Equal(t, []bool{false, true}, m.Ignored)

	_, err = parseMap(context.Background(), []byte(`{"version":3,"sources":[["nested"]],"mappings":"A"}`), maxIndexCacheBytes)
	assert.ErrorIs(t, err, ErrInvalidMap, "counting entries must not admit invalid field types")
	_, err = parseMap(context.Background(), []byte("{\"version\":3,\"sources\":[\"\xff\"],\"mappings\":\"A\"}"), maxIndexCacheBytes)
	assert.ErrorIs(t, err, ErrInvalidMap, "invalid UTF-8 must not expand past the input charge")
}

func TestDenseMappingsDoNotAllocatePerSegmentOrGrowTheTable(t *testing.T) {
	const segments = 1 << 20
	data := []byte(`{"version":3,"sources":[],"names":[],"mappings":"` + strings.Repeat("A,", segments) + `"}`)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	m, err := parseMap(context.Background(), data, maxIndexCacheBytes)
	runtime.ReadMemStats(&after)
	require.NoError(t, err)
	require.Len(t, m.Segments, segments)
	allocated := after.TotalAlloc - before.TotalAlloc
	// One segment table plus bounded JSON/string overhead; the old decoder
	// allocated about 124 MB for this 2 MiB input through repeated growth
	// and a fresh VLQ slice per segment.
	assert.Less(t, allocated, uint64(2*(segmentSize*segments+len(data))))
	t.Logf("raw=%d decoded_segments=%d allocated=%d", len(data), len(m.Segments), allocated)
	runtime.KeepAlive(m)
}

func TestCancelledMapParsingIsNotAPermanentInvalidMap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := parseMap(ctx, []byte(cartMap), maxIndexCacheBytes)
	assert.ErrorIs(t, err, context.Canceled)
	assert.NotErrorIs(t, err, ErrInvalidMap)

	store := newFakeStore()
	store.maps[testHash] = []byte(cartMap)
	service, _ := newTestService(store)
	_, err = service.buildIndex(ctx, "app-1", testHash, false)
	assert.ErrorIs(t, err, context.Canceled)
	var permanent *river.JobCancelError
	assert.NotErrorAs(t, err, &permanent)
	assert.Empty(t, store.indexes)
}

func TestIndexSizeChecksWithoutConstructingTables(t *testing.T) {
	m := labMap(labScreen, 6)
	// Cross a fence boundary with source text and both string tables present.
	m.Segments = append(m.Segments[:1], make([]Segment, fenceStride)...)
	for i := range m.Segments {
		m.Segments[i] = Segment{Column: uint32(i), Source: 0, Name: NoIndex}
	}
	var encoded bytes.Buffer
	require.NoError(t, WriteIndex(&encoded, m))
	assert.Equal(t, encoded.Len(), IndexSize(m))
	assert.Zero(t, testing.AllocsPerRun(10, func() { IndexSize(m) }))
}
