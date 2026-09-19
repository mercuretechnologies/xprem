package services

import (
	"archive/tar"
	"bytes"
	"strings"
	"testing"
	"xprem/internal/types"

	"github.com/stretchr/testify/require"
)

func cacheArchive(t *testing.T, headers ...tar.Header) []byte {
	t.Helper()
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	for _, header := range headers {
		require.NoError(t, writer.WriteHeader(&header))
		if header.Typeflag == tar.TypeReg {
			_, err := writer.Write(bytes.Repeat([]byte("a"), int(header.Size)))
			require.NoError(t, err)
		}
	}
	require.NoError(t, writer.Close())
	return archive.Bytes()
}

func TestBuildCacheArchiveRejectsOversizedEntry(t *testing.T) {
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	require.NoError(t, writer.WriteHeader(&tar.Header{Name: "entry", Typeflag: tar.TypeReg, Size: types.MaxBuildCacheObjectBytes + 1}))
	require.NoError(t, archive.WriteByte('a'))
	require.ErrorIs(t, validateBuildCacheArchive(&archive), ErrBuildCacheArchive)
	require.Equal(t, 1, archive.Len(), "oversized entries must be rejected before reading their contents")
}

func TestBuildCacheArchive(t *testing.T) {
	valid := cacheArchive(t,
		tar.Header{Name: "./", Typeflag: tar.TypeDir, Mode: 0755},
		tar.Header{Name: "./cache-entry", Typeflag: tar.TypeReg, Size: 12, Mode: 0644},
		tar.Header{Name: "./" + strings.Repeat("long/", 30) + "entry", Typeflag: tar.TypeReg, Size: 4, Format: tar.FormatPAX},
	)
	t.Run("portable tar with extended path", func(t *testing.T) {
		require.NoError(t, validateBuildCacheArchive(bytes.NewReader(valid)))
	})
	t.Run("tar block padding", func(t *testing.T) {
		padded := append(bytes.Clone(valid), make([]byte, 10240)...)
		require.NoError(t, validateBuildCacheArchive(bytes.NewReader(padded)))
	})
	for _, name := range []string{"../outside", "cache/../../outside", "/tmp/outside", `cache\outside`, "C:/outside"} {
		t.Run(name, func(t *testing.T) {
			archive := cacheArchive(t, tar.Header{Name: name, Typeflag: tar.TypeReg, Size: 1})
			require.ErrorIs(t, validateBuildCacheArchive(bytes.NewReader(archive)), ErrBuildCacheArchive)
		})
	}
	for name, kind := range map[string]byte{
		"symbolic link":    tar.TypeSymlink,
		"hard link":        tar.TypeLink,
		"named pipe":       tar.TypeFifo,
		"character device": tar.TypeChar,
		"block device":     tar.TypeBlock,
	} {
		t.Run(name, func(t *testing.T) {
			archive := cacheArchive(t, tar.Header{Name: "cache", Typeflag: kind, Linkname: "../outside"})
			require.ErrorIs(t, validateBuildCacheArchive(bytes.NewReader(archive)), ErrBuildCacheArchive)
		})
	}
	for name, archive := range map[string][]byte{
		"arbitrary bytes":       bytes.Repeat([]byte("a"), 2048),
		"truncated header":      valid[:200],
		"truncated file":        valid[:1028],
		"appended payload":      append(bytes.Clone(valid), []byte("not part of the archive")...),
		"concatenated archives": append(bytes.Clone(valid), valid...),
	} {
		t.Run(name, func(t *testing.T) {
			require.ErrorIs(t, validateBuildCacheArchive(bytes.NewReader(archive)), ErrBuildCacheArchive)
		})
	}
}

func TestBuildCacheArchiveEntryLimit(t *testing.T) {
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	for range 100001 {
		require.NoError(t, writer.WriteHeader(&tar.Header{Name: "entry", Typeflag: tar.TypeReg}))
	}
	require.NoError(t, writer.Close())
	require.ErrorIs(t, validateBuildCacheArchive(&archive), ErrBuildCacheArchive)
}
