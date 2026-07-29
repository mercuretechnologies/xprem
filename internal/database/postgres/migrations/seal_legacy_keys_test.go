package migrations

import (
	"encoding/base64"
	"expo-open-ota/config"
	"expo-open-ota/internal/crypto"
	"expo-open-ota/internal/database/postgres/pgdb"
	"expo-open-ota/internal/keyStore"
	"expo-open-ota/internal/store"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// setMasterKey installs a valid 32-byte master key for the duration of the test.
func setMasterKey(t *testing.T) []byte {
	t.Helper()
	raw := []byte("0123456789abcdef0123456789abcdef") // exactly 32 bytes
	t.Setenv("DB_KEYS_MASTER_KEY_B64", base64.StdEncoding.EncodeToString(raw))
	return raw
}

func newKeyPair(t *testing.T) (string, string) {
	t.Helper()
	pub, priv, err := crypto.GenerateRSAKeyPair()
	if err != nil {
		t.Fatalf("failed to generate key pair: %v", err)
	}
	return pub, priv
}

func writeKeyFiles(t *testing.T, pub, priv string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	pubPath := filepath.Join(dir, "public-key.pem")
	privPath := filepath.Join(dir, "private-key.pem")
	if err := os.WriteFile(pubPath, []byte(pub), 0o600); err != nil {
		t.Fatalf("failed to write public key: %v", err)
	}
	if err := os.WriteFile(privPath, []byte(priv), 0o600); err != nil {
		t.Fatalf("failed to write private key: %v", err)
	}
	return pubPath, privPath
}

// The whole point of resealing rather than regenerating: expo-updates clients
// pin the certificate at build time, so the migrated app must keep the exact
// same key pair, byte for byte.
func TestSealLegacyKeysIntoDBPreservesLocalKeyPair(t *testing.T) {
	masterKey := setMasterKey(t)
	pub, priv := newKeyPair(t)
	pubPath, privPath := writeKeyFiles(t, pub, priv)

	app := config.AppConfig{
		Id: "11111111-1111-1111-1111-111111111111",
		Keys: config.KeysConfig{
			Mode:        config.KeysModeLocal,
			PublicPath:  pubPath,
			PrivatePath: privPath,
		},
	}
	params := pgdb.MigrateLegacyAppParams{}

	if err := sealLegacyKeysIntoDB(app, &params); err != nil {
		t.Fatalf("expected local keys to seal, got error: %v", err)
	}

	if params.KeysMode == nil || *params.KeysMode != string(config.KeysModeDatabase) {
		t.Fatalf("expected keys_mode=database, got %v", params.KeysMode)
	}
	if params.PathPublicKey != nil || params.PathPrivateKey != nil {
		t.Errorf("expected the stale key paths to be dropped, got %v / %v", params.PathPublicKey, params.PathPrivateKey)
	}
	if params.SealedPublicKey == nil || params.SealedPrivateKey == nil {
		t.Fatal("expected both keys to be sealed")
	}

	unsealedPub, err := crypto.UnsealAESGCM(*params.SealedPublicKey, masterKey, keyStore.AppKeyAAD(app.Id, true))
	if err != nil {
		t.Fatalf("failed to unseal public key: %v", err)
	}
	unsealedPriv, err := crypto.UnsealAESGCM(*params.SealedPrivateKey, masterKey, keyStore.AppKeyAAD(app.Id, false))
	if err != nil {
		t.Fatalf("failed to unseal private key: %v", err)
	}
	if string(unsealedPub) != pub {
		t.Error("public key did not survive the seal/unseal round trip")
	}
	if string(unsealedPriv) != priv {
		t.Error("private key did not survive the seal/unseal round trip")
	}
}

// mode=environment has no column in the apps table, so before this conversion
// it migrated with no key at all and broke signing at the first signature.
func TestSealLegacyKeysIntoDBPreservesEnvironmentKeyPair(t *testing.T) {
	masterKey := setMasterKey(t)
	pub, priv := newKeyPair(t)

	app := config.AppConfig{
		Id: "22222222-2222-2222-2222-222222222222",
		Keys: config.KeysConfig{
			Mode:       config.KeysModeEnvironment,
			PublicB64:  base64.StdEncoding.EncodeToString([]byte(pub)),
			PrivateB64: base64.StdEncoding.EncodeToString([]byte(priv)),
		},
	}
	params := pgdb.MigrateLegacyAppParams{}

	if err := sealLegacyKeysIntoDB(app, &params); err != nil {
		t.Fatalf("expected environment keys to seal, got error: %v", err)
	}
	if params.KeysMode == nil || *params.KeysMode != string(config.KeysModeDatabase) {
		t.Fatalf("expected keys_mode=database, got %v", params.KeysMode)
	}

	unsealedPriv, err := crypto.UnsealAESGCM(*params.SealedPrivateKey, masterKey, keyStore.AppKeyAAD(app.Id, false))
	if err != nil {
		t.Fatalf("failed to unseal private key: %v", err)
	}
	// Sealed content must be the decoded PEM, matching what mode=database
	// stores when the dashboard generates a pair, not the b64 wrapper.
	if string(unsealedPriv) != priv {
		t.Error("private key did not survive the seal/unseal round trip")
	}
}

// The sealed key must still sign, and verify against the sealed public half.
func TestSealedLocalKeyStillSigns(t *testing.T) {
	masterKey := setMasterKey(t)
	pub, priv := newKeyPair(t)
	pubPath, privPath := writeKeyFiles(t, pub, priv)

	app := config.AppConfig{
		Id:   "33333333-3333-3333-3333-333333333333",
		Keys: config.KeysConfig{Mode: config.KeysModeLocal, PublicPath: pubPath, PrivatePath: privPath},
	}
	params := pgdb.MigrateLegacyAppParams{}
	if err := sealLegacyKeysIntoDB(app, &params); err != nil {
		t.Fatalf("failed to seal: %v", err)
	}

	unsealedPriv, err := crypto.UnsealAESGCM(*params.SealedPrivateKey, masterKey, keyStore.AppKeyAAD(app.Id, false))
	if err != nil {
		t.Fatalf("failed to unseal: %v", err)
	}
	if _, err := crypto.SignRSASHA256("some-manifest-body", string(unsealedPriv)); err != nil {
		t.Fatalf("sealed private key can no longer sign: %v", err)
	}
}

// aws-secrets-manager is a reference that stays reachable from every replica,
// so it must pass through the migration untouched.
func TestSealLegacyKeysIntoDBLeavesAWSSecretsManagerAlone(t *testing.T) {
	setMasterKey(t)
	secretPub, secretPriv := "expo-public-secret-id", "expo-private-secret-id"
	app := config.AppConfig{
		Id: "44444444-4444-4444-4444-444444444444",
		Keys: config.KeysConfig{
			Mode:            config.KeysModeAWSSM,
			PublicSecretId:  secretPub,
			PrivateSecretId: secretPriv,
		},
	}
	params := pgdb.MigrateLegacyAppParams{
		AwsSecretIDPublic:  &secretPub,
		AwsSecretIDPrivate: &secretPriv,
	}

	if err := sealLegacyKeysIntoDB(app, &params); err != nil {
		t.Fatalf("expected aws-sm to be a no-op, got error: %v", err)
	}
	if params.SealedPublicKey != nil || params.SealedPrivateKey != nil {
		t.Error("aws-sm app must not have keys sealed into the database")
	}
	if params.AwsSecretIDPublic == nil || *params.AwsSecretIDPublic != secretPub {
		t.Error("aws-sm secret ids must survive the migration untouched")
	}
}

// Failing loudly beats migrating an empty key and breaking signing at runtime.
func TestSealLegacyKeysIntoDBFailsWhenKeysAreUnreadable(t *testing.T) {
	setMasterKey(t)
	app := config.AppConfig{
		Id: "55555555-5555-5555-5555-555555555555",
		Keys: config.KeysConfig{
			Mode:        config.KeysModeLocal,
			PublicPath:  filepath.Join(t.TempDir(), "does-not-exist.pem"),
			PrivatePath: filepath.Join(t.TempDir(), "missing-too.pem"),
		},
	}
	params := pgdb.MigrateLegacyAppParams{}

	if err := sealLegacyKeysIntoDB(app, &params); err == nil {
		t.Fatal("expected the migration to abort on unreadable key files, got nil")
	}
	if params.SealedPrivateKey != nil {
		t.Error("no key should have been sealed on the failure path")
	}
}

// A control-plane deployment that carries EXPO_APP_ID but none of the key
// vars parses as mode=local (the KEYS_STORAGE_TYPE default) with empty paths.
// The error must call out the missing env vars, not a generic read failure,
// because that is what the operator actually has to fix.
func TestSealLegacyKeysIntoDBNamesMissingKeyEnvVars(t *testing.T) {
	setMasterKey(t)
	for _, app := range []config.AppConfig{
		{
			Id:   "77777777-7777-7777-7777-777777777777",
			Keys: config.KeysConfig{Mode: config.KeysModeLocal},
		},
		{
			Id:   "77777777-7777-7777-7777-777777777777",
			Keys: config.KeysConfig{Mode: config.KeysModeEnvironment, PublicB64: "notempty"},
		},
	} {
		params := pgdb.MigrateLegacyAppParams{}
		err := sealLegacyKeysIntoDB(app, &params)
		if err == nil {
			t.Fatalf("expected the migration to abort on empty %s key config, got nil", app.Keys.Mode)
		}
		if !strings.Contains(err.Error(), "KEYS_STORAGE_TYPE") {
			t.Errorf("mode=%s: error should point at the missing key env vars, got: %v", app.Keys.Mode, err)
		}
		if params.SealedPrivateKey != nil {
			t.Error("no key should have been sealed on the failure path")
		}
	}
}

func TestSealLegacyKeysIntoDBFailsWithoutMasterKey(t *testing.T) {
	t.Setenv("DB_KEYS_MASTER_KEY_B64", "")
	pub, priv := newKeyPair(t)
	pubPath, privPath := writeKeyFiles(t, pub, priv)

	app := config.AppConfig{
		Id:   "66666666-6666-6666-6666-666666666666",
		Keys: config.KeysConfig{Mode: config.KeysModeLocal, PublicPath: pubPath, PrivatePath: privPath},
	}
	params := pgdb.MigrateLegacyAppParams{}

	if err := sealLegacyKeysIntoDB(app, &params); err == nil {
		t.Fatal("expected the migration to abort without a master key, got nil")
	}
}

// The migration canonicalizes a legacy app id with uuid.Parse before using it as
// both the row key and the key-sealing aad. This guards that coupling: whatever
// uuid.Parse canonicalizes to must be exactly what the row reads back as, or the
// blob would bind to an id no unseal ever reconstructs and signing would break
// after migration.
func TestCanonicalAppIdMatchesWhatTheRowReadsBack(t *testing.T) {
	for _, raw := range []string{
		"11111111-aaaa-4bbb-8ccc-111111111111",   // already canonical
		"11111111-AAAA-4BBB-8CCC-111111111111",   // uppercase
		"{11111111-aaaa-4bbb-8ccc-111111111111}", // braced
		"urn:uuid:11111111-aaaa-4bbb-8ccc-111111111111",
	} {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			t.Fatalf("uuid.Parse(%q) unexpectedly failed: %v", raw, err)
		}
		if rowValue := store.ToPgUUID(raw).String(); rowValue != parsed.String() {
			t.Errorf("id %q: the row reads back as %q but the aad would be sealed under %q",
				raw, rowValue, parsed.String())
		}
	}
}

