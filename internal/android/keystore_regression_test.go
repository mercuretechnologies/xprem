package android

import (
	"bytes"
	"encoding/binary"
	keystore "github.com/pavlo-v-chernykh/keystore-go/v4"
	"github.com/stretchr/testify/require"
	"runtime"
	"testing"
	"time"
	"xprem/internal/android/androidtest"
)

func TestUnnamedPKCS12RejectsAliasUnknownToJava(t *testing.T) {
	blob := androidtest.PKCS12Keystore("store-pass")
	if err := ValidateKeystore(blob, "store-pass", "store-pass", "any-alias"); err == nil {
		t.Error("vault accepts an alias that Java cannot use")
	}
}

func TestJKSRejectsNonSigningEntry(t *testing.T) {
	ks := keystore.New()
	require.NoError(t, ks.SetPrivateKeyEntry("upload", keystore.PrivateKeyEntry{CreationTime: time.Now(), PrivateKey: []byte("not-a-private-key")}, []byte("key-pass")))
	var encoded bytes.Buffer
	require.NoError(t, ks.Store(&encoded, []byte("store-pass")))
	if err := ValidateKeystore(encoded.Bytes(), "store-pass", "key-pass", "upload"); err == nil {
		t.Error("vault accepts invalid private-key bytes with no certificate chain")
	}
}

func TestJKSAllocationBoundedByUploadSize(t *testing.T) {
	// Deliberately bounded at 8 MiB; never exercise an OOM on the host.
	var encoded bytes.Buffer
	for _, value := range []uint32{0xFEEDFEED, 2, 1, 1} {
		require.NoError(t, binary.Write(&encoded, binary.BigEndian, value))
	}
	require.NoError(t, binary.Write(&encoded, binary.BigEndian, uint16(1)))
	encoded.WriteByte('a')
	require.NoError(t, binary.Write(&encoded, binary.BigEndian, uint64(0)))
	require.NoError(t, binary.Write(&encoded, binary.BigEndian, uint32(8<<20)))
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	require.Error(t, ValidateKeystore(encoded.Bytes(), "store-pass", "key-pass", "a"))
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("%d-byte upload allocated %d bytes", encoded.Len(), allocated)
	if allocated > 1<<20 {
		t.Error("embedded length drives allocation despite tiny actual upload")
	}
}
