package keyStore

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"xprem/config"
	"xprem/internal/crypto"

	"github.com/stretchr/testify/assert"
)

// Two distinct b64-encoded PEM stubs. Contents are not real keys — the point
// of these tests is that each app's own KeysConfig drives key resolution, not
// cryptographic behavior. decodeB64 decodes raw bytes, so any non-empty b64
// round-trips cleanly here.
const (
	app1PEMB64 = "LS0tLS1CRUdJTiBBUFAgT05FIEtFWS0tLS0tCmtleTEKLS0tLS1FTkQgQVBQIE9ORSBLRVktLS0tLQo="
	app1PEM    = "-----BEGIN APP ONE KEY-----\nkey1\n-----END APP ONE KEY-----\n"

	app2PEMB64 = "LS0tLS1CRUdJTiBBUFAgVFdPIEtFWS0tLS0tCmtleTIKLS0tLS1FTkQgQVBQIFRXTyBLRVktLS0tLQo="
	app2PEM    = "-----BEGIN APP TWO KEY-----\nkey2\n-----END APP TWO KEY-----\n"
)

func envKeysApp(id, pubB64, privB64 string) config.AppConfig {
	return config.AppConfig{
		Id:          id,
		AccessToken: "t",
		Keys: config.KeysConfig{
			Mode:       config.KeysModeEnvironment,
			PublicB64:  pubB64,
			PrivateB64: privB64,
		},
	}
}

func TestGetPrivateExpoKey_IsolatedPerApp(t *testing.T) {
	// Multi-app correctness property — two apps served by the same instance
	// must NOT sign with the same private key. A regression that resolved the
	// wrong app's KeysConfig would silently cross-contaminate signatures
	// between tenants.
	app1 := envKeysApp("app-1", app1PEMB64, app1PEMB64)
	app2 := envKeysApp("app-2", app2PEMB64, app2PEMB64)

	priv1 := GetPrivateExpoKey(app1)
	priv2 := GetPrivateExpoKey(app2)

	assert.Equal(t, app1PEM, priv1)
	assert.Equal(t, app2PEM, priv2)
	assert.NotEqual(t, priv1, priv2, "each app must yield its own key")
}

func TestGetPublicExpoKey_IsolatedPerApp(t *testing.T) {
	app1 := envKeysApp("app-1", app1PEMB64, app1PEMB64)
	app2 := envKeysApp("app-2", app2PEMB64, app2PEMB64)

	assert.Equal(t, app1PEM, GetPublicExpoKey(app1))
	assert.Equal(t, app2PEM, GetPublicExpoKey(app2))
}

func TestGetExpoKey_UnconfiguredKeysReturnEmpty(t *testing.T) {
	// An app whose KeysConfig carries no material is treated as "no key
	// available" rather than a crash — the handler layer already returns 404
	// for unknown apps before we get here, so returning "" keeps the key path
	// defensive.
	app := config.AppConfig{Id: "app-1", AccessToken: "t", Keys: config.KeysConfig{Mode: config.KeysModeEnvironment}}
	assert.Empty(t, GetPrivateExpoKey(app))
	assert.Empty(t, GetPublicExpoKey(app))
}

// dbKeysApp mirrors what GetAppByID hydrates from an apps row: the id is the
// row's own id, and the sealed blobs are whatever that row happens to carry.
func dbKeysApp(id, sealedPub, sealedPriv string) config.AppConfig {
	return config.AppConfig{
		Id:          id,
		AccessToken: "t",
		Keys: config.KeysConfig{
			Mode:             config.KeysModeDatabase,
			SealedPublicKey:  sealedPub,
			SealedPrivateKey: sealedPriv,
		},
	}
}

func sealFor(t *testing.T, appId, pub, priv string) (string, string) {
	t.Helper()
	master := []byte(ReadDBKeysMasterKey())
	sealedPub, err := crypto.SealAESGCM([]byte(pub), master, AppKeyAAD(appId, true))
	if err != nil {
		t.Fatalf("failed to seal public key: %v", err)
	}
	sealedPriv, err := crypto.SealAESGCM([]byte(priv), master, AppKeyAAD(appId, false))
	if err != nil {
		t.Fatalf("failed to seal private key: %v", err)
	}
	return sealedPub, sealedPriv
}

