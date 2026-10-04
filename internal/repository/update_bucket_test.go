package repository

import (
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/require"
)

// The asset mapping backfill must tell an interrupted read from a malformed
// file, whatever the bytes read before the failure.
func TestDecodeUpdateMetadataFilePreservesReadErrors(t *testing.T) {
	for _, metadata := range []string{`{"assetMapping":`, `{"assetMapping":{}}`} {
		t.Run(metadata, func(t *testing.T) {
			reader := io.MultiReader(strings.NewReader(metadata), iotest.ErrReader(io.ErrUnexpectedEOF))
			stored, err := decodeUpdateMetadataFile(reader)
			require.ErrorIs(t, err, io.ErrUnexpectedEOF)
			require.Nil(t, stored)
		})
	}
}
