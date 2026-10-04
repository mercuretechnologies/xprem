// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func hermesTrace(frames int) string {
	lines := []string{"Error: boom"}
	for i := 0; i < frames; i++ {
		lines = append(lines, fmt.Sprintf("    at f%d (address at /data/.expo-internal/cc6bcf26.bundle:1:%d)", i, 1000+i))
	}
	return strings.Join(lines, "\n")
}

func TestReadStacktraceCutsTheMiddle(t *testing.T) {
	trace, ok := readStacktrace(hermesTrace(300), 2)
	require.True(t, ok)
	lines := strings.Split(trace.text, "\n")
	require.Len(t, lines, 1+recentFramesKept+1+oldestFramesKept)
	assert.Equal(t, "Error: boom", lines[0])
	assert.Contains(t, lines[recentFramesKept], "at f49 ")
	assert.Equal(t, "    ... skipping 200 frames", lines[recentFramesKept+1])
	assert.Contains(t, lines[recentFramesKept+2], "at f250 ")
	assert.Contains(t, lines[len(lines)-1], "at f299 ")
	require.Len(t, trace.frames, recentFramesKept+oldestFramesKept)
	assert.Equal(t, "f49", trace.frames[recentFramesKept-1].Function)
	assert.Equal(t, "f250", trace.frames[recentFramesKept].Function)
}

func TestReadStacktraceCountsFramesHermesAlreadySkipped(t *testing.T) {
	lines := strings.Split(hermesTrace(120), "\n")
	// Line 61 is frame f60, inside the middle that is cut.
	lines = append(lines[:61:61], append([]string{"    ... skipping 1000 frames"}, lines[61:]...)...)
	trace, ok := readStacktrace(strings.Join(lines, "\n"), 2)
	require.True(t, ok)
	assert.Contains(t, trace.text, "    ... skipping 1020 frames")
}

func TestReadStacktraceKeepsAShortTrace(t *testing.T) {
	short := hermesTrace(20)
	trace, ok := readStacktrace(short, 2)
	require.True(t, ok)
	assert.Equal(t, short, trace.text)
	assert.Len(t, trace.frames, 20)
}

func TestReadStacktraceCutsTheMessageAndNotTheFrames(t *testing.T) {
	trace, ok := readStacktrace(strings.Repeat("m", 40_000)+"\n"+hermesTrace(3), 2)
	require.True(t, ok)
	assert.Len(t, trace.frames, 3)
	lines := strings.Split(trace.text, "\n")
	require.Len(t, lines, 5)
	assert.Len(t, lines[0], maxAttributeValueRunes)
	assert.Contains(t, lines[4], "at f2 ")

	message := strings.Repeat(strings.Repeat("m", 1000)+"\n", 40)
	trace, ok = readStacktrace(message+hermesTrace(3), 2)
	require.True(t, ok)
	assert.Len(t, trace.frames, 3, "a message of many lines leaves the frames their room")
	assert.Contains(t, trace.text, "at f2 ")
	assert.LessOrEqual(t, len([]rune(trace.text)), maxStacktraceRunes)

	long := "Error: boom"
	for i := 0; i < 100; i++ {
		long += fmt.Sprintf("\n    at f%d (address at /%s/cc6bcf26.bundle:1:%d)", i, strings.Repeat("d", 700), 1000+i)
	}
	trace, ok = readStacktrace(long, 2)
	require.True(t, ok)
	assert.LessOrEqual(t, len([]rune(trace.text)), maxStacktraceRunes, "frames past the budget are dropped, the first ones kept")
	assert.Contains(t, trace.text, "at f0 ")
	assert.Len(t, trace.frames, strings.Count(trace.text, "    at "))
}

func TestReadStacktraceIgnoresText(t *testing.T) {
	_, ok := readStacktrace("line one\nline two\n    at only one frame (a.js:1:2)", 2)
	assert.False(t, ok)
	_, ok = readStacktrace("line one\nline two\n    at only one frame (a.js:1:2)", 1)
	assert.True(t, ok)
	_, ok = readStacktrace("onPress@main.jsbundle:1:120", 1)
	assert.True(t, ok)
	_, ok = readStacktrace(strings.Repeat("x", maxStacktraceScanBytes+10), 2)
	assert.False(t, ok)
}

func TestMarshalAttributesKeepsAStacktracePastTheValueLimit(t *testing.T) {
	trace := hermesTrace(30)
	require.Greater(t, len(trace), maxAttributeValueRunes)
	note := strings.Repeat("n", 2000)

	out, traces := marshalAttributes(map[string]any{
		"exception.stacktrace": trace,
		"note":                 note,
	}, nil)
	var kept map[string]string
	require.NoError(t, json.Unmarshal([]byte(out), &kept))
	assert.Equal(t, trace, kept["exception.stacktrace"])
	assert.Len(t, kept["note"], maxAttributeValueRunes)
	assert.Len(t, traces["exception.stacktrace"].frames, 30)
}

