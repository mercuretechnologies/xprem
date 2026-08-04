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
	// cost paid for ever rather than once. It is dropped rather than shortened: a
	// structured value cut to length lands mid-token and yields something that
	// still parses but no longer means what was sent. Callers size their own
	// value; this is the backstop, and it must not invent a smaller one.
	t.Run("drops an oversized value rather than cutting it", func(t *testing.T) {
		d := NewHeaderDictionary()
		d.Set("k", strings.Repeat("x", maxHeaderDictionaryValue+50))
		assert.Empty(t, d.Encode())
	})

	t.Run("keeps a value exactly on the bound", func(t *testing.T) {
		d := NewHeaderDictionary()
		d.Set("k", strings.Repeat("x", maxHeaderDictionaryValue))
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

// Branch names may legally contain non-ASCII (validation.Name screens control
// characters and path separators, not the byte range). An RFC 8941 string cannot
// carry those bytes, and a client that fails to parse the dictionary discards
// ALL of it — so one stray byte in any member would cost the device the surf
// verdicts too, and re-serve an update it already crashed on.
func TestHeaderDictionaryRefusesBytesAStructuredStringCannotCarry(t *testing.T) {
	for name, value := range map[string]string{
		"non-ascii":       "pr-é",
		"tab":             "pr\tx",
		"newline":         "pr\nx",
		"del":             "pr\x7fx",
		"invalid utf-8":   "pr\xffx",
		"high code point": "pr-🚀",
	} {
		t.Run(name, func(t *testing.T) {
			d := NewHeaderDictionary()
			d.Set("k", value)
			assert.Empty(t, d.Encode(), "the member must be dropped, not emitted unparseable")
		})
	}

	// The boundary itself stays usable.
	d := NewHeaderDictionary()
	d.Set("k", " ~")
	assert.Equal(t, `k=" ~"`, d.Encode())
}
