// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package licensing

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func setupTestKeypair(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	previous := verifyKeyHex
	verifyKeyHex = hex.EncodeToString(pub)
	t.Cleanup(func() {
		verifyKeyHex = previous
		Deactivate()
	})
	return priv
}

// signTestKey builds a key in Keygen's ED25519_SIGN format: the signature
// covers the literal string "key/" + base64url(dataset).
func signTestKey(t *testing.T, priv ed25519.PrivateKey, expiry *time.Time) string {
	t.Helper()
	dataset := keygenDataset{}
	dataset.Account.ID = "b3195b02-fde3-4cf5-9279-e55751e41005"
	dataset.Product.ID = "11e753b3-9f14-4f5a-90de-e1962ba62be8"
	dataset.Policy.ID = "4d9663af-1f29-47f5-9bb2-8905c7cbdba7"
	dataset.License.ID = "6e5b1b1d-2a11-4b64-8de0-d160b81a0301"
	dataset.License.Created = time.Now().UTC()
	dataset.License.Expiry = expiry
	payload, err := json.Marshal(dataset)
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	sig := ed25519.Sign(priv, []byte("key/"+encoded))
	return fmt.Sprintf("key/%s.%s", encoded, base64.RawURLEncoding.EncodeToString(sig))
}

func in(d time.Duration) *time.Time {
	at := time.Now().Add(d)
	return &at
}

func TestActivateValidKey(t *testing.T) {
	priv := setupTestKeypair(t)
	key := signTestKey(t, priv, in(24*time.Hour))
	l, err := Activate(key)
	if err != nil {
		t.Fatalf("expected activation to succeed, got %v", err)
	}
	if l.LicenseID != "6e5b1b1d-2a11-4b64-8de0-d160b81a0301" {
		t.Fatalf("unexpected payload: %+v", l)
	}
	if !IsEnterprise() {
		t.Fatal("expected IsEnterprise to be true after activation")
	}
	Deactivate()
	if IsEnterprise() {
		t.Fatal("expected IsEnterprise to be false after deactivation")
	}
}

func TestActivatePerpetualKey(t *testing.T) {
	priv := setupTestKeypair(t)
	key := signTestKey(t, priv, nil)
	l, err := Activate(key)
	if err != nil {
		t.Fatalf("expected activation to succeed, got %v", err)
	}
	if l.Expiry != nil || l.Expired() {
		t.Fatalf("expected perpetual license, got %+v", l)
	}
}

func TestRejectsTamperedKey(t *testing.T) {
	priv := setupTestKeypair(t)
	key := signTestKey(t, priv, in(24*time.Hour))
	other := signTestKey(t, priv, in(48*time.Hour))
	// Graft other's dataset onto key's signature. Each part is genuine on its
	// own, the datasets are guaranteed to differ (24h vs 48h expiry), so
	// only signature verification over the full payload catches the mix.
	dataset := strings.SplitN(strings.TrimPrefix(other, keyPrefix), ".", 2)[0]
	signature := strings.SplitN(strings.TrimPrefix(key, keyPrefix), ".", 2)[1]
	tampered := keyPrefix + dataset + "." + signature
	if _, err := Activate(tampered); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected ErrInvalidSignature for tampered key, got %v", err)
	}
	if IsEnterprise() {
		t.Fatal("expected no active license after rejected activation")
	}
}

func TestRejectsKeyFromWrongSigningKey(t *testing.T) {
	setupTestKeypair(t)
	_, otherPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	key := signTestKey(t, otherPriv, in(24*time.Hour))
	if _, err := Activate(key); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected ErrInvalidSignature, got %v", err)
	}
}

func TestRejectsExpiredKey(t *testing.T) {
	priv := setupTestKeypair(t)
	key := signTestKey(t, priv, in(-time.Hour))
	if _, err := Activate(key); !errors.Is(err, ErrExpired) {
		t.Fatalf("expected ErrExpired, got %v", err)
	}
}

func TestRejectsKeyWithoutPrefix(t *testing.T) {
	priv := setupTestKeypair(t)
	key := signTestKey(t, priv, in(24*time.Hour))
	if _, err := Parse(key[len("key/"):]); !errors.Is(err, ErrMalformedKey) {
		t.Fatalf("expected ErrMalformedKey, got %v", err)
	}
}

func TestNoVerifyKeyConfigured(t *testing.T) {
	previous := verifyKeyHex
	verifyKeyHex = ""
	t.Cleanup(func() { verifyKeyHex = previous })
	if _, err := Parse("key/whatever.sig"); !errors.Is(err, ErrNoVerifyKey) {
		t.Fatalf("expected ErrNoVerifyKey, got %v", err)
	}
}
