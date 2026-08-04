package services

import (
	"sort"
	"strings"
)

// maxHeaderDictionaryValue bounds one member.
const maxHeaderDictionaryValue = 512

// HeaderDictionary accumulates the members of an RFC 8941 dictionary header.
// Setting a key that is already present replaces it. Held by the caller across a
// whole response so several features can contribute members to one header.
type HeaderDictionary struct {
	members map[string]string
}

func NewHeaderDictionary() *HeaderDictionary {
	return &HeaderDictionary{members: map[string]string{}}
}

// Set stores a member, dropping one whose value is over the bound rather than
// cutting it. A structured value is not a string that can be shortened: cutting
// lands mid-token or mid-escape and produces something that parses but means
// something else. Callers assemble a value that fits; this is the backstop.
func (d *HeaderDictionary) Set(key string, value string) {
	if key == "" || value == "" || len(value) > maxHeaderDictionaryValue {
		return
	}
	// An RFC 8941 string carries 0x20-0x7E and nothing else. Escaping handles the
	// two characters that end a string early; it cannot rescue a byte the grammar
	// has no room for. A client that fails to parse the dictionary drops ALL of
	// it, so one stray byte in any member costs every other member too — today
	// that would mean re-serving an update this device already crashed on.
	if !isStructuredStringSafe(value) {
		return
	}
	d.members[key] = value
}

func isStructuredStringSafe(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] < 0x20 || value[i] > 0x7e {
			return false
		}
	}
	return true
}

// Encode renders the dictionary with keys sorted, so the same state always
// produces the same bytes. Empty when there is nothing to carry.
func (d *HeaderDictionary) Encode() string {
	if len(d.members) == 0 {
		return ""
	}
	keys := make([]string, 0, len(d.members))
	for key := range d.members {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+`="`+escapeStructuredString(d.members[key])+`"`)
	}
	return strings.Join(parts, ", ")
}

// escapeStructuredString escapes the two characters an RFC 8941 string cannot
// carry raw. Callers pass branch names, which validation.Name does not screen
// for quotes.
func escapeStructuredString(value string) string {
	if !strings.ContainsAny(value, `"\`) {
		return value
	}
	var escaped strings.Builder
	escaped.Grow(len(value) + 8)
	for _, r := range value {
		if r == '"' || r == '\\' {
			escaped.WriteByte('\\')
		}
		escaped.WriteRune(r)
	}
	return escaped.String()
}
