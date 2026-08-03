package services

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Production carries a single member today, so everything below is the part of
// this type that only the next caller will exercise.
func TestHeaderDictionaryEncode(t *testing.T) {
	t.Run("empty carries nothing", func(t *testing.T) {
		assert.Empty(t, NewHeaderDictionary().Encode())
	})

	t.Run("one member", func(t *testing.T) {
		d := NewHeaderDictionary()
		d.Set("xprem-surf-blocked", "pr-482@200")
		assert.Equal(t, `xprem-surf-blocked="pr-482@200"`, d.Encode())
	})

	// Sorted, so the same state always produces the same bytes: an unsorted map
	// walk would make the header flap between polls and defeat any caching or
	// diffing downstream.
	t.Run("members are sorted, not map-ordered", func(t *testing.T) {
		d := NewHeaderDictionary()
		d.Set("zulu", "3")
		d.Set("alpha", "1")
		d.Set("mike", "2")
		for i := 0; i < 20; i++ {
			assert.Equal(t, `alpha="1", mike="2", zulu="3"`, d.Encode())
		}
	})
}

func TestHeaderDictionarySet(t *testing.T) {
	t.Run("replaces an existing key", func(t *testing.T) {
		d := NewHeaderDictionary()
		d.Set("k", "first")
		d.Set("k", "second")
		assert.Equal(t, `k="second"`, d.Encode())
	})

	t.Run("ignores an empty key or value", func(t *testing.T) {
		d := NewHeaderDictionary()
		d.Set("", "value")
		d.Set("k", "")
		assert.Empty(t, d.Encode())
	})

	// The client replays every member on every poll, so an oversized value is a
	// cost paid for ever rather than once.
	t.Run("truncates an oversized value", func(t *testing.T) {
		d := NewHeaderDictionary()
		d.Set("k", strings.Repeat("x", maxHeaderDictionaryValue+50))
		assert.Equal(t, `k="`+strings.Repeat("x", maxHeaderDictionaryValue)+`"`, d.Encode())
	})
}

// Unescaped, either character ends the string early and the client parses a
// different dictionary than the one sent — silently losing the other members.
func TestHeaderDictionaryEscapesWhatBreaksTheDictionary(t *testing.T) {
	cases := map[string]struct{ value, want string }{
		"quote":     {`pr-"x`, `k="pr-\"x"`},
		"backslash": {`pr-\x`, `k="pr-\\x"`},
		"both":      {`a"b\c`, `k="a\"b\\c"`},
		"untouched": {"pr-482@200", `k="pr-482@200"`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			d := NewHeaderDictionary()
			d.Set("k", tc.value)
			assert.Equal(t, tc.want, d.Encode())
		})
	}
}
