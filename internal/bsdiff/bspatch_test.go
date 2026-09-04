package bsdiff

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

// buildPatch assembles a patch from raw (uncompressed) blocks.
func buildPatch(t *testing.T, triples [][3]int64, diff, extra []byte, newSize int64) []byte {
	t.Helper()
	var ctrl bytes.Buffer
	var buf [8]byte
	for _, tr := range triples {
		for _, v := range tr {
			offtout(v, buf[:])
			ctrl.Write(buf[:])
		}
	}
	compress := func(b []byte) []byte {
		out, err := compressBzip2(b)
		require.NoError(t, err)
		return out
	}
	ctrlBz, diffBz, extraBz := compress(ctrl.Bytes()), compress(diff), compress(extra)
	patch := []byte(magic)
	var header [24]byte
	offtout(int64(len(ctrlBz)), header[0:8])
	offtout(int64(len(diffBz)), header[8:16])
	offtout(newSize, header[16:24])
	patch = append(patch, header[:]...)
	patch = append(patch, ctrlBz...)
	patch = append(patch, diffBz...)
	return append(patch, extraBz...)
}

func TestPatchRejectsCorruptHeaders(t *testing.T) {
	old, _, ref := fixture(t)
	withHeader := func(offset int, b byte) []byte {
		p := append([]byte(nil), ref...)
		p[offset] = b
		return p
	}
	cases := map[string][]byte{
		"empty":                     nil,
		"shorter than header":       ref[:31],
		"bad magic":                 withHeader(0, 'X'),
		"negative ctrl length":      withHeader(15, 0x80),
		"negative diff length":      withHeader(23, 0x80),
		"negative new size":         withHeader(31, 0x80),
		"ctrl length beyond patch":  withHeader(10, 0xFF),
		"diff length beyond patch":  withHeader(18, 0xFF),
		"new size above limit":      withHeader(28, 0x7F),
		"truncated inside streams":  ref[:len(ref)-20],
		"new size larger than data": withHeader(25, 0x10),
	}
	for name, patch := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := Patch(old, patch)
			require.ErrorIs(t, err, ErrCorruptPatch)
			require.Nil(t, got)
		})
	}
}

func TestPatchRejectsCorruptControlBlocks(t *testing.T) {
	old := []byte("0123456789")
	cases := map[string]struct {
		triples [][3]int64
		newSize int64
	}{
		"copy past new size":   {[][3]int64{{20, 0, 0}}, 10},
		"insert past new size": {[][3]int64{{0, 20, 0}}, 10},
		"negative copy":        {[][3]int64{{-1, 0, 0}}, 10},
		"negative insert":      {[][3]int64{{0, -1, 0}}, 10},
		"seek before old":      {[][3]int64{{5, 0, -6}, {5, 0, 0}}, 10},
		"seek past old":        {[][3]int64{{5, 0, 6}, {5, 0, 0}}, 10},
		"ctrl block too short": {[][3]int64{{5, 0, 0}}, 10},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			patch := buildPatch(t, tc.triples, make([]byte, 20), make([]byte, 20), tc.newSize)
			got, err := Patch(old, patch)
			require.ErrorIs(t, err, ErrCorruptPatch)
			require.Nil(t, got)
		})
	}
}

func TestPatchHandcraftedControlBlock(t *testing.T) {
	old := []byte("hello world")
	// Copy "hello" as is, insert "!!", skip " ", then copy "world" with +1 on the first byte.
	patch := buildPatch(t,
		[][3]int64{{5, 2, 1}, {5, 0, 0}},
		concat(make([]byte, 5), []byte{1, 0, 0, 0, 0}),
		[]byte("!!"),
		12)
	got, err := Patch(old, patch)
	require.NoError(t, err)
	require.Equal(t, "hello!!xorld", string(got))
}

// bspatch cannot know it was given the wrong base: the output is garbage, not
// an error. This is why callers must check the hash of the result.
func TestPatchOnWrongBaseIsSilent(t *testing.T) {
	old, new, ref := fixture(t)
	wrong := append([]byte(nil), old...)
	wrong[500] ^= 0xFF
	got, err := Patch(wrong, ref)
	require.NoError(t, err)
	require.Len(t, got, len(new))
	require.NotEqual(t, new, got)
}

// Any pair of inputs must round-trip through both matchers, and the sparse
// patch must stay within the size contract.
func FuzzRoundTrip(f *testing.F) {
	old, new, _ := fixture(f)
	f.Add(old, new)
	f.Add([]byte("hello world"), []byte("hello brave new world"))
	f.Add([]byte{}, []byte{0})
	f.Add(bytes.Repeat([]byte{0}, 100), bytes.Repeat([]byte{0}, 101))
	f.Fuzz(func(t *testing.T, old, new []byte) {
		sparse, err := Diff(old, new)
		require.NoError(t, err)
		got, err := Patch(old, sparse)
		require.NoError(t, err)
		require.True(t, bytes.Equal(new, got))
		reference, err := DiffReference(old, new)
		require.NoError(t, err)
		got, err = Patch(old, reference)
		require.NoError(t, err)
		require.True(t, bytes.Equal(new, got))
		requireSizeWithinReference(t, len(sparse), len(reference))
	})
}

func FuzzPatch(f *testing.F) {
	old, new, ref := fixture(f)
	f.Add(ref)
	f.Add(ref[:headerSize])
	f.Add([]byte(magic))
	f.Fuzz(func(t *testing.T, patch []byte) {
		got, err := Patch(old, patch)
		if bytes.Equal(patch, ref) {
			require.NoError(t, err)
			require.Equal(t, new, got)
		}
		if err != nil {
			require.Nil(t, got)
		}
	})
}
