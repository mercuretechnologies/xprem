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

// refusedUpdates is the set of updates a device must not be served on the branch
// it asked for: the ones it reported as failed to launch, plus the verdict it is
// already carrying.
type refusedUpdates map[string]struct{}

func (r refusedUpdates) contains(branchName string, updateId string) bool {
	if len(r) == 0 {
		return false
	}
	_, refused := r[surfBlockToken(branchName, updateId)]
	return refused
}

// collectRefusedUpdates resolves the device's failed update UUIDs to the updates
// they name. Only called for a device that is asking for a branch AND reporting
// something, so the lookups stay off the steady-state path.
func (s *ExpoProtocolService) collectRefusedUpdates(ctx context.Context, appId string, surfBlockTokens string, failedUpdateIDsRaw string) refusedUpdates {
	refused := refusedUpdates{}
	for _, token := range parseSurfBlockTokens(surfBlockTokens) {
		refused[token] = struct{}{}
	}
	for _, failedID := range update2.ParseFailedUpdateIDs(failedUpdateIDsRaw) {
		failedUpdate, err := s.updateRepo.GetUpdateByUUID(ctx, appId, failedID)
		if err != nil || failedUpdate == nil {
			continue
		}
		refused[surfBlockToken(failedUpdate.Branch, failedUpdate.UpdateId)] = struct{}{}
	}
	return refused
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
