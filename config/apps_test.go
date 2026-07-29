package config

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubPEMB64 is the base64 encoding of a tiny PEM-shaped byte string used
// throughout the tests to satisfy validatePEMKeyB64. Real key content is
// not required, the validator only checks for the BEGIN marker.
const stubPEMB64 = "LS0tLS1CRUdJTiBURVNUIEtFWS0tLS0tCnRlc3RkYXRhCi0tLS0tRU5EIFRFU1QgS0VZLS0tLS0K"

// resetAppsEnv unsets every env var that LoadAppsFromFlatEnv can read so each test
// starts from a known-empty environment. Central list, when a new env var
// is added to the loader, add it here too or the test suite will start
// interfering across runs.
func resetAppsEnv(t *testing.T) {
	t.Helper()
	vars := []string{
		"EXPO_APP_ID",
		"SKIP_LEGACY_APP_ID_FALLBACK",
		"EXPO_ACCESS_TOKEN",
		"KEYS_STORAGE_TYPE",
		"PUBLIC_LOCAL_EXPO_KEY_PATH",
		"PRIVATE_LOCAL_EXPO_KEY_PATH",
		"AWSSM_EXPO_PUBLIC_KEY_SECRET_ID",
		"AWSSM_EXPO_PRIVATE_KEY_SECRET_ID",
		"PUBLIC_EXPO_KEY_B64",
		"PRIVATE_EXPO_KEY_B64",
	}
	for _, v := range vars {
		os.Unsetenv(v)
	}
	ResetAppsForTest()
	t.Cleanup(func() {
		for _, v := range vars {
			os.Unsetenv(v)
		}
		ResetAppsForTest()
	})
}

// -----------------------------------------------------------------------------
// validateApp / ValidateKeys, unit tests on the validator only. No env.
// -----------------------------------------------------------------------------

func TestValidateApp_AcceptsEachMode(t *testing.T) {
	cases := map[string]AppConfig{
		"local": {
			Id:          "app-1",
			AccessToken: "token",
			Keys: KeysConfig{
				Mode:        KeysModeLocal,
				PublicPath:  "/keys/pub.pem",
				PrivatePath: "/keys/priv.pem",
			},
		},
		"aws-secrets-manager": {
			Id:          "app-1",
			AccessToken: "token",
			Keys: KeysConfig{
				Mode:            KeysModeAWSSM,
				PublicSecretId:  "/eoota/pub",
				PrivateSecretId: "/eoota/priv",
			},
		},
		"environment": {
			Id:          "app-1",
			AccessToken: "token",
			Keys: KeysConfig{
				Mode:       KeysModeEnvironment,
				PublicB64:  stubPEMB64,
				PrivateB64: stubPEMB64,
			},
		},
	}
	for name, app := range cases {
		t.Run(name, func(t *testing.T) {
			assert.NoError(t, validateApp(&app, 0))
		})
	}
}

func TestValidateApp_RejectsBadId(t *testing.T) {
	cases := map[string]string{
		"empty":           "",
		"slash":           "foo/bar",
		"backslash":       "foo\\bar",
		"space":           "foo bar",
		"tab":             "foo\tbar",
		"newline":         "foo\nbar",
		"carriage-return": "foo\rbar",
		"null-byte":       "foo\x00bar",
		"control-char":    "foo\x01bar",
		"dot":             ".",
		"dotdot":          "..",
		"unicode-letter":  "app-é",
		"unicode-cjk":     "app中",
		"unicode-slash":   "app∕bar", // U+2215 division slash, a `/` lookalike
		"fullwidth-slash": "app／bar", // U+FF0F
		"colon":           "app:1",
		"plus":            "app+1",
		"at":              "app@1",
		"emoji":           "app🚀",
	}
	for name, badId := range cases {
		t.Run(name, func(t *testing.T) {
			app := AppConfig{
				Id:          badId,
				AccessToken: "token",
				Keys: KeysConfig{
					Mode:        KeysModeLocal,
					PublicPath:  "/p",
					PrivatePath: "/q",
				},
			}
			err := validateApp(&app, 0)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "apps[0].id")
		})
	}
}

