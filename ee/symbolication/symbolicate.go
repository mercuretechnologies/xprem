// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package symbolication

import (
	"path"
	"strings"

	"github.com/google/uuid"
)

// Trace is a stack trace read frame by frame, with where each frame comes
// from when the map knows.
type Trace struct {
	Frames []TraceFrame `json:"frames"`
}

// TraceFrame is one entry of a trace. Repeat folds a recursion into one entry,
// and an entry with Skipped stands for frames the trace left out.
type TraceFrame struct {
	Function string `json:"function,omitempty"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Column   int    `json:"column,omitempty"`
	Native   bool   `json:"native,omitempty"`
	// Bytecode marks a Hermes frame, whose Column is a bytecode offset.
	Bytecode bool    `json:"bytecode,omitempty"`
	Repeat   int     `json:"repeat,omitempty"`
	Skipped  int     `json:"skipped,omitempty"`
	Origin   *Origin `json:"origin,omitempty"`
}

// Origin is the source position a frame maps to.
type Origin struct {
	Source string `json:"source"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
	Name   string `json:"name,omitempty"`
	// InApp is false for a source the map lists as ignored, a dependency.
	InApp   bool     `json:"inApp"`
	Context *Context `json:"context,omitempty"`
}

// Context is the source around an in-app frame: the line itself, with
// contextLines before and after it.
type Context struct {
	FirstLine int      `json:"firstLine"`
	Lines     []string `json:"lines"`
}

// contextLines is how many lines show before and after the frame's, as
// Sentry does. A line longer than maxContextLineRunes is cut to
// trimmedLineRunes around the frame's column, with an ellipsis at each cut.
const (
	contextLines        = 5
	maxContextLineRunes = 150
	trimmedLineRunes    = 140
)

// Symbolicate reads a stack trace and maps every frame it can through the
// index. A nil index leaves the frames as they are. Lookup failures leave
// a frame without an origin; source text failures omit its context.
func Symbolicate(index *Index, stacktrace string) Trace {
	trace := ReadTrace(stacktrace)
	if index == nil {
		return trace
	}
	sources := sourceLines{index: index, lines: map[int][]string{}}
	origins := map[Frame]*Origin{}
	for i := range trace.Frames {
		frame := &trace.Frames[i]
		if frame.Native || frame.Skipped > 0 {
			continue
		}
		key := Frame{Kind: SourceFrame, File: frame.File, Line: frame.Line, Column: frame.Column}
		if frame.Bytecode {
			key.Kind = BytecodeFrame
		}
		origin, seen := origins[key]
		if !seen {
			origin = originOf(index, key, &sources)
			origins[key] = origin
		}
		frame.Origin = origin
	}
	return trace
}

// ReadTrace reads the frames of a trace, folding a recursion into one frame.
// Lines that are not frames, the message first, are left out.
func ReadTrace(stacktrace string) Trace {
	var trace Trace
	for _, line := range strings.Split(stacktrace, "\n") {
		if frame, isFrame := ParseFrame(line); isFrame {
			entry := TraceFrame{
				Function: frame.Function, File: frame.File, Line: frame.Line, Column: frame.Column,
				Native: frame.Kind == NativeFrame, Bytecode: frame.Kind == BytecodeFrame, Repeat: 1,
			}
			if last := len(trace.Frames) - 1; last >= 0 && sameFrame(trace.Frames[last], entry) {
				trace.Frames[last].Repeat++
				continue
			}
			trace.Frames = append(trace.Frames, entry)
			continue
		}
		if count, isSkipped := SkippedFrames(line); isSkipped {
			trace.Frames = append(trace.Frames, TraceFrame{Skipped: count})
		}
	}
	return trace
}

// sameFrame compares frame identity for repetition folding, ignoring repeat
// counts and origins. Entries with a nonzero skipped count do not match.
func sameFrame(a, b TraceFrame) bool {
	return a.Skipped == 0 && b.Skipped == 0 && a.Function == b.Function && a.File == b.File &&
		a.Line == b.Line && a.Column == b.Column && a.Native == b.Native && a.Bytecode == b.Bytecode
}

// originOf looks one frame up in the index. A bytecode frame is looked up by
// its offset, a source frame by its line and column made zero-based.
// Missing mappings and lookup errors return nil; context is optional.
func originOf(index *Index, frame Frame, sources *sourceLines) *Origin {
	var pos Position
	var ok bool
	var err error
	switch {
	case frame.Kind == BytecodeFrame:
		pos, ok, err = index.Lookup(0, uint32(frame.Column))
	case frame.Line > 0 && frame.Column > 0:
		pos, ok, err = index.Lookup(uint32(frame.Line-1), uint32(frame.Column-1))
	}
	if err != nil || !ok {
		return nil
	}
	origin := &Origin{Source: pos.Source, Line: pos.Line, Column: pos.Column, Name: pos.Name, InApp: !pos.Ignored}
	if origin.InApp {
		origin.Context = sources.around(pos.SourceIndex, pos.Line, pos.Column)
	}
	return origin
}