func setTestMasterKey(t *testing.T) {
	t.Helper()
	t.Setenv("DB_KEYS_MASTER_KEY_B64",
		base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
}

func TestDatabaseKeys_RoundTripForOwnApp(t *testing.T) {
	setTestMasterKey(t)
	const appId = "11111111-1111-1111-1111-111111111111"
	sealedPub, sealedPriv := sealFor(t, appId, app1PEM, app2PEM)
	app := dbKeysApp(appId, sealedPub, sealedPriv)

	assert.Equal(t, app1PEM, GetPublicExpoKey(app))
	assert.Equal(t, app2PEM, GetPrivateExpoKey(app))
}

// The reason the aad exists. All apps seal under one master key, so app 2's row
// carrying app 1's blob would otherwise decrypt cleanly and sign app 2's
// manifests with app 1's key — surfacing only as a signature rejection on every
// installed client. Binding turns it into a failed unseal at the point of use.
func TestDatabaseKeys_BlobFromAnotherAppDoesNotOpen(t *testing.T) {
	setTestMasterKey(t)
	const app1Id = "11111111-1111-1111-1111-111111111111"
	const app2Id = "22222222-2222-2222-2222-222222222222"
	app1SealedPub, app1SealedPriv := sealFor(t, app1Id, app1PEM, app1PEM)

	// App 2's row, with app 1's blobs pasted into it.
	swapped := dbKeysApp(app2Id, app1SealedPub, app1SealedPriv)

	assert.Empty(t, GetPrivateExpoKey(swapped), "app 1's sealed private key must not open under app 2's id")
	assert.Empty(t, GetPublicExpoKey(swapped), "app 1's sealed public key must not open under app 2's id")
}

// Both halves sit under the same master key in adjacent columns, so the public
// blob landing in the private column is the same class of mistake.
func TestDatabaseKeys_HalvesAreNotInterchangeable(t *testing.T) {
	setTestMasterKey(t)
	const appId = "11111111-1111-1111-1111-111111111111"
	sealedPub, sealedPriv := sealFor(t, appId, app1PEM, app2PEM)

	crossed := dbKeysApp(appId, sealedPriv, sealedPub) // columns swapped

	assert.Empty(t, GetPublicExpoKey(crossed), "the private blob must not open as the public half")
	assert.Empty(t, GetPrivateExpoKey(crossed), "the public blob must not open as the private half")
}

func TestDatabaseKeys_MissingMasterKeyReturnsEmpty(t *testing.T) {
	setTestMasterKey(t)
	const appId = "11111111-1111-1111-1111-111111111111"
	sealedPub, sealedPriv := sealFor(t, appId, app1PEM, app1PEM)

	t.Setenv("DB_KEYS_MASTER_KEY_B64", "")
	app := dbKeysApp(appId, sealedPub, sealedPriv)
	assert.Empty(t, GetPrivateExpoKey(app))
	assert.Empty(t, GetPublicExpoKey(app))
}

// --- CloudFront key resolution ---

// The CloudFront key follows KEYS_STORAGE_TYPE when it is set (stateless
// mode), and falls back to trying every source in order when it is unset
// (control plane, where the variable does not exist).

func setCloudfrontEnv(t *testing.T, mode, secretId, b64, path string) {
	t.Helper()
	t.Setenv("KEYS_STORAGE_TYPE", mode)
	t.Setenv("AWSSM_CLOUDFRONT_PRIVATE_KEY_SECRET_ID", secretId)
	t.Setenv("PRIVATE_CLOUDFRONT_KEY_B64", b64)
	t.Setenv("PRIVATE_CLOUDFRONT_KEY_PATH", path)
}

func writeTempPEM(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cloudfront.pem")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCloudfrontKey_LocalModeReadsFileAndIgnoresB64(t *testing.T) {
	path := writeTempPEM(t, app1PEM)
	// b64 of a DIFFERENT key is also set — the configured mode must win.
	setCloudfrontEnv(t, "local", "", app2PEMB64, path)

	assert.Equal(t, app1PEM, GetPrivateCloudfrontKey(),
		"KEYS_STORAGE_TYPE=local must read the file, not the b64 leftover")
}

func TestCloudfrontKey_EnvironmentModeReadsB64AndIgnoresFile(t *testing.T) {
	path := writeTempPEM(t, app1PEM)
	setCloudfrontEnv(t, "environment", "", app2PEMB64, path)

	assert.Equal(t, app2PEM, GetPrivateCloudfrontKey(),
		"KEYS_STORAGE_TYPE=environment must decode the b64, not read the file")
}

func TestCloudfrontKey_ModeSelectedSourceEmptyDoesNotFallBack(t *testing.T) {
	// local mode with no PATH: the b64 leftover must NOT silently win —
	// that was the old behavior this resolution replaces.
	setCloudfrontEnv(t, "local", "", app2PEMB64, "")

	assert.Empty(t, GetPrivateCloudfrontKey(),
		"an empty mode-selected source must disable the key, not fall back")
}

func TestCloudfrontKey_UnsetModeTriesSourcesInOrder(t *testing.T) {
	path := writeTempPEM(t, app1PEM)

	// b64 outranks path when both are set.
	setCloudfrontEnv(t, "", "", app2PEMB64, path)
	assert.Equal(t, app2PEM, GetPrivateCloudfrontKey())

	// path is used when it is the only source.
	setCloudfrontEnv(t, "", "", "", path)
	assert.Equal(t, app1PEM, GetPrivateCloudfrontKey())
}
