package services

import (
	"context"
	"strings"
	update2 "xprem/internal/update"
)

// SurfBlockedHeader is the dictionary member holding the refusals, which the
// device echoes back as its own request header on every poll. That replay is what
// makes a refusal outlive the client's own crash report, which expires after a
// couple of healthy updates.
//
// ServerDefinedHeadersHeader carries the whole dictionary, not just this member:
// it is written once per response, from one place.
const (
	SurfBlockedHeader          = "xprem-surf-blocked"
	ServerDefinedHeadersHeader = "expo-server-defined-headers"
)

// surfBlockToken names the exact update that crashed, not just its branch: a fix
// published to the same branch gets a new update id and is therefore served.
func surfBlockToken(branchName string, updateId string) string {
	return branchName + "@" + updateId
}

// surfBlockSet holds the surfBlockToken of every update this device must not be
// served on the branch it asked for.
type surfBlockSet map[string]struct{}

func (b surfBlockSet) contains(branchName string, updateId string) bool {
	if len(b) == 0 {
		return false
	}
	_, blocked := b[surfBlockToken(branchName, updateId)]
	return blocked
}

// collectSurfBlocks gathers what this device must not be surfed onto, from its
// two sources. The verdicts it echoes back are already tokens; the crashes it
// reports are UUIDs, which only the store can resolve to a branch and an update.
// An id that resolves to nothing tells us nothing, so it is skipped rather than
// failing the poll.
//
// Only called for a device whose surf was honoured AND that reported something,
// so these lookups stay off the steady-state path.
func (s *ExpoProtocolService) collectSurfBlocks(ctx context.Context, appId string, surfBlockTokens string, failedUpdateIDsRaw string) surfBlockSet {
	blocks := surfBlockSet{}
	for _, token := range parseSurfBlockTokens(surfBlockTokens) {
		blocks[token] = struct{}{}
	}
	for _, failedID := range update2.ParseFailedUpdateIDs(failedUpdateIDsRaw) {
		failedUpdate, err := s.updateRepo.GetUpdateByUUID(ctx, appId, failedID)
		if err != nil || failedUpdate == nil {
			continue
		}
		blocks[surfBlockToken(failedUpdate.Branch, failedUpdate.UpdateId)] = struct{}{}
	}
	return blocks
}

// maxSurfBlockTokens bounds how many verdicts a device carries. The client
// replays them on every poll, so the list cannot be allowed to grow for ever;
// the oldest fall off, and their branches become surfable again.
const maxSurfBlockTokens = 5

// maxSurfBlockTokensRaw bounds the echoed header before it is split. Five tokens
// of a max-length branch name fit well under it; Go accepts request headers up to
// 1 MiB, and this value is entirely client-supplied.
const maxSurfBlockTokensRaw = 2048

// parseSurfBlockTokens reads the verdicts a device echoes back, bounded on the
// input AND on the count. Nothing downstream limits either.
func parseSurfBlockTokens(raw string) []string {
	if len(raw) > maxSurfBlockTokensRaw {
		raw = raw[:maxSurfBlockTokensRaw]
	}
	tokens := make([]string, 0, maxSurfBlockTokens)
	for _, token := range strings.Split(raw, ",") {
		if token = strings.TrimSpace(token); token == "" {
			continue
		}
		if !isPrintableASCII(token) {
			continue
		}
		tokens = append(tokens, token)
		if len(tokens) == maxSurfBlockTokens {
			break
		}
	}
	return tokens
}

// isPrintableASCII reports whether every byte is in 0x20-0x7E, the only bytes
// an RFC 8941 string can carry; a retained token outside that range would be
// echoed back and break the whole dictionary on the device.
func isPrintableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// SetSurfBlocked contributes the verdict to the dictionary the response carries,
// keeping the ones the device already holds: expo-updates replaces its whole
// stored copy, so dropping the previous tokens would unblock the branches they
// name. It writes into the caller's dictionary rather than returning a header, so
// other members can share it.
func SetSurfBlocked(dictionary *HeaderDictionary, carriedTokens string, token string) {
	tokens := make([]string, 0, maxSurfBlockTokens)
	seen := map[string]struct{}{token: {}}
	tokens = append(tokens, token)
	for _, carriedToken := range parseSurfBlockTokens(carriedTokens) {
		if len(tokens) == maxSurfBlockTokens {
			break
		}
		if _, duplicate := seen[carriedToken]; duplicate {
			continue
		}
		seen[carriedToken] = struct{}{}
		tokens = append(tokens, carriedToken)
	}
	dictionary.Set(SurfBlockedHeader, strings.Join(tokens, ","))
}

// BlockedSurf is what a refused poll reports back so the response can carry the
// verdict and tell the app which branch it lost.
type BlockedSurf struct {
	BranchName string
	Token      string
}