func TestBoundBodyGivesAStacktraceItsRoom(t *testing.T) {
	trace := hermesTrace(70)
	require.Greater(t, len(trace), maxBodyRunes)
	assert.Equal(t, trace, boundBody(trace))
	assert.Len(t, boundBody(strings.Repeat("b", 5000)), maxBodyRunes)
}

func TestHandleLogsRejectsAnOversizedStacktraceAttributeName(t *testing.T) {
	key := strings.Repeat("x", 8<<20)
	encodedKey, err := json.Marshal(key)
	require.NoError(t, err)
	trace := hermesTrace(30)
	encodedTrace, err := json.Marshal(trace)
	require.NoError(t, err)
	body := []byte(fmt.Sprintf(`{"resourceLogs":[{
		"resource":{"attributes":[{"key":"expo.eas_client.id","value":{"stringValue":"4127c568-af7f-4d2b-9e0a-1c6e2b7d9f31"}}]},
		"scopeLogs":[{"logRecords":[{"attributes":[
			{"key":%s,"value":{"stringValue":%s}},
			{"key":"exception.stacktrace","value":{"stringValue":%s}}
		]}]}]
	}]}`, encodedKey, encodedTrace, encodedTrace))
	require.Less(t, len(body), maxBatchBodyBytes)
	sink := &capturingSink{}
	response := serveIngest(NewIngestHandler(nil, sink, nil, nil), http.MethodPost, logsPath, body)
	require.Equal(t, http.StatusNoContent, response.Code)
	require.Len(t, sink.logs, 1)
	require.LessOrEqual(t, len(sink.logs[0].Attributes), maxAttributesBytes+maxStacktraceBytesPerRecord)
	var kept map[string]string
	require.NoError(t, json.Unmarshal([]byte(sink.logs[0].Attributes), &kept))
	assert.NotContains(t, kept, key)
	assert.Equal(t, trace, kept[exceptionStacktraceKey])
}

func TestMarshalAttributesChargesJSONEscapingToEachBudget(t *testing.T) {
	attrs := map[string]any{
		exceptionStacktraceKey: hermesTrace(30),
		manualStacktraceKey:    hermesTrace(30),
	}
	// HTML escaping makes each of these field names six times larger in JSON.
	for i := 0; i < 4; i++ {
		attrs[strings.Repeat("<", 7000)+fmt.Sprint(i)] = hermesTrace(30)
	}
	for i := 0; i < 30; i++ {
		attrs[fmt.Sprintf("note%02d", i)] = strings.Repeat("<\x00\"\\", 256)
	}
	out, traces := marshalAttributes(attrs, nil)
	require.LessOrEqual(t, len(out), maxAttributesBytes+maxStacktraceBytesPerRecord)
	var kept map[string]string
	require.NoError(t, json.Unmarshal([]byte(out), &kept))
	assert.Equal(t, attrs[exceptionStacktraceKey], kept[exceptionStacktraceKey])
	assert.Equal(t, attrs[manualStacktraceKey], kept[manualStacktraceKey])
	assert.Len(t, traces[exceptionStacktraceKey].frames, 30)
	assert.Len(t, traces[manualStacktraceKey].frames, 30)
	assert.Less(t, len(kept), len(attrs), "escaped names and values spend their full encoded size")

	ordinary := map[string]any{}
	for i := 0; i < 30; i++ {
		ordinary[fmt.Sprintf("note%02d", i)] = strings.Repeat("<\x00\"\\", 256)
	}
	out, traces = marshalAttributes(ordinary, nil)
	assert.Empty(t, traces)
	assert.LessOrEqual(t, len(out), maxAttributesBytes)
}

func TestMarshalAttributesCountsStacktracesAndKeepsExceptionFirst(t *testing.T) {
	attrs := map[string]any{
		exceptionStacktraceKey: hermesTrace(30),
		manualStacktraceKey:    hermesTrace(30),
		exceptionTypeKey:       "TypeError",
		exceptionMessageKey:    "Checkout failed",
	}
	for i := 0; i < 300; i++ {
		attrs[fmt.Sprintf("a%03d", i)] = hermesTrace(2)
	}
	out, traces := marshalAttributes(attrs, nil)
	var kept map[string]string
	require.NoError(t, json.Unmarshal([]byte(out), &kept))
	assert.Len(t, kept, maxAttributesPerRecord)
	assert.Equal(t, attrs[exceptionStacktraceKey], kept[exceptionStacktraceKey])
	assert.Equal(t, attrs[manualStacktraceKey], kept[manualStacktraceKey])
	assert.Equal(t, "TypeError", kept[exceptionTypeKey])
	assert.Equal(t, "Checkout failed", kept[exceptionMessageKey])
	assert.Len(t, traces[exceptionStacktraceKey].frames, 30)
	assert.Len(t, traces[manualStacktraceKey].frames, 30)
	assert.NotContains(t, kept, "a299")
	for key := range traces {
		assert.Contains(t, kept, key, "fingerprinting must only use retained traces")
	}
}
