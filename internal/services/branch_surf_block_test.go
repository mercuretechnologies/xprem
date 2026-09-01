package services

import (
	"context"
	"reflect"
	"strings"
	"testing"
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
	h.channelRepo.mappings["qa"] = &types.ChannelResolution{Id: "1", BranchName: "staging"}
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
	result, err := h.protocolService.ResolveUpdateForDevice(context.Background(), params)

	require.NoError(t, err)
	assert.Equal(t, "staging", result.BranchName)
	require.NotNil(t, result.Update, "the fallback must actually be served, not just named")
	assert.Equal(t, "100", result.Update.UpdateId)
	require.NotNil(t, result.BlockedSurf)
	assert.Equal(t, "pr-482", result.BlockedSurf.BranchName)
	assert.Equal(t, "cHItNDgyADIwMA", result.BlockedSurf.Token)
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
	params.SurfBlockTokens = "cHItNDgyADIwMA,c3RhZ2luZwAxMDA"
	result, err := h.protocolService.ResolveUpdateForDevice(context.Background(), params)

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
	params.SurfBlockTokens = "cHItNDgyADIwMA"
	result, err := h.protocolService.ResolveUpdateForDevice(context.Background(), params)

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
	params.SurfBlockTokens = "cHItNDgyADIwMA"
	result, err := h.protocolService.ResolveUpdateForDevice(context.Background(), params)

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
	result, err := h.protocolService.ResolveUpdateForDevice(context.Background(), params)

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
	assert.Equal(t, `xprem-surf-blocked="cHItNDgyADIwMA"`, surfBlockedOnly("", "cHItNDgyADIwMA"))
}

// The response replaces the client's whole stored dictionary, so a second verdict
// has to carry the first or it unblocks the branch it names.
func TestSetSurfBlockedKeepsTheVerdictsAlreadyCarried(t *testing.T) {
	assert.Equal(t,
		`xprem-surf-blocked="cHItMgAyMDA,cHItMQAxMDA"`,
		surfBlockedOnly("cHItMQAxMDA", "cHItMgAyMDA"))
}

func TestSetSurfBlockedDropsDuplicatesAndCaps(t *testing.T) {
	assert.Equal(t,
		`xprem-surf-blocked="cHItMQAxMDA"`,
		surfBlockedOnly("cHItMQAxMDA", "cHItMQAxMDA"))

	carried := "YQAx,YgAy,YwAz,ZAA0,ZQA1,ZgA2"
	assert.Equal(t,
		`xprem-surf-blocked="bmV3ADk,YQAx,YgAy,YwAz,ZAA0"`,
		surfBlockedOnly(carried, "bmV3ADk"))
}

// The characters a branch name may legally contain are exactly the ones that
// would wreck the header carrying it: a comma splits one verdict into two, a
// quote ends the string early, and a non-ASCII byte cannot be in an RFC 8941
// string at all. The token is encoded, so none of them ever reach the wire.
func TestHostileBranchNamesProduceSafeTokens(t *testing.T) {
	for _, branchName := range []string{"pr,ios", `pr-"x`, "pr-é", " pr-1 ", "pr\x00x"} {
		token := surfBlockToken(branchName, "200")
		assert.True(t, isBase64URL(token), "token for %q must be safe to put on the wire", branchName)
		assert.Equal(t,
			[]string{token},
			parseSurfBlockTokens(token),
			"a token the server minted must survive being echoed back")
	}

	// The distinctness that matters: two (branch, update) pairs that concatenate
	// to the same bytes must still yield different tokens, or one branch's crash
	// would block another branch's update. This pair collides under the realistic
	// regression — dropping the separator — where "pr"+"1@2" and "pr@1"+"2" do
	// not, which is why the earlier version of this assertion proved nothing.
	assert.NotEqual(t, surfBlockToken("pr", "12"), surfBlockToken("pr1", "2"))
}

// The verdict is one member among others: a dictionary already carrying something
// must keep it.
func TestSetSurfBlockedSharesTheDictionary(t *testing.T) {
	dictionary := NewHeaderDictionary()
	dictionary.Set("xprem-other", "value")

	SetSurfBlocked(dictionary, "", "cHItNDgyADIwMA")

	assert.Equal(t, `xprem-other="value", xprem-surf-blocked="cHItNDgyADIwMA"`, dictionary.Encode())
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
		params.SurfBlockTokens = "cHItNDgyADIwMA"
		result, err := h.protocolService.ResolveUpdateForDevice(context.Background(), params)

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
		result, err := h.protocolService.ResolveUpdateForDevice(context.Background(), params)

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
		result, err := h.protocolService.ResolveUpdateForDevice(context.Background(), params)

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
	params.SurfBlockTokens = "YQAx,YgAy,YwAz"
	_, err := h.protocolService.ResolveUpdateForDevice(context.Background(), params)

	require.NoError(t, err)
	assert.Equal(t, before, h.updateRepo.uuidLookups(), "no update lookup may run for a declined surf")
}

