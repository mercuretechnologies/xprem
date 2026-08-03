package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"xprem/internal/services"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const paramsTestAppID = "d3a60200-4574-4781-9734-c174b511fb55"

func validManifestRequest(target string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, target, nil)
	r.Header.Set("expo-app-id", paramsTestAppID)
	r.Header.Set("expo-channel-name", "qa")
	r.Header.Set("expo-protocol-version", "1")
	r.Header.Set("expo-platform", "ios")
	r.Header.Set("expo-runtime-version", "3.0.0")
	return r
}

// Every header the manifest path consumes, asserted in one place. A field
// dropped from the params struct is invisible to the service tests, which build
// their params by hand.
func TestManifestParamsReadsEveryHeader(t *testing.T) {
	r := validManifestRequest("/manifest")
	r.Header.Set("xprem-branch", "pr-482")
	r.Header.Set("xprem-surf-blocked", "pr-482@200")
	r.Header.Set("EAS-Client-ID", "device-1")
	r.Header.Set("expo-current-update-id", "11111111-1111-4111-8111-111111111111")
	r.Header.Set("expo-fatal-error", "boom")
	r.Header.Set("Expo-Recent-Failed-Update-Ids", `"22222222-2222-4222-8222-222222222222"`)

	params, err := manifestParams(r, "req-1")

	require.NoError(t, err)
	assert.Equal(t, "req-1", params.RequestID)
	assert.Equal(t, paramsTestAppID, params.AppID)
	assert.Equal(t, "qa", params.ChannelName)
	assert.Equal(t, "ios", params.Platform)
	assert.Equal(t, "3.0.0", params.RuntimeVersion)
	assert.Equal(t, int64(1), params.ProtocolVersion)
	assert.Equal(t, "pr-482", params.XpremBranch)
	assert.Equal(t, "pr-482@200", params.SurfBlockTokens)
	assert.Equal(t, "device-1", params.ClientID)
	assert.Equal(t, "11111111-1111-4111-8111-111111111111", params.CurrentUpdateID)
	assert.Equal(t, "boom", params.ExpoFatalError)
	assert.Equal(t, `"22222222-2222-4222-8222-222222222222"`, params.RecentFailedUpdateIDs)
}

// The two surf headers are the ones with no other test able to see them: the
// service tests set the fields directly, so only this one covers the wire.
func TestManifestParamsReadsTheSurfHeaders(t *testing.T) {
	cases := map[string]struct {
		header string
		read   func(p services.ManifestRequestParams) string
	}{
		"xprem-branch":       {"xprem-branch", func(p services.ManifestRequestParams) string { return p.XpremBranch }},
		"xprem-surf-blocked": {"xprem-surf-blocked", func(p services.ManifestRequestParams) string { return p.SurfBlockTokens }},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := validManifestRequest("/manifest")
			r.Header.Set(tc.header, "sentinel")

			params, err := manifestParams(r, "req-1")

			require.NoError(t, err)
			assert.Equal(t, "sentinel", tc.read(params))
		})
	}
}

// Absent optional headers are empty, never a stale value from another field.
func TestManifestParamsLeavesAbsentHeadersEmpty(t *testing.T) {
	params, err := manifestParams(validManifestRequest("/manifest"), "req-1")

	require.NoError(t, err)
	assert.Empty(t, params.XpremBranch)
	assert.Empty(t, params.SurfBlockTokens)
	assert.Empty(t, params.ClientID)
	assert.Empty(t, params.CurrentUpdateID)
	assert.Empty(t, params.ExpoFatalError)
	assert.Empty(t, params.RecentFailedUpdateIDs)
}

// Platform and runtime version fall back to the query string for clients that
// cannot set the headers.
func TestManifestParamsFallsBackToTheQueryString(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/manifest?platform=android&runtimeVersion=2.0.0", nil)
	r.Header.Set("expo-app-id", paramsTestAppID)
	r.Header.Set("expo-channel-name", "qa")
	r.Header.Set("expo-protocol-version", "1")

	params, err := manifestParams(r, "req-1")

	require.NoError(t, err)
	assert.Equal(t, "android", params.Platform)
	assert.Equal(t, "2.0.0", params.RuntimeVersion)
}

func TestManifestParamsRejects(t *testing.T) {
	cases := map[string]func(*http.Request){
		"no channel":          func(r *http.Request) { r.Header.Del("expo-channel-name") },
		"no protocol version": func(r *http.Request) { r.Header.Del("expo-protocol-version") },
		"bad protocol version": func(r *http.Request) {
			r.Header.Set("expo-protocol-version", "not-a-number")
		},
		"unknown platform":   func(r *http.Request) { r.Header.Set("expo-platform", "blackberry") },
		"no platform":        func(r *http.Request) { r.Header.Del("expo-platform") },
		"no runtime version": func(r *http.Request) { r.Header.Del("expo-runtime-version") },
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			r := validManifestRequest("/manifest")
			breakIt(r)

			_, err := manifestParams(r, "req-1")

			assert.Error(t, err)
		})
	}
}

// The device-facing half of the refusal: without this header the device keeps
// asking for the branch that crashed on it, on every poll, forever.
func TestServerDefinedHeadersCarriesTheVerdict(t *testing.T) {
	params := services.ManifestRequestParams{SurfBlockTokens: "pr-1@100"}
	result := services.ManifestResult{BlockedSurf: &services.BlockedSurf{BranchName: "pr-2", Token: "pr-2@200"}}

	w := httptest.NewRecorder()
	writeServerDefinedHeaders(w, serverDefinedHeaders(params, result))

	// The verdict the device already held survives the new one.
	assert.Equal(t, `xprem-surf-blocked="pr-2@200,pr-1@100"`, w.Header().Get("expo-server-defined-headers"))
}

// No refusal means no header at all: an empty dictionary would replace whatever
// the device already carries.
func TestServerDefinedHeadersIsSilentWithoutARefusal(t *testing.T) {
	params := services.ManifestRequestParams{SurfBlockTokens: "pr-1@100"}

	w := httptest.NewRecorder()
	writeServerDefinedHeaders(w, serverDefinedHeaders(params, services.ManifestResult{}))

	assert.Empty(t, w.Header().Get("expo-server-defined-headers"))
}
