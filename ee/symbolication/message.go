// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package symbolication

import (
	"crypto/sha256"
	"encoding/binary"
	"regexp"

	"github.com/google/uuid"
)

// The parts of a message that change from one occurrence of an error to the
// next, and what each becomes. Order matters: a UUID holds numbers.
var messageVariables = []struct {
	pattern     *regexp.Regexp
	placeholder string
}{
	{regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`), "<uuid>"},
	{regexp.MustCompile(`https?://\S+`), "<url>"},
	{regexp.MustCompile(`0x[0-9a-fA-F]+`), "<hex>"},
	{regexp.MustCompile(`'[^']*'|"[^"]*"`), "<str>"},
	{regexp.MustCompile(`\d+`), "<num>"},
}

// NormalizeMessage replaces the values inside a message, so "User 42 not
// found" and "User 7 not found" read as the same error.
func NormalizeMessage(message string) string {
	for _, variable := range messageVariables {
		message = variable.pattern.ReplaceAllString(message, variable.placeholder)
	}
	return message
}

// Fingerprint hashes parts into a UUID. Each part is length-prefixed, so two
// adjacent parts cannot shift into the same hash.
func Fingerprint(parts ...string) uuid.UUID {
	h := sha256.New()
	var length [8]byte
	for _, part := range parts {
		binary.LittleEndian.PutUint64(length[:], uint64(len(part)))
		_, _ = h.Write(length[:])
		_, _ = h.Write([]byte(part))
	}
	var key uuid.UUID
	copy(key[:], h.Sum(nil)[:16])
	return key
}
