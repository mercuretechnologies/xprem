package expo

import (
	"testing"
	"xprem/internal/types"
)

func TestHasCredential(t *testing.T) {
	token := " expo-token "
	blank := "  "
	session := "session-secret"

	if HasCredential(types.Auth{}) {
		t.Fatal("empty auth must not count as a credential")
	}
	if HasCredential(types.Auth{Token: &blank}) {
		t.Fatal("whitespace-only token must not count as a credential")
	}
	if !HasCredential(types.Auth{Token: &token}) {
		t.Fatal("non-blank token must count as a credential")
	}
	if !HasCredential(types.Auth{SessionSecret: &session}) {
		t.Fatal("expo-cli session must count as a credential")
	}
}
