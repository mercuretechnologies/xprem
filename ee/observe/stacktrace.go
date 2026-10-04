// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"strings"
	"unicode/utf8"
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
	// maxStacktraceBytesPerRecord bounds encoded stack trace attributes,
	// including their field names and JSON syntax, in addition to the ordinary
	// attribute budget.
	maxStacktraceBytesPerRecord = 64 << 10
)

// stacktrace is a value read as a stack trace: the text kept, and its frames.
type stacktrace struct {
	text   string
	frames []symbolication.Frame
}

// readStacktrace reads value as a stack trace, with the frames past the ones
// kept replaced by a single "... skipping N frames" line.
func readStacktrace(value string, minFrames int) (stacktrace, bool) {
	if minFrames > 1 && !strings.Contains(value, "\n") {
		return stacktrace{}, false
	}
	if len(value) > maxStacktraceScanBytes {
		// Cut on a line break so no frame is read half.
		lastBreak := strings.LastIndexByte(value[:maxStacktraceScanBytes], '\n')
		if lastBreak < 0 {
			return stacktrace{}, false
		}
		value = value[:lastBreak]
	}
	lines := symbolication.ReadLines(value)
	var frameLines []int
	for i, line := range lines {
		if line.Frame != nil {
			frameLines = append(frameLines, i)
		}
	}
	if len(frameLines) < minFrames {
		return stacktrace{}, false
	}
	if len(frameLines) > recentFramesKept+oldestFramesKept {
		lines = skipMiddleFrames(lines, frameLines)
	}
	// Kept line by line, so the frames are exactly the ones in the text; the
	// frames come first in the budget and the message lines take what is left.
	frameRunes := 0
	for _, line := range lines {
		if line.Frame != nil || line.Skipped > 0 {
			frameRunes += utf8.RuneCountInString(line.Text) + 1
		}
	}
	messageRunes := maxStacktraceRunes - frameRunes
	var read stacktrace
	var texts []string
	kept := 0
	for _, line := range lines {
		text := line.Text
		if line.Frame != nil || line.Skipped > 0 {
			if kept += utf8.RuneCountInString(text) + 1; kept > maxStacktraceRunes {
				continue
			}
			if line.Frame != nil {
				read.frames = append(read.frames, *line.Frame)
			}
		} else {
			text = truncateRunes(text, maxAttributeValueRunes)
			if messageRunes -= utf8.RuneCountInString(text) + 1; messageRunes < 0 {
				continue
			}
		}
		texts = append(texts, text)
	}
	read.text = strings.Join(texts, "\n")
	return read, true
}

// skipMiddleFrames keeps the lines up to the last recent frame kept and from
// the first oldest frame kept, and counts every frame in between, including
// the ones an earlier "skipping" line already stood for.
func skipMiddleFrames(lines []symbolication.Line, frameLines []int) []symbolication.Line {
	lastRecent := frameLines[recentFramesKept-1]
	firstOldest := frameLines[len(frameLines)-oldestFramesKept]
	skipped := len(frameLines) - recentFramesKept - oldestFramesKept
	for _, line := range lines[lastRecent+1 : firstOldest] {
		skipped += line.Skipped
	}
	skipping := symbolication.Line{Text: symbolication.SkippedFramesLine(skipped), Skipped: skipped}
	kept := append(lines[:lastRecent+1:lastRecent+1], skipping)
	return append(kept, lines[firstOldest:]...)
}

// boundBody caps a log body, giving a stack trace the room of one.
func boundBody(body string) string {
	if trace, isTrace := readStacktrace(body, 2); isTrace {
		return trace.text
	}
	return truncateRunes(body, maxBodyRunes)
}
