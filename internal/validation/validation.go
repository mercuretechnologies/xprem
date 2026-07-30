// Package validation holds the input-validation rules for user-supplied
// dashboard values (resource names, display labels). Services call these before
// persisting so bad input fails fast with a caller-facing 400 instead of a deep
// store/bucket error, or, worse, a malformed row/key that only breaks later.
package validation

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// maxNameLen caps resource names used as storage-path segments. Kept equal to
// the bucket layer's maxSegmentLen so a name accepted here can never be
// rejected later when an update is written to the bucket.
const maxNameLen = 128

// maxDisplayNameLen caps human-facing labels (app name, API key name). These
// are never path segments, so the limit is only about sane storage/UI bounds.
const maxDisplayNameLen = 255

// maxPatternLen caps branch patterns, matching the VARCHAR(255) of
// api_key_branch_rules.pattern. Wider than maxNameLen so a rule can name a
// legacy branch that predates the maxNameLen cap.
const maxPatternLen = 255

// Error is a validation failure on user-supplied input. Handlers detect it with
// errors.As and map it to HTTP 400, surfacing Message to the caller, while
// unrecognized errors stay a 500.
type Error struct {
	Field   string
	Message string
}

func (e *Error) Error() string {
	if e.Field == "" {
		return e.Message
	}
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

// IsValidationError reports whether err is (or wraps) a validation Error.
func IsValidationError(err error) bool {
	var ve *Error
	return errors.As(err, &ve)
}

func fail(field, format string, args ...any) *Error {
	return &Error{Field: field, Message: fmt.Sprintf(format, args...)}
}

// Errorf builds a validation Error, e.g. to wrap a lower-level validator's
// message (config.ValidateKeys) so it still maps to a 400.
func Errorf(field, format string, args ...any) *Error {
	return fail(field, format, args...)
}

// Name validates a resource name used as a single storage-path segment and DB
// value (branch, channel, release channel).
//
// The rules are a mirror of internal/bucket.validateSegment, keep the two in
// sync. Rejects empties, path separators, "." / "..", null bytes, control
// characters, and anything over maxNameLen.
//
// It also rejects "*", the wildcard of the branch patterns API key access
// rules are written in (NamePattern below), so no branch can be named
// something a rule already means.
//
// The rejection is not limited to creation: several callers validate a name
// they are only reading, so a deployment already holding a branch named with
// "*" answers 400 on the dashboard and CLI routes that name it. Devices are
// unaffected, the update protocol goes through bucket.validateSegment, which
// still accepts it.
func Name(field, value string) error {
	if strings.Contains(value, "*") {
		return fail(field, "must not contain %q, which is reserved as the wildcard of API key access rules", "*")
	}
	return namePattern(field, value, maxNameLen)
}

// NamePattern validates a branch pattern used in an API key's access rules: a
// branch name, or a name with "*" standing for any run of characters, empty
// included ("pr-*", "*-staging", "*"). Every other rule of Name applies except
// the length cap (maxPatternLen), so a pattern without a wildcard is exactly a
// branch name and matches only that branch.
func NamePattern(field, value string) error {
	return namePattern(field, value, maxPatternLen)
}

// namePattern is Name without the wildcard rule and with a caller-chosen
// length cap, which is all the two differ by.
func namePattern(field, value string, maxLen int) error {
	if value == "" {
		return fail(field, "must not be empty")
	}
	if len(value) > maxLen {
		return fail(field, "exceeds max length %d", maxLen)
	}
	if strings.ContainsAny(value, "/\\") {
		return fail(field, "must not contain path separators")
	}
	if value == "." || value == ".." {
		return fail(field, "%q is reserved", value)
	}
	for _, r := range value {
		if r == 0x00 {
			return fail(field, "must not contain null bytes")
		}
		if unicode.IsControl(r) {
			return fail(field, "must not contain control characters")
		}
	}
	return nil
}

// DisplayName validates a human-facing label (app name, API key name). Looser
// than Name, it is never a path segment, so spaces and unicode are allowed -
// but still non-empty (ignoring surrounding whitespace), bounded, and free of
// control characters (log / UI injection).
func DisplayName(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return fail(field, "must not be empty")
	}
	if len(value) > maxDisplayNameLen {
		return fail(field, "exceeds max length %d", maxDisplayNameLen)
	}
	for _, r := range value {
		if r == 0x00 {
			return fail(field, "must not contain null bytes")
		}
		if unicode.IsControl(r) && r != '\t' {
			return fail(field, "must not contain control characters")
		}
	}
	return nil
}

// RolloutPercentage parses a rollout percentage passed as a string (the
// rolloutPercentage query param on publish). The advertised rollout range is
// 1-99; 100 is also accepted and means a plain full-fleet publish, which
// callers treat as "no rollout".
func RolloutPercentage(field, value string) (int, error) {
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 || n > 100 {
		return 0, fail(field, "must be an integer between 1 and 99")
	}
	return n, nil
}

// NumericID validates a positive integer id passed as a string (branch id, API
// key id). Guards the string→int64 conversion the stores do so a malformed id
// fails with a 400 instead of a 500.
func NumericID(field, value string) error {
	if value == "" {
		return fail(field, "must not be empty")
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fail(field, "must be a numeric id")
	}
	if n <= 0 {
		return fail(field, "must be a positive id")
	}
	return nil
}
