package bsdiff

import (
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"
)

// Every indexed window must be found again from its own position, whatever
// alignment the search expects.
func TestKgramIndexFindsEveryIndexedWindow(t *testing.T) {
	rng := rand.New(rand.NewSource(6))
	old := make([]byte, 20000)
	rng.Read(old)
	x := newKgramIndex(old)
	for p := 0; p+kgram <= len(old); p += step {
		want := min(64, len(old)-p)
		pos, length := x.search(old[p:p+want], -1)
		require.Equal(t, p, pos, "window at %d", p)
		require.Equal(t, want, length, "window at %d", p)
	}
}

// A block of old copied anywhere into new, at any alignment, is found in full
// as long as it spans at least one indexed window (kgram+step-1 bytes).
func TestSearchFindsUnalignedMatches(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	old := make([]byte, 50000)
	rng.Read(old)
	x := newKgramIndex(old)
	for _, size := range []int{kgram + step - 1, 30, 64, 200, 4096} {
		for trial := 0; trial < 50; trial++ {
			p := rng.Intn(len(old) - size)
			expect := rng.Intn(len(old))
			pos, length := x.search(old[p:p+size], expect)
			require.Equal(t, size, length, "size %d at %d", size, p)
			require.True(t, string(old[pos:pos+size]) == string(old[p:p+size]))
		}
	}
}

// When the current alignment already matches, it wins over an equally long
// match elsewhere: that is what keeps the control block short.
func TestSearchPrefersCurrentAlignment(t *testing.T) {
	block := make([]byte, 100)
	rand.New(rand.NewSource(8)).Read(block)
	old := concat(block, make([]byte, 1000), block)
	x := newKgramIndex(old)
	pos, length := x.search(block, 1100)
	require.Equal(t, 1100, pos)
	require.Equal(t, 100, length)
	pos, _ = x.search(block, 0)
	require.Equal(t, 0, pos)
}

func TestKgramIndexTinyInputs(t *testing.T) {
	for n := 0; n < kgram+step+1; n++ {
		old := make([]byte, n)
		for i := range old {
			old[i] = byte(i)
		}
		x := newKgramIndex(old)
		for _, new := range [][]byte{nil, old, old[n/2:], concat(old, old)} {
			pos, length := x.search(new, 0)
			require.LessOrEqual(t, length, min(len(old), len(new)))
			require.True(t, pos >= 0 && pos <= len(old))
			if length > 0 {
				require.Equal(t, old[pos:pos+length], new[:length])
			}
		}
	}
}

// matchlen compares eight bytes at a time; every prefix length and every
// alignment must agree with the byte-by-byte answer.
func TestMatchlen(t *testing.T) {
	rng := rand.New(rand.NewSource(9))
	naive := func(a, b []byte) int {
		n := min(len(a), len(b))
		for i := 0; i < n; i++ {
			if a[i] != b[i] {
				return i
			}
		}
		return n
	}
	for trial := 0; trial < 2000; trial++ {
		a := make([]byte, rng.Intn(70))
		rng.Read(a)
		b := append([]byte(nil), a...)
		if rng.Intn(4) > 0 && len(b) > 0 {
			b[rng.Intn(len(b))] ^= 1 + byte(rng.Intn(255))
		}
		if rng.Intn(2) == 0 {
			b = b[:rng.Intn(len(b)+1)]
		}
		require.Equal(t, naive(a, b), matchlen(a, b), "a=%x b=%x", a, b)
	}
}
