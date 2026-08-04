package services

import (
	"context"
	"encoding/base64"
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
//
// Opaque on purpose. The token travels in a comma-separated RFC 8941 string, and
// a branch name is neither comma-free nor ASCII-only — "pr,ios" is a legal branch
// that would be read back as two tokens, and a branch with an accent cannot be
// carried by a structured string at all. Either would silently drop a verdict and
// let a crashing update be served again. Encoding the pair sidesteps both: the
// result is base64url, so it can only ever be one token. Nothing decodes it — the
// token is compared, never read.
func surfBlockToken(branchName string, updateId string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(branchName + "\x00" + updateId))
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
// reports are manifest UUIDs, a different identifier from the update id, which
// only the store can resolve. An id that resolves to nothing tells us nothing, so
// it is skipped rather than failing the poll.
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

// maxSurfBlockTokensRaw bounds the echoed header before it is split. Five ids fit
// well under it; Go accepts request headers up to 1 MiB, and this value is
// entirely client-supplied.
const maxSurfBlockTokensRaw = 2048

// parseSurfBlockTokens reads the verdicts a device echoes back, bounded on the
// input AND on the count. Nothing downstream limits either. Anything that is not
// base64url is dropped: the value is client-supplied, only a token the server
// itself minted can ever match, and retaining other bytes would be echoed back
// into a structured string that cannot carry them.
func parseSurfBlockTokens(raw string) []string {
	if len(raw) > maxSurfBlockTokensRaw {
		raw = raw[:maxSurfBlockTokensRaw]
	}
	tokens := make([]string, 0, maxSurfBlockTokens)
	for _, token := range strings.Split(raw, ",") {
		if token = strings.TrimSpace(token); token == "" {
			continue
		}
		if !isBase64URL(token) {
			continue
		}
		tokens = append(tokens, token)
		if len(tokens) == maxSurfBlockTokens {
			break
		}
	}
	return tokens
}

// SetSurfBlocked contributes the verdict to the dictionary the response carries,
// keeping the ones the device already holds: expo-updates replaces its whole
// stored copy, so dropping the previous tokens would unblock the branches they
// name. It writes into the caller's dictionary rather than returning a header, so
// other members can share it.
//
// Tokens are taken one at a time while the joined value still fits the member
// bound, oldest dropped first. Assembling the whole list and cutting it to length
// would leave a half token that can never match anything, and would take the
// newest verdict — the one this response is about — with it.
func SetSurfBlocked(dictionary *HeaderDictionary, carriedTokens string, token string) {
	if len(token) > maxHeaderDictionaryValue {
		return
	}
	tokens := make([]string, 0, maxSurfBlockTokens)
	seen := map[string]struct{}{token: {}}
	tokens = append(tokens, token)
	size := len(token)
	for _, carriedToken := range parseSurfBlockTokens(carriedTokens) {
		if len(tokens) == maxSurfBlockTokens {
			break
		}
		if _, duplicate := seen[carriedToken]; duplicate {
			continue
		}
		if size+1+len(carriedToken) > maxHeaderDictionaryValue {
			// Skipped, not stopped: this header is client-supplied, so one
			// oversized entry would otherwise discard every legitimate verdict
			// behind it — and those are what keep a crashing update refused.
			continue
		}
		seen[carriedToken] = struct{}{}
		tokens = append(tokens, carriedToken)
		size += 1 + len(carriedToken)
	}
	dictionary.Set(SurfBlockedHeader, strings.Join(tokens, ","))
}

// isBase64URL reports whether every byte is in the base64url alphabet, which is
// what surfBlockToken produces and a subset of what an RFC 8941 string can carry.
func isBase64URL(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			continue
		}
		return false
	}
	return true
}

// BlockedSurf is what a refused poll reports back so the response can carry the
// verdict and tell the app which branch it lost. Token names the refused update;
// BranchName is only for the message shown to the tester.
type BlockedSurf struct {
	BranchName string
	Token      string
}
