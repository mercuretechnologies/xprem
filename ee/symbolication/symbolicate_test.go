// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package symbolication

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const labScreen = `import { useState } from 'react'

export function LabScreen() {
  const [user] = useState(undefined)
  const onPress = () => {
    console.log(user.profile.name)
  }
  return null
}
`

// labMap is a Hermes-like map of one bundle line: onPress at offset 120 maps
// to LabScreen.tsx line 6, useState at offset 500 to a dependency.
func labMap(screen string, onPressLine int) *Map {
	return &Map{
		Sources:        []string{"src/LabScreen.tsx", "node_modules/react/index.js"},
		SourcesContent: []string{screen, "export function useState() {}\n"},
		Names:          []string{"onPress", "useState"},
		Ignored:        []bool{false, true},
		Segments: []Segment{
			{Column: 100, Source: 0, OriginalLine: uint32(onPressLine - 1), OriginalColumn: 4, Name: 0},
			{Column: 400, Source: 1, OriginalLine: 0, OriginalColumn: 16, Name: 1},
		},
	}
}

func hermesTrace() string {
	return strings.Join([]string{
		"TypeError: Cannot read property 'name' of undefined",
		"    at onPress (address at " + deviceBundle + ":1:120)",
		"    at onPress (address at " + deviceBundle + ":1:120)",
		"    at useState (address at " + deviceBundle + ":1:500)",
		"    ... skipping 40 frames",
		"    at forEach (native)",
		"    at unknown (address at " + deviceBundle + ":1:5)",
	}, "\n")
}

func TestSymbolicateMapsFramesToTheirSource(t *testing.T) {
	index := openIndexOf(t, labMap(labScreen, 6))
	trace := Symbolicate(index, hermesTrace())

	require.Len(t, trace.Frames, 5, "the message line is not a frame")

	onPress := trace.Frames[0]
	assert.Equal(t, 2, onPress.Repeat, "a recursion is one frame")
	assert.True(t, onPress.Bytecode)
	require.NotNil(t, onPress.Origin)
	assert.Equal(t, "src/LabScreen.tsx", onPress.Origin.Source)
	assert.Equal(t, 6, onPress.Origin.Line)
	assert.True(t, onPress.Origin.InApp)
	require.NotNil(t, onPress.Origin.Context)
	assert.Equal(t, 1, onPress.Origin.Context.FirstLine, "five lines before, cut at the top of the file")
	assert.Equal(t, []string{
		"import { useState } from 'react'",
		"",
		"export function LabScreen() {",
		"  const [user] = useState(undefined)",
		"  const onPress = () => {",
		"    console.log(user.profile.name)",
		"  }",
		"  return null",
		"}",
	}, onPress.Origin.Context.Lines)

	useState := trace.Frames[1]
	require.NotNil(t, useState.Origin)
	assert.False(t, useState.Origin.InApp, "a dependency is not the app's code")
	assert.Nil(t, useState.Origin.Context, "no code for a dependency")

	assert.Equal(t, 40, trace.Frames[2].Skipped)
	assert.True(t, trace.Frames[3].Native)
	assert.Nil(t, trace.Frames[3].Origin)
	assert.Nil(t, trace.Frames[4].Origin, "an offset before the first segment maps nowhere")
}

func TestSymbolicateLeavesHermesInternalFramesAlone(t *testing.T) {
	index := openIndexOf(t, labMap(labScreen, 6))
	trace := Symbolicate(index, strings.Join([]string{
		"Error: boom",
		"    at anonymous (address at InternalBytecode.js:1:120)",
		"    at onPress (address at " + deviceBundle + ":1:120)",
	}, "\n"))

	require.Len(t, trace.Frames, 2)
	assert.True(t, trace.Frames[0].Native)
	assert.Nil(t, trace.Frames[0].Origin, "the engine's own bytecode is not in the app's map")
	require.NotNil(t, trace.Frames[1].Origin)
	assert.Equal(t, "LabScreen.tsx in onPress", Culprit(trace))
}

func TestSymbolicateWithoutAnIndexKeepsTheFrames(t *testing.T) {
	trace := Symbolicate(nil, hermesTrace())
	require.Len(t, trace.Frames, 5)
	assert.Equal(t, "onPress", trace.Frames[0].Function)
	assert.Nil(t, trace.Frames[0].Origin)
}

func TestGroupFingerprintSurvivesAnUpdate(t *testing.T) {
	errorType, message := "TypeError", "Cannot read property 'name' of undefined"
	inUpdateA := GroupFingerprint(errorType, message, Symbolicate(openIndexOf(t, labMap(labScreen, 6)), hermesTrace()))

	// The same code, two lines lower: same error.
	moved := "// a comment\n// another\n" + labScreen
	inUpdateB := GroupFingerprint(errorType, message, Symbolicate(openIndexOf(t, labMap(moved, 8)), hermesTrace()))
	assert.Equal(t, inUpdateA, inUpdateB)

	// The line that throws changed: another error.
	edited := strings.Replace(labScreen, "user.profile.name", "user.profile.fullName", 1)
	inUpdateC := GroupFingerprint(errorType, message, Symbolicate(openIndexOf(t, labMap(edited, 6)), hermesTrace()))
	assert.NotEqual(t, inUpdateA, inUpdateC)

	// The message carries no weight once frames map to the app's code.
	otherMessage := GroupFingerprint(errorType, "Cannot read property 'email' of undefined", Symbolicate(openIndexOf(t, labMap(labScreen, 6)), hermesTrace()))
	assert.Equal(t, inUpdateA, otherMessage)
}

func TestGroupFingerprintFallsBackOnTheMessage(t *testing.T) {
	withValues := GroupFingerprint("Error", "User 42 not found", Symbolicate(nil, "Error: User 42 not found"))
	otherValues := GroupFingerprint("Error", "User 7 not found", Symbolicate(nil, "Error: User 7 not found"))
	assert.Equal(t, withValues, otherValues)
	assert.NotEqual(t, withValues, GroupFingerprint("Error", "Cart 42 is empty", Symbolicate(nil, "")))
}

func TestCulpritIsTheFirstInAppFrame(t *testing.T) {
	trace := Symbolicate(openIndexOf(t, labMap(labScreen, 6)), hermesTrace())
	assert.Equal(t, "LabScreen.tsx in onPress", Culprit(trace))
	assert.Equal(t, "", Culprit(Symbolicate(nil, hermesTrace())))
}

func TestTrimLineKeepsTheCodeAroundTheColumn(t *testing.T) {
	short := "const onPress = () => {}"
	assert.Equal(t, short, trimLine(short, 10))

	long := strings.Repeat("a", 100) + "throw new Error('here')" + strings.Repeat("b", 200)
	aroundThrow := trimLine(long, 105)
	assert.Contains(t, aroundThrow, "throw new Error('here')")
	assert.True(t, strings.HasPrefix(aroundThrow, "…"), "cut at the start")
	assert.True(t, strings.HasSuffix(aroundThrow, "…"), "cut at the end")
	assert.LessOrEqual(t, len([]rune(aroundThrow)), trimmedLineRunes+2)

	atTheStart := trimLine(long, 3)
	assert.True(t, strings.HasPrefix(atTheStart, "aaaa"), "nothing cut before a column near the start")
	atTheEnd := trimLine(long, 320)
	assert.True(t, strings.HasSuffix(atTheEnd, "bbbb"), "nothing cut after a column near the end")
	assert.Equal(t, trimmedLineRunes+1, len([]rune(atTheEnd)))
}
