// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package symbolication

import (
	"regexp"
	"strconv"
)

// FrameKind says what a frame's position points into.
type FrameKind uint8

const (
	// SourceFrame points at a line and column of a JavaScript file.
	SourceFrame FrameKind = iota
	// BytecodeFrame points at a Hermes bytecode offset.
	BytecodeFrame
	// NativeFrame is native code, which no source map covers.
	NativeFrame
)

// Frame is one line of a stack trace that names a code position.
type Frame struct {
	Kind     FrameKind
	Function string
	File     string
	// Line and Column are one-based; for a BytecodeFrame, Column is the
	// zero-based bytecode offset.
	Line   int
	Column int
}

// maxFrameLineBytes bounds the lines worth matching: no real frame is longer.
const maxFrameLineBytes = 4096

type frameFormat struct {
	pattern *regexp.Regexp
	frame   func(match []string) Frame
}

// frameFormats are tried in order, and the first match wins.
var frameFormats = []frameFormat{
	// Hermes and V8: "at fn (address at file:1:2)", "at fn (file:1:2)", "at fn (native)".
	{
		regexp.MustCompile(`^\s*at (.+?) \((?:(native)|(address at )?(.+):(\d+):(\d+))\)$`),
		func(m []string) Frame {
			switch {
			case m[2] != "":
				return Frame{Kind: NativeFrame, Function: m[1]}
			case m[3] != "":
				return Frame{Kind: BytecodeFrame, Function: m[1], File: m[4], Line: atoi(m[5]), Column: atoi(m[6])}
			}
			return Frame{Kind: SourceFrame, Function: m[1], File: m[4], Line: atoi(m[5]), Column: atoi(m[6])}
		},
	},
	// V8 without a function name: "at file:1:2".
	{
		regexp.MustCompile(`^\s*at (\S.*):(\d+):(\d+)$`),
		func(m []string) Frame {
			return Frame{Kind: SourceFrame, File: m[1], Line: atoi(m[2]), Column: atoi(m[3])}
		},
	},
	// JavaScriptCore and Firefox: "fn@file:1:2", "@file:1:2".
	{
		regexp.MustCompile(`^\s*([^@\s]*)@(.+):(\d+):(\d+)$`),
		func(m []string) Frame {
			return Frame{Kind: SourceFrame, Function: m[1], File: m[2], Line: atoi(m[3]), Column: atoi(m[4])}
		},
	},
	// JavaScriptCore native code: "fn@[native code]", "[native code]".
	{
		regexp.MustCompile(`^\s*(?:([^@\s]*)@)?\[native code\]$`),
		func(m []string) Frame { return Frame{Kind: NativeFrame, Function: m[1]} },
	},
	// Java, as Android renders it: "com.app.Main.run(Main.java:12)", "com.app.Main.run(Native Method)".
	{
		regexp.MustCompile(`^\s*(?:at )?([\w$]+(?:\.[\w$<>]+)+)\(([^():]*)(?::(\d+))?\)$`),
		func(m []string) Frame {
			return Frame{Kind: NativeFrame, Function: m[1], File: m[2], Line: atoi(m[3])}
		},
	},
	// iOS: "symbol (Binary + 1234)", "Binary + 1234".
	{
		regexp.MustCompile(`^(?:(.+) \()?([^\s()]+) \+ \d+(?:\))?$`),
		func(m []string) Frame { return Frame{Kind: NativeFrame, Function: m[1], File: m[2]} },
	},
	// iOS, an address alone: "0x1a2b".
	{
		regexp.MustCompile(`^0x[0-9a-fA-F]+$`),
		func([]string) Frame { return Frame{Kind: NativeFrame} },
	},
}

// ParseFrame reads one line of a stack trace. It returns false for an
// unrecognized line or one longer than 4096 bytes.
func ParseFrame(line string) (Frame, bool) {
	if len(line) > maxFrameLineBytes {
		return Frame{}, false
	}
	for _, format := range frameFormats {
		if m := format.pattern.FindStringSubmatch(line); m != nil {
			return format.frame(m), true
		}
	}
	return Frame{}, false
}

// Lines that stand for frames a stack trace left out: Hermes writes the
// first, the expo-observe SDK the second.
var skippedFramesPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^\s*\.\.\. skipping (\d+) frames$`),
	regexp.MustCompile(`^\s*… \+(\d+) more frames$`),
}

// SkippedFrames reads a Hermes or expo-observe skipped-frame marker. It
// returns false for an unrecognized line or one longer than 4096 bytes.
func SkippedFrames(line string) (int, bool) {
	if len(line) > maxFrameLineBytes {
		return 0, false
	}
	for _, pattern := range skippedFramesPatterns {
		if m := pattern.FindStringSubmatch(line); m != nil {
			return atoi(m[1]), true
		}
	}
	return 0, false
}

// SkippedFramesLine is the line Hermes writes for frames it left out.
func SkippedFramesLine(count int) string {
	return "    ... skipping " + strconv.Itoa(count) + " frames"
}

// atoi reads matched digits, discarding conversion errors. Empty input
// yields 0; a positive value beyond the int range yields the maximum int.
func atoi(digits string) int {
	n, _ := strconv.Atoi(digits)
	return n
}
