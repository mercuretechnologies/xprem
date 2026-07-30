// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package apikeyrestrictions

import (
	"errors"
	"net/netip"
	"reflect"
	"testing"
)

func TestParseCidrsNormalizes(t *testing.T) {
	got, err := parseCidrs([]string{" 192.168.1.5/24 ", "10.1.2.3", "2001:db8::1", "192.168.1.0/24", ""})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 192.168.1.5/24 masks to 192.168.1.0/24 since postgres cidr rejects host bits.
	expected := []netip.Prefix{
		netip.MustParsePrefix("192.168.1.0/24"),
		netip.MustParsePrefix("10.1.2.3/32"),
		netip.MustParsePrefix("2001:db8::1/128"),
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("unexpected prefixes: %v", got)
	}
}

// Entries in IPv4-mapped IPv6 form (::ffff:x.y.z.w) are stored in plain IPv4 form.
func TestParseCidrsNormalizesMappedIpv4(t *testing.T) {
	got, err := parseCidrs([]string{
		"::ffff:10.1.2.3",        // bare mapped address becomes an IPv4 /32
		"10.1.2.3",               // duplicate of the above once unmapped
		"::ffff:192.168.1.5/120", // mapped range: 120-96 = /24 in IPv4, host bits masked off
		"::ffff:192.0.2.6/128",   // mapped single-host range becomes a /32
		"::ffff:0.0.0.0/96",      // the whole mapped block, i.e. every IPv4 address
		"2001:db8::/32",          // genuine IPv6 stays untouched
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := []netip.Prefix{
		netip.MustParsePrefix("10.1.2.3/32"),
		netip.MustParsePrefix("192.168.1.0/24"),
		netip.MustParsePrefix("192.0.2.6/32"),
		netip.MustParsePrefix("0.0.0.0/0"),
		netip.MustParsePrefix("2001:db8::/32"),
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("unexpected prefixes: %v", got)
	}
}

func TestParseCidrsRejectsInvalidEntries(t *testing.T) {
	for _, entry := range []string{
		"not-an-ip",
		"10.1.2.3/33",
		// Mapped prefixes wider than /96 have no IPv4 equivalent.
		"::ffff:0.0.0.0/95",
		"::ffff:10.1.2.3/64",
		"::ffff:0.0.0.0/0",
	} {
		if _, err := parseCidrs([]string{entry}); !errors.Is(err, ErrInvalidCidr) {
			t.Fatalf("entry %q: expected ErrInvalidCidr, got %v", entry, err)
		}
	}
}

// An empty allowlist parses to nil, never an empty slice.
func TestParseCidrsEmptyInputIsNil(t *testing.T) {
	got, err := parseCidrs([]string{"", "  "})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestIpAllowed(t *testing.T) {
	allowed := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("2001:db8::/32"),
	}
	cases := []struct {
		caller string
		want   bool
	}{
		{"10.1.2.3", true},        // inside the IPv4 range
		{"::ffff:10.1.2.3", true}, // mapped caller matches its IPv4 range once unmapped
		{"11.0.0.1", false},       // outside every range
		{"2001:db8::1", true},     // inside the genuine IPv6 range
		{"2001:db9::1", false},    // outside it
		{"::1", false},            // an IPv6 caller never matches an IPv4 prefix
	}
	for _, tc := range cases {
		if got := ipAllowed(netip.MustParseAddr(tc.caller), allowed); got != tc.want {
			t.Fatalf("caller %q: got %v, want %v", tc.caller, got, tc.want)
		}
	}
	// An unresolvable caller never passes an allowlist.
	if ipAllowed(netip.Addr{}, allowed) {
		t.Fatal("invalid address must not pass an allowlist")
	}
}
