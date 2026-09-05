package bsdiff

import (
	"bytes"
	"fmt"
	"math/rand"
)

// scenario is an (old, new) pair a patch must rebuild exactly.
type scenario struct {
	name     string
	old, new []byte
}

// scenarios builds a deterministic battery of edits over random, bundle-like
// and real inputs: every edit kind a real update can produce, at sizes around
// the k-gram and sampling boundaries of the sparse index and well above them.
func scenarios(seed int64, v1, v2 []byte) []scenario {
	rng := rand.New(rand.NewSource(seed))
	random := func(n int) []byte {
		b := make([]byte, n)
		rng.Read(b)
		return b
	}

	var bases []scenario
	for _, n := range []int{0, 1, 7, 8, 15, 16, 17, 23, 24, 31, 100, 1000, 4096, 65536, 200000} {
		bases = append(bases, scenario{name: fmt.Sprintf("random-%d", n), old: random(n)})
	}
	for _, n := range []int{10000, 100000, 300000} {
		bases = append(bases, scenario{name: fmt.Sprintf("bundlelike-%d", n), old: bundleLike(rng, n)})
	}
	bases = append(bases, scenario{name: "v1.hbc", old: v1})

	var out []scenario
	for _, b := range bases {
		old := b.old
		for _, e := range edits(rng, old, random) {
			out = append(out, scenario{name: b.name + "/" + e.name, old: old, new: e.new})
		}
	}
	out = append(out, scenario{name: "v1.hbc/real-update", old: v1, new: v2})
	out = append(out, scenario{name: "v2.hbc/real-downgrade", old: v2, new: v1})
	return out
}

// bundleLike mimics a Hermes bundle: runs of zeros, small integers, repeated
// short patterns, identifiers, and some random bytes.
func bundleLike(rng *rand.Rand, n int) []byte {
	out := make([]byte, 0, n)
	words := []string{"greeting", "addition", "total", "formatPrice", "print", "length", "Hello ", " EUR"}
	for len(out) < n {
		switch rng.Intn(5) {
		case 0:
			out = append(out, make([]byte, 1+rng.Intn(64))...)
		case 1:
			for i := 0; i < 1+rng.Intn(32); i++ {
				out = append(out, byte(rng.Intn(16)), 0, 0, 0)
			}
		case 2:
			p := []byte{byte(rng.Intn(256)), byte(rng.Intn(256)), byte(rng.Intn(256))}
			out = append(out, bytes.Repeat(p, 1+rng.Intn(20))...)
		case 3:
			out = append(out, words[rng.Intn(len(words))]...)
		default:
			b := make([]byte, 1+rng.Intn(48))
			rng.Read(b)
			out = append(out, b...)
		}
	}
	return out[:n]
}

// edits applies every edit kind to old. Positions are chosen to stay valid
// for tiny inputs, so the same kinds run on a 7 byte input and a 300 KB one.
func edits(rng *rand.Rand, old []byte, random func(int) []byte) []scenario {
	n := len(old)
	at := func() int {
		if n == 0 {
			return 0
		}
		return rng.Intn(n)
	}
	span := func(pos, max int) int {
		if pos >= n {
			return 0
		}
		return min(1+rng.Intn(max), n-pos)
	}
	cut := func(pos, k int) []byte { return concat(old[:pos], old[pos+k:]) }

	var out []scenario
	add := func(name string, new []byte) { out = append(out, scenario{name: name, new: new}) }

	add("identical", old)
	add("empty", nil)
	add("unrelated", random(n))
	for _, k := range []int{1, 3, 8, 100, 5000} {
		p := at()
		add(fmt.Sprintf("insert-%d", k), concat(old[:p], random(k), old[p:]))
	}
	p := at()
	add("delete-small", cut(p, span(p, 8)))
	p = at()
	add("delete-large", cut(p, span(p, 5000)))
	p = at()
	k := span(p, 200)
	add("replace-same-length", concat(old[:p], random(k), old[p+k:]))
	add("replace-longer", concat(old[:p], random(k+37), old[p+k:]))
	add("replace-shorter", concat(old[:p], random(k/2), old[p+k:]))
	flipped := append([]byte(nil), old...)
	for i := 0; i < 50 && n > 0; i++ {
		flipped[rng.Intn(n)] ^= 0xFF
	}
	add("flip-50-bytes", flipped)
	p = at()
	k = span(p, 20000)
	moved := concat(cut(p, k), old[p:p+k])
	add("move-block-to-end", moved)
	add("duplicate-block", concat(old, old[p:p+k]))
	add("append", concat(old, random(1+rng.Intn(3000))))
	add("truncate", old[:n/2])
	add("prepend", concat(random(1+rng.Intn(3000)), old))
	add("doubled", concat(old, old))

	// Many small edits at once, like a real release touching several functions.
	multi := append([]byte(nil), old...)
	for i := 0; i < 5+rng.Intn(15); i++ {
		if len(multi) == 0 {
			break
		}
		q := rng.Intn(len(multi))
		switch rng.Intn(3) {
		case 0:
			multi = concat(multi[:q], random(1+rng.Intn(200)), multi[q:])
		case 1:
			multi = concat(multi[:q], multi[q+min(1+rng.Intn(200), len(multi)-q):])
		default:
			multi[q] ^= 0x55
		}
	}
	add("multi-edit", multi)
	return out
}
