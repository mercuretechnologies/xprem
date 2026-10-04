// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"fmt"
	"path"
	"xprem/ee/symbolication"

	"github.com/google/uuid"
)

// The attributes an error record may describe itself with: the SDK's
// OpenTelemetry exception.* keys, and the manual xprem_js_crash event's.
const (
	exceptionTypeKey       = "exception.type"
	exceptionMessageKey    = "exception.message"
	exceptionStacktraceKey = "exception.stacktrace"
	manualTypeKey          = "name"
	manualMessageKey       = "message"
	manualStacktraceKey    = "stack"
)

// severityError is OpenTelemetry's lowest ERROR level; FATAL sits above it.
const severityError = 17

// exception is what an error record says of itself.
type exception struct {
	errorType  string
	message    string
	stacktrace string
	// stacktraceKey is the attribute the stacktrace came from.
	stacktraceKey string
}

// exceptionOf reads the exception under whichever keys the record uses; a
// record with no message at all is named after its event.
func exceptionOf(eventName, body string, attributes map[string]any) exception {
	text := func(keys ...string) string {
		for _, key := range keys {
			if value, _ := attributes[key].(string); value != "" {
				return value
			}
		}
		return ""
	}
	found := exception{
		errorType: text(exceptionTypeKey, manualTypeKey),
		message:   text(exceptionMessageKey, manualMessageKey),
	}
	for _, key := range []string{exceptionStacktraceKey, manualStacktraceKey} {
		if value := text(key); value != "" {
			found.stacktrace, found.stacktraceKey = value, key
			break
		}
	}
	if found.message == "" {
		found.message = body
	}
	if found.message == "" {
		found.message = eventName
	}
	return found
}

// errorFingerprint names an error by its type and the frames it went through,
// or by its message without frames. uuid.Nil means the record is not an error.
func errorFingerprint(row LogRow, attributes map[string]any, traces map[string]stacktrace) uuid.UUID {
	if !row.IsFatal && row.SeverityNumber < severityError {
		return uuid.Nil
	}
	found := exceptionOf(row.EventName, row.Body, attributes)
	if keys := frameKeys(traces[found.stacktraceKey].frames); len(keys) > 0 {
		return symbolication.Fingerprint(append([]string{found.errorType}, keys...)...)
	}
	return symbolication.Fingerprint(found.errorType, symbolication.NormalizeMessage(found.message))
}

// frameKeys lists frames the same way on every device: the file by its name
// only, and a recursion once whatever its depth.
func frameKeys(frames []symbolication.Frame) []string {
	var keys []string
	for _, frame := range frames {
		file := ""
		if frame.File != "" {
			file = path.Base(frame.File)
		}
		key := fmt.Sprintf("%s %s:%d:%d", frame.Function, file, frame.Line, frame.Column)
		// A "skipping N frames" line inside a recursion must not split it in two.
		if len(keys) > 0 && keys[len(keys)-1] == key {
			continue
		}
		keys = append(keys, key)
	}
	return keys
}
