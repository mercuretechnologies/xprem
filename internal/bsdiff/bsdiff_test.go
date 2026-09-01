package bsdiff

import (
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var update = flag.Bool("update", false, "rewrite testdata/v1-to-v2.sparse.patch from the current Diff")

// expoBspatch is the bspatch.c that expo-updates runs on the phone, compiled
// once for the whole test binary. Empty when no C toolchain or libbz2 is around.
var (
	expoBspatch     string
	expoBspatchNote string
)

func TestMain(m *testing.M) {
	flag.Parse()
	dir, err := os.MkdirTemp("", "expo-bspatch")
	if err != nil {
		panic(err)
	}
	src := filepath.Join("testdata", "expo-bspatch")
	bin := filepath.Join(dir, "bspatch")
	out, err := exec.Command("cc", "-O1", "-o", bin, filepath.Join(src, "main.c"), filepath.Join(src, "bspatch.c"), "-lbz2").CombinedOutput()
	if err == nil {
		expoBspatch = bin
	} else {
		expoBspatchNote = fmt.Sprintf("cannot build expo bspatch.c: %v\n%s", err, out)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// matchers pairs the production matcher with the bsdiff 4.3 oracle.
var matchers = []struct {
	name string
	diff func(old, new []byte) ([]byte, error)
}{
	{"sparse", Diff},
	{"reference", DiffReference},
}

// fixture returns two Hermes bundles and the patch bsdiff 4.3 (the C
// reference) produced for them.
func fixture(t testing.TB) (old, new, patch []byte) {
	t.Helper()
	return readTestdata(t, "v1.hbc"), readTestdata(t, "v2.hbc"), readTestdata(t, "v1-to-v2.patch")
}

func readTestdata(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return b
}

func requireTool(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s not on PATH", name)
	}
	return path
}

func requireExpoBspatch(t *testing.T) string {
	t.Helper()
	if expoBspatch == "" {
		t.Skip(expoBspatchNote)
	}
	return expoBspatch
}

// runBspatch applies patch to old with an external bspatch binary.
func runBspatch(t *testing.T, bin string, old, patch []byte) []byte {
	t.Helper()
	dir := t.TempDir()
	oldPath, newPath, patchPath := filepath.Join(dir, "old"), filepath.Join(dir, "new"), filepath.Join(dir, "patch")
	require.NoError(t, os.WriteFile(oldPath, old, 0o600))
	require.NoError(t, os.WriteFile(patchPath, patch, 0o600))
	out, err := exec.Command(bin, oldPath, newPath, patchPath).CombinedOutput()
	require.NoError(t, err, "%s: %s", bin, out)
	got, err := os.ReadFile(newPath)
	require.NoError(t, err)
	return got
}

// blocks decompresses the three streams of a patch.
func blocks(t testing.TB, patch []byte) (ctrl, diff, extra []byte) {
	t.Helper()
	require.GreaterOrEqual(t, len(patch), headerSize)
	ctrlLen, diffLen := offtin(patch[8:16]), offtin(patch[16:24])
	inflate := func(b []byte) []byte {
		out, err := io.ReadAll(bzip2.NewReader(bytes.NewReader(b)))
		require.NoError(t, err)
		return out
	}
	diffStart := headerSize + ctrlLen
	extraStart := diffStart + diffLen
	return inflate(patch[headerSize:diffStart]), inflate(patch[diffStart:extraStart]), inflate(patch[extraStart:])
}

func requireSameBlocks(t testing.TB, want, got []byte) {
	t.Helper()
	wc, wd, we := blocks(t, want)
	gc, gd, ge := blocks(t, got)
	require.True(t, bytes.Equal(wc, gc), "control block differs")
	require.True(t, bytes.Equal(wd, gd), "diff block differs")
	require.True(t, bytes.Equal(we, ge), "extra block differs")
	require.Equal(t, want[24:32], got[24:32], "new size differs")
}

// requireSizeWithinReference is the contract of the sparse matcher: it can
// miss matches shorter than kgram+step-1 bytes, which costs a few extra
// control triples on tiny inputs (a few dozen bytes), never a share of the file.
func requireSizeWithinReference(t testing.TB, sparse, reference int) {
	t.Helper()
	require.LessOrEqual(t, sparse, reference+64+reference/50,
		"sparse patch %d bytes is materially larger than the reference %d bytes", sparse, reference)
}

func concat(parts ...[]byte) []byte {
	return bytes.Join(parts, nil)
}

func roundTrip(t *testing.T, diff func(old, new []byte) ([]byte, error), old, new []byte) []byte {
	t.Helper()
	patch, err := diff(old, new)
	require.NoError(t, err)
	require.Equal(t, magic, string(patch[:8]))
	require.Equal(t, int64(len(new)), offtin(patch[24:32]))
	got, err := Patch(old, patch)
	require.NoError(t, err)
	require.True(t, bytes.Equal(new, got), "patched output differs from new")
	return patch
}

// Every scenario, both matchers: the patch rebuilds new, and the sparse patch
// is not materially larger than the exhaustive one.
func TestScenarios(t *testing.T) {
	v1, v2, _ := fixture(t)
	for _, sc := range scenarios(1, v1, v2) {
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			sparse := roundTrip(t, Diff, sc.old, sc.new)
			reference := roundTrip(t, DiffReference, sc.old, sc.new)
			requireSizeWithinReference(t, len(sparse), len(reference))
		})
	}
}

