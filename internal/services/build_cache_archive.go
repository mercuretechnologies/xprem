package services

import (
	"archive/tar"
	"errors"
	"io"
	"path"
	"regexp"
	"strings"
	"xprem/internal/types"
)

var ErrBuildCacheArchive = errors.New("cache must be a TAR archive containing only regular files and directories")
var buildCacheArchiveKey = regexp.MustCompile(`^archive-v1-[a-f0-9]{64}$`)

// Gradle and ccache snapshots are uncompressed TARs. Inspect them without extracting anything
// on the server; the CLI performs the same path and file-type checks on restore.
func validateBuildCacheArchive(reader io.Reader) error {
	archive := tar.NewReader(reader)
	var size int64
	for entries := 0; ; entries++ {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil || entries == 100000 {
			return ErrBuildCacheArchive
		}
		name := header.Name
		if name == "" || path.IsAbs(name) || strings.ContainsAny(name, `\:`) {
			return ErrBuildCacheArchive
		}
		for _, part := range strings.Split(name, "/") {
			if part == ".." {
				return ErrBuildCacheArchive
			}
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeDir {
			return ErrBuildCacheArchive
		}
		for key := range header.PAXRecords {
			if strings.HasPrefix(key, "GNU.sparse.") {
				return ErrBuildCacheArchive
			}
		}
		if header.Size > types.MaxBuildCacheObjectBytes-size || (header.Typeflag == tar.TypeDir && header.Size != 0) {
			return ErrBuildCacheArchive
		}
		size += header.Size
		if _, err := io.Copy(io.Discard, archive); err != nil {
			return ErrBuildCacheArchive
		}
	}
	// TAR permits trailing zero padding, but not another archive or arbitrary data.
	var padding [4096]byte
	for {
		n, err := reader.Read(padding[:])
		for _, value := range padding[:n] {
			if value != 0 {
				return ErrBuildCacheArchive
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return ErrBuildCacheArchive
		}
	}
}
