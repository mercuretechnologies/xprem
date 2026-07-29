// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

// Package licensing implements offline validation of Mercure Technologies
// Enterprise license keys issued through Keygen (https://keygen.sh) with the
// ED25519_SIGN scheme. A key looks like:
//
//	key/{base64url(JSON dataset)}.{base64url(Ed25519 signature)}
//
// The signature covers the literal string "key/" + the base64url dataset and
// is verified against the Keygen account's Ed25519 verify key embedded below,
// so validation works fully offline, no network calls, no phone home.
package licensing

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

// verifyKeyHex is the Ed25519 verify key of the Mercure Technologies Keygen
// account (dashboard > Settings > Ed25519 Verify Key). It is public by
// nature; only Keygen holds the matching signing key.
var verifyKeyHex = "55575e10d6b6857c295895e05e06671bebdcf99aaa9b1a17c1538c32360984bf"

const keyPrefix = "key/"

// License is the payload embedded in a signed Keygen license key.
type License struct {
	LicenseID string
	ProductID string
	PolicyID  string
	Created   time.Time
	Expiry    *time.Time // nil means the license never expires
}

func (l *License) Expired() bool {
	return l.Expiry != nil && time.Now().After(*l.Expiry)
}

var (
	ErrNoVerifyKey      = errors.New("licensing: no verify key configured in this build")
	ErrMalformedKey     = errors.New("licensing: malformed license key")
	ErrInvalidSignature = errors.New("licensing: invalid signature")
	ErrExpired          = errors.New("licensing: license key is expired")
)

var (
	mu      sync.RWMutex
	current *License
)

// keygenDataset mirrors the JSON dataset Keygen embeds in ED25519_SIGN keys.
type keygenDataset struct {
	Account struct {
		ID string `json:"id"`
	} `json:"account"`
	Product struct {
		ID string `json:"id"`
	} `json:"product"`
	Policy struct {
		ID       string `json:"id"`
		Duration *int64 `json:"duration"`
	} `json:"policy"`
	License struct {
		ID      string     `json:"id"`
		Created time.Time  `json:"created"`
		Expiry  *time.Time `json:"expiry"`
	} `json:"license"`
}

// b64urlDecode accepts both raw and padded base64url, since encoders differ.
func b64urlDecode(s string) ([]byte, error) {
	if decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "=")); err == nil {
		return decoded, nil
	}
	return base64.URLEncoding.DecodeString(s)
}

// Parse verifies the signature of a license key and returns its payload.
// It does not check expiration; Activate does.
func Parse(key string) (*License, error) {
	if verifyKeyHex == "" {
		return nil, ErrNoVerifyKey
	}
	pub, err := hex.DecodeString(verifyKeyHex)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return nil, ErrNoVerifyKey
	}
	key = strings.TrimSpace(key)
	if !strings.HasPrefix(key, keyPrefix) {
		return nil, ErrMalformedKey
	}
	parts := strings.SplitN(strings.TrimPrefix(key, keyPrefix), ".", 2)
	if len(parts) != 2 {
		return nil, ErrMalformedKey
	}
	sig, err := b64urlDecode(parts[1])
	if err != nil {
		return nil, ErrMalformedKey
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), []byte(keyPrefix+parts[0]), sig) {
		return nil, ErrInvalidSignature
	}
	payload, err := b64urlDecode(parts[0])
	if err != nil {
		return nil, ErrMalformedKey
	}
	var dataset keygenDataset
	if err := json.Unmarshal(payload, &dataset); err != nil {
		return nil, ErrMalformedKey
	}
	return &License{
		LicenseID: dataset.License.ID,
		ProductID: dataset.Product.ID,
		PolicyID:  dataset.Policy.ID,
		Created:   dataset.License.Created,
		Expiry:    dataset.License.Expiry,
	}, nil
}

// Activate verifies a key and, if valid and unexpired, makes it the active
// license for this process.
func Activate(key string) (*License, error) {
	l, err := Parse(key)
	if err != nil {
		return nil, err
	}
	if l.Expired() {
		return nil, ErrExpired
	}
	mu.Lock()
	current = l
	mu.Unlock()
	return l, nil
}

// Deactivate clears the active license (e.g. when the stored key is removed).
func Deactivate() {
	mu.Lock()
	current = nil
	mu.Unlock()
}

// Current returns the active license, or nil when running as community
// edition (no key, or the active key has expired).
func Current() *License {
	mu.RLock()
	defer mu.RUnlock()
	if current == nil || current.Expired() {
		return nil
	}
	return current
}

// IsEnterprise reports whether a valid, unexpired license is active.
func IsEnterprise() bool {
	return Current() != nil
}