// The header is entirely client-supplied and Go accepts up to 1 MiB of it, so
// both the input and the count are bounded before anything is retained.
func TestParseSurfBlockTokensIsBounded(t *testing.T) {
	// The filler is a VALID token: filler the filter would drop makes this pass
	// without the cap ever being reached.
	t.Run("caps the number of tokens", func(t *testing.T) {
		raw := strings.Repeat(surfBlockToken("pr-1", "1")+",", 40000)
		assert.Len(t, parseSurfBlockTokens(raw), maxSurfBlockTokens)
	})

	// The count cap alone never trips on separators, so only the input cap can
	// stop this: a valid token placed past the boundary must not be reached.
	t.Run("caps the input before splitting it", func(t *testing.T) {
		raw := strings.Repeat(",", maxSurfBlockTokensRaw) + surfBlockToken("pr-late", "1")
		assert.Empty(t, parseSurfBlockTokens(raw))
	})

	t.Run("keeps a legitimate value whole", func(t *testing.T) {
		first, second := surfBlockToken("pr-1", "100"), surfBlockToken("pr-2", "200")
		assert.Equal(t,
			[]string{first, second},
			parseSurfBlockTokens(" "+first+" , "+second+" "))
	})

	// Retained tokens are echoed back inside an RFC 8941 string, which can only
	// carry printable ASCII; one stray byte would cost the device the whole
	// dictionary. This also covers the input cap cutting a multi-byte rune.
	t.Run("drops anything the server did not mint", func(t *testing.T) {
		valid := surfBlockToken("pr-1", "100")
		assert.Equal(t,
			[]string{valid},
			parseSurfBlockTokens(valid+",pr-482@200,pr-é,\"quoted\""))
	})
}

// Whether a channel allows surfing must never ride the manifest again. A manifest
// is frozen when its update is served; the setting changes whenever an admin says
// so, so a device already up to date would never learn it was turned on. The
// client asks /branch_lists.
func TestTheManifestNeverCarriesTheChannelSetting(t *testing.T) {
	// Over the field tags, not over an encoding of the zero value: the shape
	// anyone would actually add is a pointer with omitempty, and that emits
	// nothing when unset — so marshalling an empty struct and grepping the bytes
	// passes no matter what the struct declares.
	extra := reflect.TypeOf(types.ExtraManifestData{})
	for i := 0; i < extra.NumField(); i++ {
		field := extra.Field(i)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		assert.NotEqual(t, "branchSurfing", name,
			"a channel-lifetime setting has no place in an update-lifetime document: %s", field.Name)
		assert.NotContains(t, strings.ToLower(field.Name), "surfingenabled",
			"same, under another name: %s", field.Name)
	}
}

// Long branch names make five tokens overflow the member bound. Cutting the
// joined value would keep a half token that can never match, and would take the
// newest verdict with it — the one the response exists to deliver.
func TestVerdictsAreDroppedWholeWhenTheyDoNotFit(t *testing.T) {
	longBranch := strings.Repeat("b", 128)
	fresh := surfBlockToken(longBranch+"-new", "900")
	carried := make([]string, 0, maxSurfBlockTokens)
	for i := 0; i < maxSurfBlockTokens; i++ {
		carried = append(carried, surfBlockToken(longBranch+string(rune('a'+i)), "100"))
	}

	dictionary := NewHeaderDictionary()
	SetSurfBlocked(dictionary, strings.Join(carried, ","), fresh)
	encoded := dictionary.Encode()

	require.NotEmpty(t, encoded, "the newest verdict must survive; losing it re-serves the crash")
	value := strings.TrimSuffix(strings.TrimPrefix(encoded, SurfBlockedHeader+`="`), `"`)
	assert.LessOrEqual(t, len(value), maxHeaderDictionaryValue)
	kept := strings.Split(value, ",")
	assert.Equal(t, fresh, kept[0], "the fresh verdict is kept first")
	for _, token := range kept {
		assert.True(t, isBase64URL(token), "no token may be left half-written")
		assert.Contains(t, append(carried, fresh), token, "a kept token must be whole")
	}
}
