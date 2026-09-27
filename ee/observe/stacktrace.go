// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"strings"
	"xprem/ee/symbolication"
)

// A stack trace keeps its most recent and its oldest frames; the oldest show
// what started a runaway recursion.
const (
	recentFramesKept = 50
	oldestFramesKept = 50
)

const (
	// maxStacktraceScanBytes bounds how much of a value is read for frames.
	maxStacktraceScanBytes = 256 << 10
	maxStacktraceRunes     = 32 << 10
	// maxStacktraceBytesPerRecord bounds the stack traces of one record together.
	maxStacktraceBytesPerRecord = 64 << 10
)

// trimStacktrace reports whether value is a stack trace, that is holds at
// least two frames, and returns it with the frames past the ones kept replaced
// by a single "... skipping N frames" line. It scans at most 256 KiB, ending
// at a line break, and caps recognized traces at 32 Ki runes. The returned
// value may already be shortened even when fewer than two frames are found.
func trimStacktrace(value string) (string, bool) {
	if !strings.Contains(value, "\n") {
		return value, false
	}
	if len(value) > maxStacktraceScanBytes {
		// Cut on a line break so no frame is read half.
		lastBreak := strings.LastIndexByte(value[:maxStacktraceScanBytes], '\n')
		if lastBreak < 0 {
			return value, false
		}
		value = value[:lastBreak]
	}
	lines := strings.Split(value, "\n")
	var frameLines []int
	for i, line := range lines {
		if _, isFrame := symbolication.ParseFrame(line); isFrame {
			frameLines = append(frameLines, i)
		}
	}
	if len(frameLines) < 2 {
		return value, false
	}
	if len(frameLines) > recentFramesKept+oldestFramesKept {
		lines = skipMiddleFrames(lines, frameLines)
	}
	return truncateRunes(strings.Join(lines, "\n"), maxStacktraceRunes), true
}

// skipMiddleFrames keeps the lines up to the last recent frame kept and from
// the first oldest frame kept, and counts every frame in between, including
// the ones an earlier "skipping" line already stood for.
func skipMiddleFrames(lines []string, frameLines []int) []string {
	lastRecent := frameLines[recentFramesKept-1]
	firstOldest := frameLines[len(frameLines)-oldestFramesKept]
	skipped := len(frameLines) - recentFramesKept - oldestFramesKept
	for _, line := range lines[lastRecent+1 : firstOldest] {
		if count, isSkipped := symbolication.SkippedFrames(line); isSkipped {
			skipped += count
		}
	}
	kept := append(lines[:lastRecent+1:lastRecent+1], symbolication.SkippedFramesLine(skipped))
	return append(kept, lines[firstOldest:]...)
}

// boundBody caps a log body, giving a stack trace the room of one.
func boundBody(body string) string {
	if trace, isTrace := trimStacktrace(body); isTrace {
		return trace
	}
	return truncateRunes(body, maxBodyRunes)
}