// Every scenario's sparse patch, applied by the bspatch.c expo-updates ships.
func TestExpoBspatchAppliesOurPatches(t *testing.T) {
	bin := requireExpoBspatch(t)
	v1, v2, _ := fixture(t)
	for _, sc := range scenarios(2, v1, v2) {
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			patch, err := Diff(sc.old, sc.new)
			require.NoError(t, err)
			require.True(t, bytes.Equal(sc.new, runBspatch(t, bin, sc.old, patch)))
		})
	}
}

// Same battery through the system bspatch when one is installed.
func TestSystemBspatchAppliesOurPatches(t *testing.T) {
	bin := requireTool(t, "bspatch")
	v1, v2, _ := fixture(t)
	for _, sc := range scenarios(3, v1, v2) {
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			for _, m := range matchers {
				patch, err := m.diff(sc.old, sc.new)
				require.NoError(t, err)
				require.True(t, bytes.Equal(sc.new, runBspatch(t, bin, sc.old, patch)), m.name)
			}
		})
	}
}

// The oracle must be bsdiff 4.3 exactly: same control, diff and extra blocks.
func TestReferenceMatchesC(t *testing.T) {
	old, new, ref := fixture(t)
	patch, err := DiffReference(old, new)
	require.NoError(t, err)
	requireSameBlocks(t, ref, patch)
}

// The sparse matcher's output on the fixture is pinned: any change to the
// index constants or the search shows up here, on purpose.
func TestSparseGolden(t *testing.T) {
	old, new, ref := fixture(t)
	patch, err := Diff(old, new)
	require.NoError(t, err)
	golden := filepath.Join("testdata", "v1-to-v2.sparse.patch")
	if *update {
		require.NoError(t, os.WriteFile(golden, patch, 0o644))
	}
	requireSameBlocks(t, readTestdata(t, "v1-to-v2.sparse.patch"), patch)
	requireSizeWithinReference(t, len(patch), len(ref))
}

func TestPatchAppliesReferencePatch(t *testing.T) {
	old, new, ref := fixture(t)
	got, err := Patch(old, ref)
	require.NoError(t, err)
	require.Equal(t, new, got)
}

// Patches made by the C bsdiff on arbitrary inputs must be accepted by Patch.
func TestPatchAppliesSystemBsdiffPatches(t *testing.T) {
	bsdiff := requireTool(t, "bsdiff")
	rng := rand.New(rand.NewSource(4))
	dir := t.TempDir()
	for i := 0; i < 8; i++ {
		old := make([]byte, 1000+rng.Intn(50000))
		rng.Read(old)
		new := concat(old[:len(old)/3], []byte("changed"), old[len(old)/3+rng.Intn(100):])

		oldPath, newPath, patchPath := filepath.Join(dir, "old"), filepath.Join(dir, "new"), filepath.Join(dir, "patch")
		require.NoError(t, os.WriteFile(oldPath, old, 0o600))
		require.NoError(t, os.WriteFile(newPath, new, 0o600))
		out, err := exec.Command(bsdiff, oldPath, newPath, patchPath).CombinedOutput()
		require.NoError(t, err, string(out))
		patch, err := os.ReadFile(patchPath)
		require.NoError(t, err)

		got, err := Patch(old, patch)
		require.NoError(t, err)
		require.Equal(t, new, got)
	}
}

// Inputs that make the exhaustive matcher quadratic must stay cheap for the
// sparse one, and still round-trip.
func TestPathologicalInputs(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	period := func(p, n int) []byte {
		b := make([]byte, p)
		rng.Read(b)
		return bytes.Repeat(b, n/p+1)[:n]
	}
	zeros := make([]byte, 1<<20)
	sparseZeros := append([]byte(nil), zeros...)
	for i := 100000; i < len(sparseZeros); i += 100000 {
		sparseZeros[i] = 1
	}
	p17 := period(17, 1<<20)
	p1000 := period(1000, 1<<20)
	cases := map[string]struct{ old, new []byte }{
		"zeros vs zeros":          {zeros, zeros},
		"zeros vs sparse flips":   {zeros, sparseZeros},
		"period 17 vs shifted":    {p17, p17[1:]},
		"period 17 vs itself":     {p17, p17},
		"period 1000 vs edited":   {p1000, concat(p1000[:500000], []byte("edit"), p1000[500000:])},
		"alternating vs inverted": {period(2, 1<<19), bytes.ToUpper(period(2, 1<<19))},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			patch := roundTrip(t, Diff, tc.old, tc.new)
			elapsed := time.Since(start)
			require.Less(t, elapsed, 10*time.Second, "sparse matcher took %v", elapsed)
			t.Logf("%d bytes in %v", len(patch), elapsed)
		})
	}
}