// sourceLines reads each source text once per trace.
type sourceLines struct {
	index *Index
	lines map[int][]string
}

// around returns up to five lines before and after a one-based source line,
// trimming long lines around column. source is a zero-based source index.
// Missing text, read errors, and out-of-range lines return nil; reads are cached.
func (s *sourceLines) around(source, line, column int) *Context {
	lines, seen := s.lines[source]
	if !seen {
		text, err := s.index.SourceText(source)
		if err == nil && text != "" {
			// A file ends with a line break; that is not one more line.
			lines = strings.Split(strings.TrimSuffix(text, "\n"), "\n")
		}
		s.lines[source] = lines
	}
	if line < 1 || line > len(lines) {
		return nil
	}
	first := max(line-contextLines, 1)
	last := min(line+contextLines, len(lines))
	context := &Context{FirstLine: first}
	for _, text := range lines[first-1 : last] {
		context.Lines = append(context.Lines, trimLine(text, column))
	}
	return context
}

// trimLine keeps the part of a long line around column: 60 characters before
// it, the rest after, snapped to an edge when close to it.
func trimLine(text string, column int) string {
	runes := []rune(text)
	if len(runes) <= maxContextLineRunes {
		return text
	}
	start := max(min(column, len(runes))-60, 0)
	if start < 5 {
		start = 0
	}
	end := min(start+trimmedLineRunes, len(runes))
	if end > len(runes)-5 {
		end = len(runes)
		start = max(end-trimmedLineRunes, 0)
	}
	trimmed := string(runes[start:end])
	if start > 0 {
		trimmed = "…" + trimmed
	}
	if end < len(runes) {
		trimmed += "…"
	}
	return trimmed
}

// GroupFingerprint names an error the same way in every update, as Sentry
// does: by its type and the in-app frames it went through, each frame by its
// file and its line of code, or its function when the code is unknown. The
// line number is left out, since the same code moves from one update to the
// next. Without in-app frames every mapped frame counts, then unmapped
// non-native frames are used if none are mapped. With none of those, the
// normalized message does.
func GroupFingerprint(errorType, message string, trace Trace) uuid.UUID {
	frames := groupKeys(trace, func(frame TraceFrame) bool { return frame.Origin != nil && frame.Origin.InApp })
	if len(frames) == 0 {
		frames = groupKeys(trace, func(frame TraceFrame) bool { return frame.Origin != nil })
	}
	if len(frames) == 0 {
		frames = groupKeys(trace, func(frame TraceFrame) bool { return !frame.Native && frame.Skipped == 0 })
	}
	if len(frames) == 0 {
		return Fingerprint(errorType, NormalizeMessage(message))
	}
	return Fingerprint(append([]string{errorType}, frames...)...)
}

// groupKeys is what each kept frame contributes to the group fingerprint.
func groupKeys(trace Trace, keep func(TraceFrame) bool) []string {
	var keys []string
	for _, frame := range trace.Frames {
		if !keep(frame) {
			continue
		}
		key := frame.Function + " " + path.Base(frame.File)
		if origin := frame.Origin; origin != nil {
			key = path.Base(origin.Source) + " " + originKey(frame, origin)
		}
		if len(keys) > 0 && keys[len(keys)-1] == key {
			continue
		}
		keys = append(keys, key)
	}
	return keys
}

// maxGroupingLineRunes is the longest line of code that still identifies a
// frame; a longer one is minified or generated.
const maxGroupingLineRunes = 120

// originKey prefers a nonempty source line of at most 120 runes after
// whitespace normalization, then the mapped name, then the frame function.
// When context is present, it must contain origin.Line.
func originKey(frame TraceFrame, origin *Origin) string {
	if origin.Context != nil {
		line := strings.Join(strings.Fields(origin.Context.Lines[origin.Line-origin.Context.FirstLine]), " ")
		if line != "" && len([]rune(line)) <= maxGroupingLineRunes {
			return line
		}
	}
	if origin.Name != "" {
		return origin.Name
	}
	return frame.Function
}

// Culprit names where an error comes from: the first in-app frame, as
// "file in function", or just the file when no function is known. It returns
// an empty string when no in-app frame is mapped.
func Culprit(trace Trace) string {
	for _, frame := range trace.Frames {
		origin := frame.Origin
		if origin == nil || !origin.InApp {
			continue
		}
		function := origin.Name
		if function == "" {
			function = frame.Function
		}
		if function == "" {
			return path.Base(origin.Source)
		}
		return path.Base(origin.Source) + " in " + function
	}
	return ""
}