func TestValidateApp_RejectsReservedIds(t *testing.T) {
	// Each of these collides with a top-level static route registered in
	// router.go. Gorilla mux resolves static routes before patterns so an
	// app id matching one of these would silently never receive traffic.
	reserved := []string{"api", "assets", "auth", "dashboard", "hc", "manifest", "metrics"}
	for _, id := range reserved {
		t.Run(id, func(t *testing.T) {
			app := AppConfig{
				Id:          id,
				AccessToken: "token",
				Keys: KeysConfig{
					Mode:        KeysModeLocal,
					PublicPath:  "/p",
					PrivatePath: "/q",
				},
			}
			err := validateApp(&app, 0)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "collides with a top-level route")
		})
	}
}

func TestValidateApp_RejectsTooLongId(t *testing.T) {
	app := AppConfig{
		Id:          strings.Repeat("a", maxAppIdLen+1),
		AccessToken: "token",
		Keys: KeysConfig{
			Mode:        KeysModeLocal,
			PublicPath:  "/p",
			PrivatePath: "/q",
		},
	}
	err := validateApp(&app, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds max length")
}

func TestValidateApp_AcceptsIdAtMaxLength(t *testing.T) {
	app := AppConfig{
		Id:          strings.Repeat("a", maxAppIdLen),
		AccessToken: "token",
		Keys: KeysConfig{
			Mode:        KeysModeLocal,
			PublicPath:  "/p",
			PrivatePath: "/q",
		},
	}
	assert.NoError(t, validateApp(&app, 0))
}

func TestValidateApp_RejectsMissingAccessToken(t *testing.T) {
	app := AppConfig{
		Id:          "app-1",
		AccessToken: "",
		Keys: KeysConfig{
			Mode:        KeysModeLocal,
			PublicPath:  "/p",
			PrivatePath: "/q",
		},
	}
	err := validateApp(&app, 2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "apps[2].accessToken")
}

func TestValidateKeys_RejectsMissingModeFields(t *testing.T) {
	cases := map[string]KeysConfig{
		"local missing public":        {Mode: KeysModeLocal, PrivatePath: "/q"},
		"local missing private":       {Mode: KeysModeLocal, PublicPath: "/p"},
		"aws-sm missing public":       {Mode: KeysModeAWSSM, PrivateSecretId: "/q"},
		"aws-sm missing private":      {Mode: KeysModeAWSSM, PublicSecretId: "/p"},
		"environment missing public":  {Mode: KeysModeEnvironment, PrivateB64: "xx"},
		"environment missing private": {Mode: KeysModeEnvironment, PublicB64: "xx"},
	}
	for name, k := range cases {
		t.Run(name, func(t *testing.T) {
			err := ValidateKeys(&k, "apps[0].keys")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "requires")
		})
	}
}

func TestValidateKeys_RejectsCrossModeFields(t *testing.T) {
	cases := map[string]KeysConfig{
		"local with aws-sm field": {
			Mode: KeysModeLocal, PublicPath: "/p", PrivatePath: "/q",
			PublicSecretId: "leaked",
		},
		"local with b64 field": {
			Mode: KeysModeLocal, PublicPath: "/p", PrivatePath: "/q",
			PublicB64: "leaked",
		},
		"aws-sm with local field": {
			Mode: KeysModeAWSSM, PublicSecretId: "/p", PrivateSecretId: "/q",
			PublicPath: "leaked",
		},
		"aws-sm with b64 field": {
			Mode: KeysModeAWSSM, PublicSecretId: "/p", PrivateSecretId: "/q",
			PrivateB64: "leaked",
		},
		"environment with local field": {
			Mode: KeysModeEnvironment, PublicB64: "xx", PrivateB64: "yy",
			PublicPath: "leaked",
		},
		"environment with aws-sm field": {
			Mode: KeysModeEnvironment, PublicB64: "xx", PrivateB64: "yy",
			PrivateSecretId: "leaked",
		},
	}
	for name, k := range cases {
		t.Run(name, func(t *testing.T) {
			err := ValidateKeys(&k, "apps[0].keys")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "must not set")
		})
	}
}

