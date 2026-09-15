package ios

import (
	"bytes"
	"crypto"
	"crypto/sha1"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"
	"xprem/internal/types"
	"xprem/internal/validation"

	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

var teamIDPattern = regexp.MustCompile(`^[A-Z0-9]{10}$`)

// Certificate is the non-secret identity of a validated Apple signing certificate.
type Certificate struct {
	CommonName      string
	SerialNumber    string
	FingerprintSHA1 string
	Type            types.IosCertificateType
	TeamID          string
	ExpiresAt       time.Time
}

// ParseCertificate opens a PKCS#12 file with its password and checks that it
// holds one private key with an unexpired Apple signing certificate.
func ParseCertificate(data []byte, password string) (*Certificate, error) {
	privateKey, certificate, otherCertificates, err := pkcs12.DecodeChain(data, password)
	if err != nil {
		if errors.Is(err, pkcs12.ErrIncorrectPassword) || errors.Is(err, pkcs12.ErrDecryption) {
			return nil, validation.Errorf("certificatePassword", "the password is incorrect")
		}
		return nil, validation.Errorf("certificateP12", "file is not a PKCS#12 certificate with exactly one private key")
	}
	leaf, err := certificateForKey(privateKey, append([]*x509.Certificate{certificate}, otherCertificates...))
	if err != nil {
		return nil, err
	}
	commonName := leaf.Subject.CommonName
	certificateType, ok := certificateTypeOf(commonName)
	if !ok {
		return nil, validation.Errorf("certificateP12", "%q is not an Apple signing certificate", commonName)
	}
	if len(leaf.Subject.OrganizationalUnit) == 0 || !teamIDPattern.MatchString(leaf.Subject.OrganizationalUnit[0]) {
		return nil, validation.Errorf("certificateP12", "certificate has no Apple team identifier")
	}
	if !time.Now().Before(leaf.NotAfter) {
		return nil, validation.Errorf("certificateP12", "certificate expired on %s", leaf.NotAfter.UTC().Format(time.DateOnly))
	}
	return &Certificate{
		CommonName:      commonName,
		SerialNumber:    strings.ToUpper(leaf.SerialNumber.Text(16)),
		FingerprintSHA1: Fingerprint(leaf.Raw),
		Type:            certificateType,
		TeamID:          leaf.Subject.OrganizationalUnit[0],
		ExpiresAt:       leaf.NotAfter,
	}, nil
}

// Fingerprint is the SHA-1 of a DER certificate as uppercase hex.
func Fingerprint(der []byte) string {
	sum := sha1.Sum(der)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// certificateForKey finds the certificate whose public key matches the imported private key.
func certificateForKey(privateKey any, certificates []*x509.Certificate) (*x509.Certificate, error) {
	signer, ok := privateKey.(crypto.Signer)
	if !ok {
		return nil, validation.Errorf("certificateP12", "certificate private key is not a signing key")
	}
	publicKey, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil {
		return nil, validation.Errorf("certificateP12", "certificate private key is not a supported signing key")
	}
	for _, certificate := range certificates {
		if bytes.Equal(publicKey, certificate.RawSubjectPublicKeyInfo) {
			return certificate, nil
		}
	}
	return nil, validation.Errorf("certificateP12", "no certificate in the file matches its private key")
}

// certificateTypeOf recognizes Apple development and distribution certificate subject names.
func certificateTypeOf(commonName string) (types.IosCertificateType, bool) {
	switch {
	case strings.HasPrefix(commonName, "Apple Distribution"), strings.HasPrefix(commonName, "iPhone Distribution"):
		return types.IosCertificateDistribution, true
	case strings.HasPrefix(commonName, "Apple Development"), strings.HasPrefix(commonName, "iPhone Developer"):
		return types.IosCertificateDevelopment, true
	}
	return "", false
}
