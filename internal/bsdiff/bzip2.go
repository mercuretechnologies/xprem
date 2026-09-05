package bsdiff

import (
	"bytes"

	"github.com/dsnet/compress/bzip2"
)

// compressBzip2 writes one bzip2 stream at level 9, like bsdiff.c does. The
// standard library only decompresses bzip2, hence the dependency.
func compressBzip2(data []byte) ([]byte, error) {
	var out bytes.Buffer
	w, err := bzip2.NewWriter(&out, &bzip2.WriterConfig{Level: bzip2.BestCompression})
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
