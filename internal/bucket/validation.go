package bucket

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/google/uuid"
)

// maxSegmentLen bounds any single path segment (branch, runtimeVersion,
// updateId, migrationId). Keeps DoS surface small on map keys and
// filesystem paths while staying comfortably above realistic names
// (UUIDs are 36, semver+build metadata under 100).
const maxSegmentLen = 128

// validateSegment ensures a single-segment identifier (branch, runtimeVersion,
// updateId, migrationId) is safe to embed in a storage path / object key.
// Defense-in-depth against path traversal on the local backend and weird
// keys on S3/GCS. Rejects empties, path separators, "." / "..", null bytes,
// control characters, and anything over maxSegmentLen.
func validateSegment(name, value string) error {
	if value == "" {
		return fmt.Errorf("invalid %s: must not be empty", name)
	}
	if len(value) > maxSegmentLen {
		return fmt.Errorf("invalid %s: exceeds max length %d", name, maxSegmentLen)
	}
	if strings.ContainsAny(value, "/\\") {
		return fmt.Errorf("invalid %s: must not contain path separators", name)
	}
	if value == "." || value == ".." {
		return fmt.Errorf("invalid %s: reserved name", name)
	}
	// Null bytes truncate keys in C-based filesystem syscalls; control
	// characters break URL encoding / logging / key listing on S3/GCS.
	for _, r := range value {
		if r == 0x00 {
			return fmt.Errorf("invalid %s: must not contain null bytes", name)
		}
		if unicode.IsControl(r) {
			return fmt.Errorf("invalid %s: must not contain control characters", name)
		}
	}
	return nil
}

// validateRelativePath validates multi-segment paths supplied for fileName /
// assetPath. Nested paths are allowed (e.g. "assets/image.png") but no
// absolute paths and no empty, ".", or ".." segments. Backslashes are rejected outright -
// on Windows filepath.Join treats them as separators, so allowing them would
// let an attacker escape the intended directory via a path like
// "assets\..\..\etc\passwd".
func validateRelativePath(name, value string) error {
	if value == "" {
		return fmt.Errorf("invalid %s: must not be empty", name)
	}
	if strings.ContainsRune(value, '\\') {
		return fmt.Errorf("invalid %s: must not contain '\\' characters", name)
	}
	if strings.HasPrefix(value, "/") {
		return fmt.Errorf("invalid %s: must not be absolute", name)
	}
	for _, seg := range strings.Split(value, "/") {
		switch seg {
		case "":
			return fmt.Errorf("invalid %s: must not contain empty segments", name)
		case ".":
			return fmt.Errorf("invalid %s: must not contain '.' segments", name)
		case "..":
			return fmt.Errorf("invalid %s: must not contain '..' segments", name)
		}
	}
	return nil
}

// validateUUID accepts only the canonical lowercase spelling, so one
// update cannot own two patch keys.
func validateUUID(name, value string) error {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.String() != value {
		return fmt.Errorf("invalid %s: must be a canonical lowercase UUID", name)
	}
	return nil
}
