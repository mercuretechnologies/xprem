package migrations

import (
	"context"
	"testing"
)

// A fresh control-plane install sets DB_URL and a master key but no
// EXPO_APP_ID, apps are created from the dashboard. The migration must treat
// that as "nothing legacy to import" and no-op. It previously let
// config.ReadAppsFromFlatEnv' "no app config found" error escape, which goose turned into
// a fatal, so the documented fresh-install path could never boot.
//
// dbEngine is nil here: reaching it at all means the guard failed to return
// early, which is exactly what this test is pinning down.
func TestUpMigrateEnvJSONNoOpsWithoutLegacyAppId(t *testing.T) {
	t.Setenv("EXPO_APP_ID", "")

	if err := UpMigrateEnvJSON(context.Background(), nil); err != nil {
		t.Fatalf("expected a no-op on a fresh control-plane install, got: %v", err)
	}
}

// Whitespace is not a configured app id. TrimSpace keeps a stray " " in the
// deployment manifest from being read as a real legacy app and dragging the
// boot into the migration path.
func TestUpMigrateEnvJSONNoOpsWithBlankLegacyAppId(t *testing.T) {
	t.Setenv("EXPO_APP_ID", "   ")

	if err := UpMigrateEnvJSON(context.Background(), nil); err != nil {
		t.Fatalf("expected a no-op for a blank EXPO_APP_ID, got: %v", err)
	}
}