// Same bytes in, same patch out, from any number of goroutines at once.
func TestDeterministicAndConcurrent(t *testing.T) {
	old, new, _ := fixture(t)
	for _, m := range matchers {
		want, err := m.diff(old, new)
		require.NoError(t, err)
		results := make([][]byte, 8)
		var wg sync.WaitGroup
		for i := range results {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i], _ = m.diff(old, new)
			}()
		}
		wg.Wait()
		for i, got := range results {
			require.Equal(t, want, got, "%s run %d", m.name, i)
		}
	}
}

func TestDiffRejectsOversizedInput(t *testing.T) {
	for _, m := range matchers {
		_, err := m.diff(make([]byte, maxSize+1), nil)
		require.ErrorIs(t, err, errTooLarge, m.name)
	}
}

func TestOfftRoundTrip(t *testing.T) {
	var buf [8]byte
	for _, x := range []int64{0, 1, -1, 255, 256, -256, 1 << 40, -(1 << 40), 1<<63 - 1, -(1<<63 - 1)} {
		offtout(x, buf[:])
		require.Equal(t, x, offtin(buf[:]), "value %d", x)
	}
	require.Equal(t, int64(-389), offtin([]byte{0x85, 0x01, 0, 0, 0, 0, 0, 0x80}))
}

// Real bundles are too large to commit. Point BSDIFF_LARGE_DIR at a directory
// holding old.hbc, new.hbc and optionally ref.patch (made by the C bsdiff).
func TestLargeBundles(t *testing.T) {
	dir := os.Getenv("BSDIFF_LARGE_DIR")
	if dir == "" {
		t.Skip("BSDIFF_LARGE_DIR not set")
	}
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err)
		return b
	}
	old, new := read("old.hbc"), read("new.hbc")

	sizes := map[string]int{}
	for _, m := range matchers {
		start := time.Now()
		patch := roundTrip(t, m.diff, old, new)
		t.Logf("%s: %d bytes in %v", m.name, len(patch), time.Since(start))
		sizes[m.name] = len(patch)
		if expoBspatch != "" {
			require.True(t, bytes.Equal(new, runBspatch(t, expoBspatch, old, patch)), "expo bspatch, %s", m.name)
		}
		if bin, err := exec.LookPath("bspatch"); err == nil {
			require.True(t, bytes.Equal(new, runBspatch(t, bin, old, patch)), "system bspatch, %s", m.name)
		}
	}
	requireSizeWithinReference(t, sizes["sparse"], sizes["reference"])
	if ref, err := os.ReadFile(filepath.Join(dir, "ref.patch")); err == nil {
		patch, err := DiffReference(old, new)
		require.NoError(t, err)
		requireSameBlocks(t, ref, patch)
	}
}

// expoFixtures returns the 1.7 MB Hermes bundle pair and the patch from
// expo-updates' own test suite.
func expoFixtures(t testing.TB) (old, new, patch []byte) {
	t.Helper()
	gunzip := func(name string) []byte {
		r, err := gzip.NewReader(bytes.NewReader(readTestdata(t, filepath.Join("expo-fixtures", name))))
		require.NoError(t, err)
		b, err := io.ReadAll(r)
		require.NoError(t, err)
		return b
	}
	return gunzip("old.hbc.gz"), gunzip("new.hbc.gz"), readTestdata(t, filepath.Join("expo-fixtures", "test.patch"))
}

// The patch expo-updates tests its phone-side bspatch with must be accepted by
// Patch, and our patches for the same bundles must be accepted by that bspatch.
func TestExpoFixtures(t *testing.T) {
	old, new, theirs := expoFixtures(t)
	got, err := Patch(old, theirs)
	require.NoError(t, err)
	require.True(t, bytes.Equal(new, got), "expo's test.patch does not rebuild their new.hbc")

	sizes := map[string]int{}
	for _, m := range matchers {
		start := time.Now()
		patch := roundTrip(t, m.diff, old, new)
		t.Logf("%s: %d bytes in %v (expo's own patch: %d bytes)", m.name, len(patch), time.Since(start), len(theirs))
		sizes[m.name] = len(patch)
		if expoBspatch != "" {
			require.True(t, bytes.Equal(new, runBspatch(t, expoBspatch, old, patch)), "expo bspatch, %s", m.name)
		}
		if bin, err := exec.LookPath("bspatch"); err == nil {
			require.True(t, bytes.Equal(new, runBspatch(t, bin, old, patch)), "system bspatch, %s", m.name)
		}
	}
	requireSizeWithinReference(t, sizes["sparse"], sizes["reference"])
	requireSizeWithinReference(t, sizes["sparse"], len(theirs))
}
