package update

import (
	"strings"

	"github.com/google/uuid"
)

// maxFailedUpdateIDs matches the cap expo-updates applies on its side
// (SELECT ... WHERE failed_launch_count > 0 ORDER BY commit_time DESC LIMIT 5).
const maxFailedUpdateIDs = 5

// maxFailedUpdateIDsRaw bounds the header itself, not just the entries kept.
const maxFailedUpdateIDsRaw = 512

// ParseFailedUpdateIDs reads the Expo-Recent-Failed-Update-IDs header, a
// quoted-UUID list (RFC 8941 style). Invalid entries are dropped and the
// result is deduplicated and capped.
func ParseFailedUpdateIDs(raw string) []string {
	if raw == "" {
		return nil
	}
	// Bound the input before splitting it: the cap below stops at five VALID
	// entries, so a header of nothing but invalid ones is walked in full, and Go
	// accepts request headers up to 1 MiB. Five quoted UUIDs fit in ~200 bytes.
	if len(raw) > maxFailedUpdateIDsRaw {
		raw = raw[:maxFailedUpdateIDsRaw]
	}
	ids := make([]string, 0, maxFailedUpdateIDs)
	seen := make(map[string]struct{}, maxFailedUpdateIDs)
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		part = strings.Trim(part, `"`)
		parsed, err := uuid.Parse(part)
		if err != nil {
			continue
		}
		id := parsed.String()
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
		if len(ids) == maxFailedUpdateIDs {
			break
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return ids
}