// Sealing must bind to the canonical id, not the raw one the operator typed.
func TestSealLegacyKeysBindsToCanonicalAppId(t *testing.T) {
	masterKey := setMasterKey(t)
	pub, priv := newKeyPair(t)
	pubPath, privPath := writeKeyFiles(t, pub, priv)

	const canonical = "77777777-aaaa-4bbb-8ccc-777777777777"
	app := config.AppConfig{
		// What the migration loop hands down, having canonicalized EXPO_APP_ID.
		Id:   uuid.MustParse("77777777-AAAA-4BBB-8CCC-777777777777").String(),
		Keys: config.KeysConfig{Mode: config.KeysModeLocal, PublicPath: pubPath, PrivatePath: privPath},
	}
	params := pgdb.MigrateLegacyAppParams{}
	if err := sealLegacyKeysIntoDB(app, &params); err != nil {
		t.Fatalf("failed to seal: %v", err)
	}

	// The unseal path only ever sees the canonical id, read back off the row.
	unsealed, err := crypto.UnsealAESGCM(*params.SealedPrivateKey, masterKey, keyStore.AppKeyAAD(canonical, false))
	if err != nil {
		t.Fatalf("sealed key does not open under the canonical app id: %v", err)
	}
	if string(unsealed) != priv {
		t.Error("private key did not survive the round trip")
	}
}
