package android

import (
	"encoding/binary"
	"testing"
	"xprem/internal/android/androidtest"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/cryptobyte"
)

func TestJKSStructureRejectsTruncationAndOversizedLengths(t *testing.T) {
	valid := androidtest.JKSKeystore("store-pass", "key-pass", "upload")
	require.True(t, validJKSStructure(valid))
	for end := 0; end < len(valid); end++ {
		require.False(t, validJKSStructure(valid[:end]), "truncated at %d", end)
	}
	keyLengthOffset := 12 + 4 + 2 + len("upload") + 8
	chainCountOffset := keyLengthOffset + 4 + int(binary.BigEndian.Uint32(valid[keyLengthOffset:]))
	certTypeOffset := chainCountOffset + 4
	certLengthOffset := certTypeOffset + 2 + int(binary.BigEndian.Uint16(valid[certTypeOffset:]))
	for _, offset := range []int{8, keyLengthOffset, chainCountOffset, certLengthOffset} {
		malformed := append([]byte(nil), valid...)
		binary.BigEndian.PutUint32(malformed[offset:], ^uint32(0))
		require.False(t, validJKSStructure(malformed), "oversized field at %d", offset)
	}
}

func TestJKSKeyEnvelopeRejectsMissingSaltAndChecksum(t *testing.T) {
	// DER EncryptedPrivateKeyInfo with an empty algorithm sequence and a
	// one-byte encrypted body. The dependency would allocate a negative size.
	require.False(t, validJKSKeyEnvelope(cryptobyte.String{0x30, 5, 0x30, 0, 0x04, 1, 0}))
}

func FuzzJKSStructure(f *testing.F) {
	f.Add(androidtest.JKSKeystore("store-pass", "key-pass", "upload"))
	f.Add([]byte{0xfe, 0xed, 0xfe, 0xed})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 512<<10 {
			return
		}
		if validJKSStructure(data) {
			// Structurally accepted inputs must also be safe for the decoder.
			_ = validateJKS(data, "store-pass", "key-pass", "upload")
		}
	})
}
