package android

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/binary"
	"runtime"
	"testing"
	"time"
	"xprem/internal/android/androidtest"

	keystore "github.com/pavlo-v-chernykh/keystore-go/v4"
	"github.com/stretchr/testify/require"
)

func TestJKSRejectsNonSigningEntry(t *testing.T) {
	ks := keystore.New()
	require.NoError(t, ks.Load(bytes.NewReader(androidtest.JKSKeystore("store-pass", "key-pass", "upload")), []byte("store-pass")))
	validEntry, err := ks.GetPrivateKeyEntry("upload", []byte("key-pass"))
	require.NoError(t, err)
	otherKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	otherPKCS8, err := x509.MarshalPKCS8PrivateKey(otherKey)
	require.NoError(t, err)
	for _, tc := range []struct {
		name  string
		key   []byte
		chain []keystore.Certificate
	}{
		{"invalid key", []byte("not-a-private-key"), validEntry.CertificateChain},
		{"no certificate", validEntry.PrivateKey, nil},
		{"invalid certificate", validEntry.PrivateKey, []keystore.Certificate{{Type: "X509", Content: []byte("not-a-certificate")}}},
		{"mismatched certificate", otherPKCS8, validEntry.CertificateChain},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, ks.SetPrivateKeyEntry("upload", keystore.PrivateKeyEntry{CreationTime: time.Now(), PrivateKey: tc.key, CertificateChain: tc.chain}, []byte("key-pass")))
			var encoded bytes.Buffer
			require.NoError(t, ks.Store(&encoded, []byte("store-pass")))
			assertFieldError(t, ValidateKeystore(encoded.Bytes(), "store-pass", "key-pass", "upload"), "keystore")
		})
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