func TestValidateKeys_EnvironmentMode_RejectsInvalidBase64(t *testing.T) {
	// Base64 that does not decode (padding mismatch) must fail at boot,
	// not at the first manifest sign.
	k := KeysConfig{
		Mode:       KeysModeEnvironment,
		PublicB64:  "not-base64!!",
		PrivateB64: stubPEMB64,
	}
	err := ValidateKeys(&k, "apps[0].keys")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid base64")
	assert.Contains(t, err.Error(), "publicB64")
}

func TestValidateKeys_EnvironmentMode_RejectsNonPEM(t *testing.T) {
	// Valid base64 that doesn't decode to a PEM-shaped payload, a common
	// mistake when the operator base64-encodes the key contents without
	// the BEGIN/END markers.
	rawB64 := "aGVsbG8td29ybGQ=" // -> "hello-world", no BEGIN marker
	k := KeysConfig{
		Mode:       KeysModeEnvironment,
		PublicB64:  stubPEMB64,
		PrivateB64: rawB64,
	}
	err := ValidateKeys(&k, "apps[0].keys")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a PEM key")
	assert.Contains(t, err.Error(), "privateB64")
}

func TestValidateKeys_RejectsMissingOrUnknownMode(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		err := ValidateKeys(&KeysConfig{}, "apps[0].keys")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "required")
	})
	t.Run("unknown", func(t *testing.T) {
		err := ValidateKeys(&KeysConfig{Mode: "env-b64"}, "apps[0].keys")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid")
		// The "env-b64" value is a common migration mistake, the message
		// must spell out the three valid modes so the user knows to swap.
		assert.Contains(t, err.Error(), "environment")
	})
}

// -----------------------------------------------------------------------------
// LoadAppsFromFlatEnv, flat env. The only stateless config source: one app, v1-compat.
// Each key mode, and failure modes.
// -----------------------------------------------------------------------------

func TestLoadAppsFromFlatEnv_LocalMode(t *testing.T) {
	resetAppsEnv(t)
	os.Setenv("EXPO_APP_ID", "solo")
	os.Setenv("EXPO_ACCESS_TOKEN", "tok")
	os.Setenv("KEYS_STORAGE_TYPE", "local")
	os.Setenv("PUBLIC_LOCAL_EXPO_KEY_PATH", "/k/pub.pem")
	os.Setenv("PRIVATE_LOCAL_EXPO_KEY_PATH", "/k/priv.pem")

	require.NoError(t, LoadAppsFromFlatEnv())
	a, err := GetAppConfig("solo")
	require.NoError(t, err)
	assert.Equal(t, KeysModeLocal, a.Keys.Mode)
	assert.Equal(t, "/k/pub.pem", a.Keys.PublicPath)
	assert.Equal(t, "/k/priv.pem", a.Keys.PrivatePath)
	assert.Equal(t, "tok", a.AccessToken)
}

func TestLoadAppsFromFlatEnv_DefaultsToLocalWhenStorageTypeUnset(t *testing.T) {
	// v1 DefaultEnvValues had KEYS_STORAGE_TYPE=local; in v2 that default
	// moved into parseFlatEnvApp itself. Unsetting the var must behave the
	// same as setting it to "local".
	resetAppsEnv(t)
	os.Setenv("EXPO_APP_ID", "solo")
	os.Setenv("EXPO_ACCESS_TOKEN", "tok")
	os.Setenv("PUBLIC_LOCAL_EXPO_KEY_PATH", "/k/pub.pem")
	os.Setenv("PRIVATE_LOCAL_EXPO_KEY_PATH", "/k/priv.pem")

	require.NoError(t, LoadAppsFromFlatEnv())
	a, err := GetAppConfig("solo")
	require.NoError(t, err)
	assert.Equal(t, KeysModeLocal, a.Keys.Mode)
}

