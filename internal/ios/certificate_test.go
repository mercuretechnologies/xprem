package ios

import (
	"crypto/x509"
	"strings"
	"testing"
	"time"
	"xprem/internal/ios/iostest"
	"xprem/internal/types"
	"xprem/internal/validation"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

func assertFieldError(t *testing.T, err error, field string) {
	t.Helper()
	var valErr *validation.Error
	require.ErrorAs(t, err, &valErr)
	assert.Equal(t, field, valErr.Field)
}

func TestParseCertificate(t *testing.T) {
	expiresAt := time.Now().Add(30 * 24 * time.Hour).Truncate(time.Second)
	identity := iostest.NewIdentity("Apple Distribution: Example Inc (ABCDE12345)", "ABCDE12345", expiresAt)

	certificate, err := ParseCertificate(identity.P12("secret"), "secret")
	require.NoError(t, err)
	assert.Equal(t, "Apple Distribution: Example Inc (ABCDE12345)", certificate.CommonName)
	assert.Equal(t, types.IosCertificateDistribution, certificate.Type)
	assert.Equal(t, "ABCDE12345", certificate.TeamID)
	assert.Equal(t, strings.ToUpper(identity.Certificate.SerialNumber.Text(16)), certificate.SerialNumber)
	assert.Equal(t, Fingerprint(identity.Certificate.Raw), certificate.FingerprintSHA1)
	assert.Regexp(t, `^[0-9A-F]{40}$`, certificate.FingerprintSHA1)
	assert.True(t, expiresAt.Equal(certificate.ExpiresAt))
}

func TestParseCertificateTypes(t *testing.T) {
	expiresAt := time.Now().Add(time.Hour)
	for commonName, expected := range map[string]types.IosCertificateType{
		"Apple Distribution: Example Inc (ABCDE12345)":  types.IosCertificateDistribution,
		"iPhone Distribution: Example Inc (ABCDE12345)": types.IosCertificateDistribution,
		"Apple Development: Jane Doe (FGHIJ67890)":      types.IosCertificateDevelopment,
		"iPhone Developer: Jane Doe (FGHIJ67890)":       types.IosCertificateDevelopment,
	} {
		certificate, err := ParseCertificate(iostest.NewIdentity(commonName, "ABCDE12345", expiresAt).P12(""), "")
		require.NoError(t, err, commonName)
		assert.Equal(t, expected, certificate.Type, commonName)
	}
}

func TestParseCertificateRejects(t *testing.T) {
	valid := time.Now().Add(time.Hour)
	appleName := "Apple Distribution: Example Inc (ABCDE12345)"

	assertFieldError(t, second(ParseCertificate(iostest.NewIdentity(appleName, "ABCDE12345", valid).P12("secret"), "wrong")), "certificatePassword")

	expired := second(ParseCertificate(iostest.NewIdentity(appleName, "ABCDE12345", time.Now().Add(-time.Hour)).P12(""), ""))
	assertFieldError(t, expired, "certificateP12")
	assert.ErrorContains(t, expired, "expired")

	notApple := second(ParseCertificate(iostest.NewIdentity("Developer ID Application: Example Inc", "ABCDE12345", valid).P12(""), ""))
	assertFieldError(t, notApple, "certificateP12")
	assert.ErrorContains(t, notApple, "not an Apple signing certificate")

	missingTeam := second(ParseCertificate(iostest.NewIdentity(appleName, "", valid).P12(""), ""))
	assertFieldError(t, missingTeam, "certificateP12")
	assert.ErrorContains(t, missingTeam, "team identifier")

	assertFieldError(t, second(ParseCertificate(iostest.NewIdentity(appleName, "abcde", valid).P12(""), "")), "certificateP12")
	assertFieldError(t, second(ParseCertificate([]byte("not a p12"), "")), "certificateP12")

	identity := iostest.NewIdentity(appleName, "ABCDE12345", valid)
	other := iostest.NewIdentity(appleName, "ABCDE12345", valid)
	mismatched, err := pkcs12.Modern.Encode(identity.Key, other.Certificate, nil, "")
	require.NoError(t, err)
	assertFieldError(t, second(ParseCertificate(mismatched, "")), "certificateP12")

	trustStore, err := pkcs12.Modern.EncodeTrustStore([]*x509.Certificate{identity.Certificate}, "")
	require.NoError(t, err)
	assertFieldError(t, second(ParseCertificate(trustStore, "")), "certificateP12")
}

// The leaf is the certificate matching the key, whatever its position in the file.
func TestParseCertificateFindsLeafAmongChain(t *testing.T) {
	valid := time.Now().Add(time.Hour)
	leaf := iostest.NewIdentity("Apple Development: Jane Doe (FGHIJ67890)", "ABCDE12345", valid)
	intermediate := iostest.NewIdentity("Apple Worldwide Developer Relations Certification Authority", "G3", valid)
	data, err := pkcs12.Modern.Encode(leaf.Key, intermediate.Certificate, []*x509.Certificate{leaf.Certificate}, "pw")
	require.NoError(t, err)

	certificate, err := ParseCertificate(data, "pw")
	require.NoError(t, err)
	assert.Equal(t, Fingerprint(leaf.Certificate.Raw), certificate.FingerprintSHA1)
}

func second[T any](_ T, err error) error {
	return err
}
