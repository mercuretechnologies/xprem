package services

import (
	"context"
	"strings"
	"testing"
	"xprem/internal/providers/expo"
	"xprem/internal/types"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const crashedUUID = "9f1c1d2e-0000-4000-8000-000000000001"

func newBlockHarness(t *testing.T) *rolloutTestHarness {
	t.Helper()
	t.Setenv("DB_URL", "postgres://stub")
	h := newRolloutTestHarness(t)
	h.channelRepo.mappings["qa"] = &expo.ChannelMapping{Id: "1", BranchName: "staging"}
	h.channelRepo.surfing = map[string]*types.BranchSurfing{"qa": {Enabled: true, Pattern: "pr-*"}}
	h.seed(seedRow{branch: "staging", rtv: "1", platform: "ios", id: 100, checked: true})
	return h
}

func blockParams(h *rolloutTestHarness) ManifestRequestParams {
	return ManifestRequestParams{
		RequestID:       "test",
		AppID:           h.appId,
		ChannelName:     "qa",
		Platform:        "ios",
		RuntimeVersion:  "1",
		ProtocolVersion: 1,
		ClientID:        "device-1",
		XpremBranch:     "pr-482",
	}
}

// The device reports the update it would be served as having failed to launch,
// so the surf is refused and it lands back on the channel's branch.
func TestSurfIsRefusedWhenTheServedUpdateCrashed(t *testing.T) {
	h := newBlockHarness(t)
	h.seed(seedRow{branch: "pr-482", rtv: "1", platform: "ios", id: 200, checked: true, uuid: crashedUUID})

	params := blockParams(h)
	params.RecentFailedUpdateIDs = `"` + crashedUUID + `"`
	result, err := h.protocolService.ResolveManifestBundle(context.Background(), params)

	require.NoError(t, err)
	assert.Equal(t, "staging", result.BranchName)
	require.NotNil(t, result.Update, "the fallback must actually be served, not just named")
	assert.Equal(t, "100", result.Update.UpdateId)
	require.NotNil(t, result.BlockedSurf)
	assert.Equal(t, "pr-482", result.BlockedSurf.BranchName)
	assert.Equal(t, "pr-482@200", result.BlockedSurf.Token)
}

// A refusal can never empty the candidate list. HonoursSurf requires the asked-for
// branch to DIFFER from the mapped one, and only the asked-for branch can be
// refused, so the mapped branch is structurally unrefusable. Loosen that condition
// and a device ends up served nothing.
func TestTheMappedBranchIsNeverRefused(t *testing.T) {
	h := newBlockHarness(t)
	h.seed(seedRow{branch: "pr-482", rtv: "1", platform: "ios", id: 200, checked: true, uuid: crashedUUID})
	stagingUUID := uuid.NewString()
	h.updateRepo.setUUID(h.appId, "staging", "100", stagingUUID)

	// Both branches reported as crashed, and both named in the carried verdicts.
	params := blockParams(h)
	params.RecentFailedUpdateIDs = `"` + crashedUUID + `","` + stagingUUID + `"`
	params.SurfBlockTokens = "pr-482@200,staging@100"
	result, err := h.protocolService.ResolveManifestBundle(context.Background(), params)

	require.NoError(t, err)
	assert.Equal(t, "staging", result.BranchName)
	require.NotNil(t, result.Update, "a device must never be left with no update at all")
	assert.Equal(t, "100", result.Update.UpdateId)
}

// A fix published to the same branch has a different update id, so the refusal
// does not survive it. Refusing per branch instead of per update would strand
// the device on the embedded bundle for good.
func TestSurfIsAllowedAgainOnceTheBranchIsFixed(t *testing.T) {
	h := newBlockHarness(t)
	h.seed(seedRow{branch: "pr-482", rtv: "1", platform: "ios", id: 200, checked: true, uuid: crashedUUID})
	h.seed(seedRow{branch: "pr-482", rtv: "1", platform: "ios", id: 300, checked: true, uuid: uuid.NewString()})

	params := blockParams(h)
	params.RecentFailedUpdateIDs = `"` + crashedUUID + `"`
	params.SurfBlockTokens = "pr-482@200"
	result, err := h.protocolService.ResolveManifestBundle(context.Background(), params)

	require.NoError(t, err)
	assert.Equal(t, "pr-482", result.BranchName)
	assert.Nil(t, result.BlockedSurf)
}

// The echoed verdict outlives the crash report, which expo-updates drops after a
// couple of healthy updates.
func TestSurfStaysRefusedFromTheEchoedVerdictAlone(t *testing.T) {
	h := newBlockHarness(t)
	h.seed(seedRow{branch: "pr-482", rtv: "1", platform: "ios", id: 200, checked: true, uuid: crashedUUID})

	params := blockParams(h)
	params.SurfBlockTokens = "pr-482@200"
	result, err := h.protocolService.ResolveManifestBundle(context.Background(), params)

	require.NoError(t, err)
	assert.Equal(t, "staging", result.BranchName)
	require.NotNil(t, result.BlockedSurf)
}

// A failed update on another branch says nothing about the one being asked for.
func TestSurfSurvivesAFailureOnAnotherBranch(t *testing.T) {
	h := newBlockHarness(t)
	h.seed(seedRow{branch: "pr-482", rtv: "1", platform: "ios", id: 200, checked: true, uuid: uuid.NewString()})
	h.seed(seedRow{branch: "pr-999", rtv: "1", platform: "ios", id: 400, checked: true, uuid: crashedUUID})

	params := blockParams(h)
	params.RecentFailedUpdateIDs = `"` + crashedUUID + `"`
	result, err := h.protocolService.ResolveManifestBundle(context.Background(), params)

	require.NoError(t, err)
	assert.Equal(t, "pr-482", result.BranchName)
	assert.Nil(t, result.BlockedSurf)
}

// surfBlockedOnly renders a dictionary carrying nothing but the surf verdict,
// which is what a response looks like today.
func surfBlockedOnly(carriedTokens string, token string) string {
	dictionary := NewHeaderDictionary()
	SetSurfBlocked(dictionary, carriedTokens, token)
	return dictionary.Encode()
}

func TestSetSurfBlockedRendersAStructuredDictionary(t *testing.T) {
	assert.Equal(t, `xprem-surf-blocked="pr-482@200"`, surfBlockedOnly("", "pr-482@200"))
}

// The response replaces the client's whole stored dictionary, so a second verdict
// has to carry the first or it unblocks the branch it names.
func TestSetSurfBlockedKeepsTheVerdictsAlreadyCarried(t *testing.T) {
	assert.Equal(t,
		`xprem-surf-blocked="pr-2@200,pr-1@100"`,
		surfBlockedOnly("pr-1@100", "pr-2@200"))
}

func TestSetSurfBlockedDropsDuplicatesAndCaps(t *testing.T) {
	assert.Equal(t,
		`xprem-surf-blocked="pr-1@100"`,
		surfBlockedOnly("pr-1@100", "pr-1@100"))

	carried := "a@1,b@2,c@3,d@4,e@5,f@6"
	assert.Equal(t,
		`xprem-surf-blocked="new@9,a@1,b@2,c@3,d@4"`,
		surfBlockedOnly(carried, "new@9"))
}

// A branch name may contain a quote; unescaped it would break the dictionary the
// client parses, silently losing every verdict.
func TestSetSurfBlockedEscapesQuotes(t *testing.T) {
	assert.Equal(t,
		`xprem-surf-blocked="pr-\"x@200"`,
		surfBlockedOnly("", `pr-"x@200`))
}

// The verdict is one member among others: a dictionary already carrying something
// must keep it.
func TestSetSurfBlockedSharesTheDictionary(t *testing.T) {
	dictionary := NewHeaderDictionary()
	dictionary.Set("xprem-other", "value")

	SetSurfBlocked(dictionary, "", "pr-482@200")

	assert.Equal(t, `xprem-other="value", xprem-surf-blocked="pr-482@200"`, dictionary.Encode())
}

// A poll the surf rule declined is a plain poll. Refusing one would take a device
// off the branch its own channel maps to, on a channel where nothing was enabled.
func TestRefusalNeverAppliesToADeclinedSurf(t *testing.T) {
	t.Run("surfing disabled on the channel", func(t *testing.T) {
		h := newBlockHarness(t)
		h.channelRepo.surfing = map[string]*types.BranchSurfing{"qa": {Enabled: false, Pattern: "*"}}
		h.seed(seedRow{branch: "pr-482", rtv: "1", platform: "ios", id: 200, checked: true, uuid: crashedUUID})

		params := blockParams(h)
		params.RecentFailedUpdateIDs = `"` + crashedUUID + `"`
		params.SurfBlockTokens = "pr-482@200"
		result, err := h.protocolService.ResolveManifestBundle(context.Background(), params)

		require.NoError(t, err)
		assert.Equal(t, "staging", result.BranchName)
		assert.Nil(t, result.BlockedSurf, "a channel with surfing off must not produce a verdict")
	})

	t.Run("branch outside the pattern", func(t *testing.T) {
		h := newBlockHarness(t)
		h.seed(seedRow{branch: "hotfix", rtv: "1", platform: "ios", id: 200, checked: true, uuid: crashedUUID})

		params := blockParams(h)
		params.XpremBranch = "hotfix"
		params.RecentFailedUpdateIDs = `"` + crashedUUID + `"`
		result, err := h.protocolService.ResolveManifestBundle(context.Background(), params)

		require.NoError(t, err)
		assert.Equal(t, "staging", result.BranchName)
		assert.Nil(t, result.BlockedSurf)
	})

	// The mapped branch is a legitimate value: ListSurfableBranches offers it under
	// the default pattern. Asking for it is not a surf, so a crash on it must not
	// cost the device its own channel branch.
	t.Run("asking for the mapped branch", func(t *testing.T) {
		h := newBlockHarness(t)
		stagingUUID := uuid.NewString()
		h.updateRepo.setUUID(h.appId, "staging", "100", stagingUUID)

		params := blockParams(h)
		params.XpremBranch = "staging"
		params.RecentFailedUpdateIDs = `"` + stagingUUID + `"`
		result, err := h.protocolService.ResolveManifestBundle(context.Background(), params)

		require.NoError(t, err)
		assert.Equal(t, "staging", result.BranchName)
		require.NotNil(t, result.Update, "the device must still be served its channel's branch")
		assert.Nil(t, result.BlockedSurf)
	})
}

// A device on a channel with surfing off costs no repository read, whatever it
// puts in the two surf headers.
func TestDeclinedSurfCostsNoLookup(t *testing.T) {
	h := newBlockHarness(t)
	h.channelRepo.surfing = map[string]*types.BranchSurfing{"qa": {Enabled: false, Pattern: "*"}}
	before := h.updateRepo.uuidLookups()

	params := blockParams(h)
	params.RecentFailedUpdateIDs = `"` + uuid.NewString() + `","` + uuid.NewString() + `"`
	params.SurfBlockTokens = "a@1,b@2,c@3"
	_, err := h.protocolService.ResolveManifestBundle(context.Background(), params)

	require.NoError(t, err)
	assert.Equal(t, before, h.updateRepo.uuidLookups(), "no update lookup may run for a declined surf")
}

// The header is entirely client-supplied and Go accepts up to 1 MiB of it, so
// both the input and the count are bounded before anything is retained.
func TestParseSurfBlockTokensIsBounded(t *testing.T) {
	t.Run("caps the number of tokens", func(t *testing.T) {
		raw := strings.Repeat("pr-1@1,", 40000)
		assert.Len(t, parseSurfBlockTokens(raw), maxSurfBlockTokens)
	})

	// The count cap alone never trips on filler, so only the input cap can stop
	// this: a token placed past the boundary must not be reached.
	t.Run("caps the input before splitting it", func(t *testing.T) {
		raw := strings.Repeat(",", maxSurfBlockTokensRaw) + "pr-late@1"
		assert.Empty(t, parseSurfBlockTokens(raw))
	})

	t.Run("keeps a legitimate value whole", func(t *testing.T) {
		assert.Equal(t,
			[]string{"pr-1@100", "pr-2@200"},
			parseSurfBlockTokens(" pr-1@100 , pr-2@200 "))
	})
}