func TestLoadAppsFromFlatEnv_AWSSMMode(t *testing.T) {
	resetAppsEnv(t)
	os.Setenv("EXPO_APP_ID", "solo")
	os.Setenv("EXPO_ACCESS_TOKEN", "tok")
	os.Setenv("KEYS_STORAGE_TYPE", "aws-secrets-manager")
	os.Setenv("AWSSM_EXPO_PUBLIC_KEY_SECRET_ID", "/eoota/pub")
	os.Setenv("AWSSM_EXPO_PRIVATE_KEY_SECRET_ID", "/eoota/priv")

	require.NoError(t, LoadAppsFromFlatEnv())
	a, err := GetAppConfig("solo")
	require.NoError(t, err)
	assert.Equal(t, KeysModeAWSSM, a.Keys.Mode)
	assert.Equal(t, "/eoota/pub", a.Keys.PublicSecretId)
	assert.Equal(t, "/eoota/priv", a.Keys.PrivateSecretId)
}

func TestLoadAppsFromFlatEnv_EnvironmentMode(t *testing.T) {
	resetAppsEnv(t)
	os.Setenv("EXPO_APP_ID", "solo")
	os.Setenv("EXPO_ACCESS_TOKEN", "tok")
	os.Setenv("KEYS_STORAGE_TYPE", "environment")
	os.Setenv("PUBLIC_EXPO_KEY_B64", stubPEMB64)
	os.Setenv("PRIVATE_EXPO_KEY_B64", stubPEMB64)

	require.NoError(t, LoadAppsFromFlatEnv())
	a, err := GetAppConfig("solo")
	require.NoError(t, err)
	assert.Equal(t, KeysModeEnvironment, a.Keys.Mode)
	assert.Equal(t, stubPEMB64, a.Keys.PublicB64)
}

func TestLoadAppsFromFlatEnv_RejectsUnknownStorageType(t *testing.T) {
	// An unknown KEYS_STORAGE_TYPE leaves Mode empty in parseFlatEnvApp,
	// then validateApp catches it with the "mode is required" error.
	resetAppsEnv(t)
	os.Setenv("EXPO_APP_ID", "solo")
	os.Setenv("EXPO_ACCESS_TOKEN", "tok")
	os.Setenv("KEYS_STORAGE_TYPE", "vault") // not supported
	err := LoadAppsFromFlatEnv()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mode is required")
}

func TestLoadAppsFromFlatEnv_RejectsMissingToken(t *testing.T) {
	resetAppsEnv(t)
	os.Setenv("EXPO_APP_ID", "solo")
	// No EXPO_ACCESS_TOKEN, no keys set either
	err := LoadAppsFromFlatEnv()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accessToken")
}

// -----------------------------------------------------------------------------
// LoadAppsFromFlatEnv, "nothing set" error path.
// -----------------------------------------------------------------------------

func TestLoadAppsFromFlatEnv_NoSourceSetReturnsActionableError(t *testing.T) {
	resetAppsEnv(t)
	err := LoadAppsFromFlatEnv()
	require.Error(t, err)
	// The error message is part of the UX, it must name the flat-env entry
	// point and point multi-app users at the control plane so a user who
	// forgot to set anything isn't left guessing.
	msg := err.Error()
	assert.Contains(t, msg, "EXPO_APP_ID")
	assert.Contains(t, msg, "control plane")
}

// -----------------------------------------------------------------------------
// GetAppConfig / ListAppIds, lookup API.
// -----------------------------------------------------------------------------

