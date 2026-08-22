// Package androidtest builds in-memory Android signing keystores for tests.
package androidtest

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"sync"
	"time"

	keystore "github.com/pavlo-v-chernykh/keystore-go/v4"
	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

// The signing key and certificate are generated once and shared by every
// keystore fixture; only the containers differ per call.
var (
	signingOnce     sync.Once
	signingKey      *ecdsa.PrivateKey
	signingKeyPKCS8 []byte
	signingCert     *x509.Certificate
)

func signingKeyAndCert() (*ecdsa.PrivateKey, []byte, *x509.Certificate) {
	signingOnce.Do(func() {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			panic(err)
		}
		template := &x509.Certificate{
			SerialNumber: big.NewInt(1),
			Subject:      pkix.Name{CommonName: "test signing key"},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(24 * time.Hour),
		}
		certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
		if err != nil {
			panic(err)
		}
		cert, err := x509.ParseCertificate(certDER)
		if err != nil {
			panic(err)
		}
		pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			panic(err)
		}
		signingKey = key
		signingKeyPKCS8 = pkcs8
		signingCert = cert
	})
	return signingKey, signingKeyPKCS8, signingCert
}

// JKSKeystore returns a JKS keystore holding one private key entry under alias.
func JKSKeystore(storePassword, keyPassword, alias string) []byte {
	_, pkcs8, cert := signingKeyAndCert()
	ks := keystore.New()
	entry := keystore.PrivateKeyEntry{
		CreationTime:     time.Now(),
		PrivateKey:       pkcs8,
		CertificateChain: []keystore.Certificate{{Type: "X509", Content: cert.Raw}},
	}
	if err := ks.SetPrivateKeyEntry(alias, entry, []byte(keyPassword)); err != nil {
		panic(err)
	}
	var buf bytes.Buffer
	if err := ks.Store(&buf, []byte(storePassword)); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// PKCS12Keystore returns a PKCS12 keystore holding one private key and its certificate.
func PKCS12Keystore(password string) []byte {
	key, _, cert := signingKeyAndCert()
	data, err := pkcs12.Modern.Encode(key, cert, nil, password)
	if err != nil {
		panic(err)
	}
	return data
}

// PKCS12TrustStore returns a PKCS12 keystore holding one certificate and no private key.
func PKCS12TrustStore(password string) []byte {
	_, _, cert := signingKeyAndCert()
	data, err := pkcs12.Modern.EncodeTrustStore([]*x509.Certificate{cert}, password)
	if err != nil {
		panic(err)
	}
	return data
}