func TestGetAppConfig_UnknownIdReturnsError(t *testing.T) {
	resetAppsEnv(t)
	SetAppsForTest([]AppConfig{
		{Id: "known", AccessToken: "t", Keys: KeysConfig{Mode: KeysModeLocal, PublicPath: "/p", PrivatePath: "/q"}},
	})

	_, err := GetAppConfig("unknown")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"unknown"`)
}

func TestListAppIds_EmptyWhenNoConfigLoaded(t *testing.T) {
	resetAppsEnv(t) // also resets the cache
	assert.Empty(t, ListAppIds())
}

func TestListAppIds_ReturnsAllLoaded(t *testing.T) {
	// The registry is single-app in production stateless mode, but it backs
	// the multi-app control-plane store too, seed several apps directly to
	// prove the lookup surface holds more than one.
	resetAppsEnv(t)
	SetAppsForTest([]AppConfig{
		{Id: "a", AccessToken: "t", Keys: KeysConfig{Mode: KeysModeLocal, PublicPath: "/p", PrivatePath: "/q"}},
		{Id: "b", AccessToken: "t", Keys: KeysConfig{Mode: KeysModeLocal, PublicPath: "/p", PrivatePath: "/q"}},
		{Id: "c", AccessToken: "t", Keys: KeysConfig{Mode: KeysModeLocal, PublicPath: "/p", PrivatePath: "/q"}},
	})
	assert.ElementsMatch(t, []string{"a", "b", "c"}, ListAppIds())
}

// -----------------------------------------------------------------------------
// Concurrency, GetAppConfig / ListAppIds must be safe for concurrent reads
// (the server calls them from every handler goroutine).
// -----------------------------------------------------------------------------

func TestLookupIsConcurrencySafe(t *testing.T) {
	resetAppsEnv(t)
	// Build a decent-sized config so the map iteration in ListAppIds has
	// something to do under contention.
	var apps []AppConfig
	for i := 0; i < 50; i++ {
		apps = append(apps, AppConfig{
			Id:          fmt.Sprintf("app-%d", i),
			AccessToken: "t",
			Keys:        KeysConfig{Mode: KeysModeLocal, PublicPath: "/p", PrivatePath: "/q"},
		})
	}
	SetAppsForTest(apps)

	const readers = 16
	const iters = 500
	done := make(chan struct{}, readers)
	for r := 0; r < readers; r++ {
		go func() {
			for i := 0; i < iters; i++ {
				_, _ = GetAppConfig(fmt.Sprintf("app-%d", i%50))
				_ = ListAppIds()
			}
			done <- struct{}{}
		}()
	}
	for r := 0; r < readers; r++ {
		<-done
	}
	// Test passes if no race was detected (go test -race) and no panic.
}

// -----------------------------------------------------------------------------
// ResetAppsForTest, must actually reset so test isolation holds. If this
// regresses, every other test in the package becomes order-dependent.
// -----------------------------------------------------------------------------

// -----------------------------------------------------------------------------
// Optional Name field, display label used by the dashboard. Absence must be
// accepted, presence must round-trip, and ListApps must surface it.
// -----------------------------------------------------------------------------

func TestGetAppConfig_PreservesOptionalName(t *testing.T) {
	resetAppsEnv(t)
	SetAppsForTest([]AppConfig{
		{Id: "no-name", AccessToken: "t", Keys: KeysConfig{Mode: KeysModeLocal, PublicPath: "/p", PrivatePath: "/q"}},
		{Id: "with-name", Name: "Production", AccessToken: "t", Keys: KeysConfig{Mode: KeysModeLocal, PublicPath: "/p", PrivatePath: "/q"}},
	})

	noName, err := GetAppConfig("no-name")
	require.NoError(t, err)
	assert.Empty(t, noName.Name)

	withName, err := GetAppConfig("with-name")
	require.NoError(t, err)
	assert.Equal(t, "Production", withName.Name)
}

func TestListApps_ReturnsDescriptorsWithName(t *testing.T) {
	resetAppsEnv(t)
	SetAppsForTest([]AppConfig{
		{Id: "a", Name: "App A", AccessToken: "t", Keys: KeysConfig{Mode: KeysModeLocal, PublicPath: "/p", PrivatePath: "/q"}},
		{Id: "b", AccessToken: "t", Keys: KeysConfig{Mode: KeysModeLocal, PublicPath: "/p", PrivatePath: "/q"}},
	})

	// Descriptors are returned in unspecified order; reshape into a map so
	// the assertion is stable.
	byId := map[string]AppDescriptor{}
	for _, d := range ListApps() {
		byId[d.Id] = d
	}
	assert.Equal(t, "App A", byId["a"].Name)
	assert.Equal(t, "", byId["b"].Name)
	assert.Len(t, byId, 2)
}

func TestListApps_EmptyWhenNoConfigLoaded(t *testing.T) {
	resetAppsEnv(t)
	assert.Empty(t, ListApps())
}

func TestResetAppsForTest_ClearsRegistry(t *testing.T) {
	resetAppsEnv(t)
	SetAppsForTest([]AppConfig{
		{Id: "x", AccessToken: "t", Keys: KeysConfig{Mode: KeysModeLocal, PublicPath: "/p", PrivatePath: "/q"}},
	})
	assert.NotEmpty(t, ListAppIds())

	ResetAppsForTest()
	assert.Empty(t, ListAppIds())
	_, err := GetAppConfig("x")
	assert.Error(t, err)
}

// -----------------------------------------------------------------------------
// LegacyFallbackAppId, which app a manifest/asset request with no expo-app-id
// header belongs to. Drives whether v1 clients keep receiving updates after the
// v2 upgrade, so each branch is pinned explicitly.
// -----------------------------------------------------------------------------

func TestLegacyFallbackAppId_ReturnsExpoAppId(t *testing.T) {
	resetAppsEnv(t)
	os.Setenv("EXPO_APP_ID", "d8471dfc-c3e9-4e14-afd9-21dc34cc498a")

	assert.Equal(t, "d8471dfc-c3e9-4e14-afd9-21dc34cc498a", LegacyFallbackAppId())
}

func TestLegacyFallbackAppId_EmptyWithoutExpoAppId(t *testing.T) {
	// The control-plane shape: apps come from the dashboard, EXPO_APP_ID is
	// unset, no v1 client can exist. Nothing to fall back to, so the header
	// stays mandatory and the handler rejects the request.
	resetAppsEnv(t)

	assert.Empty(t, LegacyFallbackAppId())
}

func TestLegacyFallbackAppId_SkipFlagDisablesFallback(t *testing.T) {
	// strconv.ParseBool accepts 1/t/T/TRUE/true/True, every truthy spelling
	// must disable the fallback, so an operator who opted out gets the opt-out
	// no matter how they spelled it.
	for _, v := range []string{"true", "1", "True", "TRUE", "t"} {
		t.Run("skip="+v, func(t *testing.T) {
			resetAppsEnv(t)
			os.Setenv("EXPO_APP_ID", "d8471dfc-c3e9-4e14-afd9-21dc34cc498a")
			os.Setenv("SKIP_LEGACY_APP_ID_FALLBACK", v)

			assert.Empty(t, LegacyFallbackAppId())
		})
	}
}

func TestLegacyFallbackAppId_FalseyOrUnparseableSkipKeepsFallback(t *testing.T) {
	// Anything that parses false, or does not parse at all, must leave the
	// fallback on. Failing open matters here: guessing that "yes" means true
	// would silently kill the update channel of every v1 client on the deploy.
	for _, v := range []string{"false", "0", "", "yes", "nope"} {
		t.Run("skip="+v, func(t *testing.T) {
			resetAppsEnv(t)
			os.Setenv("EXPO_APP_ID", "d8471dfc-c3e9-4e14-afd9-21dc34cc498a")
			os.Setenv("SKIP_LEGACY_APP_ID_FALLBACK", v)

			assert.Equal(t, "d8471dfc-c3e9-4e14-afd9-21dc34cc498a", LegacyFallbackAppId())
		})
	}
}

func TestLegacyFallbackAppId_TrimsWhitespace(t *testing.T) {
	// ReadAppsFromFlatEnv trims EXPO_APP_ID before registering the app, so a
	// padded value must resolve to the same id here or the fallback would look
	// up an app that is not in the registry.
	resetAppsEnv(t)
	os.Setenv("EXPO_APP_ID", "  d8471dfc-c3e9-4e14-afd9-21dc34cc498a\n")

	assert.Equal(t, "d8471dfc-c3e9-4e14-afd9-21dc34cc498a", LegacyFallbackAppId())
}
