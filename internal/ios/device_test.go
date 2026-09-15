package ios

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
	"xprem/internal/ios/iostest"

	"github.com/smallstep/pkcs7"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"howett.net/plist"
)

func TestRegistrationProfile(t *testing.T) {
	data, err := RegistrationProfile(RegistrationProfileInput{
		PayloadUUID: "0B8C6B3E-2F1A-4C55-9D2E-7A1B3C4D5E6F",
		AppName:     "Example",
		EnrollURL:   "https://ota.example.com/device-registrations/token/enroll",
		Challenge:   "challenge-1",
	})
	require.NoError(t, err)
	var profile struct {
		PayloadType         string `plist:"PayloadType"`
		PayloadVersion      int    `plist:"PayloadVersion"`
		PayloadIdentifier   string `plist:"PayloadIdentifier"`
		PayloadUUID         string `plist:"PayloadUUID"`
		PayloadDisplayName  string `plist:"PayloadDisplayName"`
		PayloadDescription  string `plist:"PayloadDescription"`
		PayloadOrganization string `plist:"PayloadOrganization"`
		PayloadContent      struct {
			URL              string   `plist:"URL"`
			DeviceAttributes []string `plist:"DeviceAttributes"`
			Challenge        string   `plist:"Challenge"`
		} `plist:"PayloadContent"`
	}
	format, err := plist.Unmarshal(data, &profile)
	require.NoError(t, err)
	assert.Equal(t, plist.XMLFormat, format)
	assert.Equal(t, "Profile Service", profile.PayloadType)
	assert.Equal(t, 1, profile.PayloadVersion)
	assert.Equal(t, "dev.xprem.device-registration.0B8C6B3E-2F1A-4C55-9D2E-7A1B3C4D5E6F", profile.PayloadIdentifier)
	assert.Equal(t, "0B8C6B3E-2F1A-4C55-9D2E-7A1B3C4D5E6F", profile.PayloadUUID)
	assert.Equal(t, "Register this iPhone for Example", profile.PayloadDisplayName)
	assert.NotEmpty(t, profile.PayloadDescription)
	assert.Equal(t, "xprem", profile.PayloadOrganization)
	assert.Equal(t, "https://ota.example.com/device-registrations/token/enroll", profile.PayloadContent.URL)
	assert.Equal(t, []string{"UDID", "PRODUCT", "VERSION", "DEVICE_NAME", "SERIAL"}, profile.PayloadContent.DeviceAttributes)
	assert.Equal(t, "challenge-1", profile.PayloadContent.Challenge)
}

func deviceResponse(t *testing.T, attributes map[string]string) []byte {
	t.Helper()
	content, err := plist.Marshal(attributes, plist.XMLFormat)
	require.NoError(t, err)
	return iostest.SignedData(content, true)
}

func TestParseDeviceResponse(t *testing.T) {
	authority := iostest.NewDeviceAuthority()
	identity := iostest.NewIssuedIdentity(&authority, &x509.Certificate{
		Subject:   pkix.Name{CommonName: "test iPhone"},
		NotBefore: time.Now().Add(-2 * time.Hour), NotAfter: time.Now().Add(-time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature,
	})
	verifier := NewDeviceResponseVerifier(authority.Certificate)
	sign := func(attributes map[string]string) []byte {
		content, err := plist.Marshal(attributes, plist.XMLFormat)
		require.NoError(t, err)
		return identity.SignResponse(content, true)
	}
	attributes, err := verifier.Parse(sign(map[string]string{
		"UDID": "00008110-000A1B2C3D4E801E", "PRODUCT": "iPhone15,2", "VERSION": "22A3354",
		"DEVICE_NAME": "Jane's iPhone", "SERIAL": "F2LXXXXXXX", "CHALLENGE": "challenge-1",
	}), "challenge-1")
	require.NoError(t, err)
	assert.Equal(t, &DeviceAttributes{
		UDID: "00008110-000A1B2C3D4E801E", Product: "iPhone15,2", Version: "22A3354",
		DeviceName: "Jane's iPhone", Serial: "F2LXXXXXXX", Challenge: "challenge-1",
	}, attributes)

	withoutOptional, err := verifier.Parse(sign(map[string]string{
		"UDID": "00008110-000A1B2C3D4E801E", "PRODUCT": "iPhone15,2", "VERSION": "22A3354", "CHALLENGE": "challenge-1",
	}), "challenge-1")
	require.NoError(t, err)
	assert.Empty(t, withoutOptional.DeviceName)
	assert.Empty(t, withoutOptional.Serial)

	for name, data := range map[string][]byte{
		"wrong challenge": sign(map[string]string{"UDID": "UDID-1", "CHALLENGE": "other"}),
		"no challenge":    sign(map[string]string{"UDID": "UDID-1"}),
		"no udid":         sign(map[string]string{"CHALLENGE": "challenge-1"}),
		"not cms":         []byte("<?xml version=\"1.0\"?><plist><dict/></plist>"),
		"not a plist":     identity.SignResponse([]byte("garbage"), false),
	} {
		_, err := verifier.Parse(data, "challenge-1")
		assert.Error(t, err, name)
	}
}

// TestDeviceResponseSignatures rejects tampering and unauthorized signers for both supported encodings.
func TestDeviceResponseSignatures(t *testing.T) {
	authority := iostest.NewDeviceAuthority()
	verifier := NewDeviceResponseVerifier(authority.Certificate)
	content, err := plist.Marshal(map[string]string{"UDID": "real-device", "CHALLENGE": "challenge"}, plist.XMLFormat)
	require.NoError(t, err)
	for _, indefinite := range []bool{false, true} {
		identity := iostest.NewIssuedIdentity(&authority, &x509.Certificate{
			Subject: pkix.Name{CommonName: "iPhone"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
			KeyUsage: x509.KeyUsageDigitalSignature,
		})
		signed := identity.SignResponse(content, indefinite)
		attributes, err := verifier.Parse(signed, "challenge")
		require.NoError(t, err)
		assert.Equal(t, "real-device", attributes.UDID)
		wrongKey := identity
		wrongKey.Key = iostest.NewDeviceAuthority().Key
		_, err = verifier.Parse(wrongKey.SignResponse(content, indefinite), "challenge")
		require.ErrorIs(t, err, errInvalidDeviceResponse, "signature does not match the certified key")
		for name, response := range map[string][]byte{
			"modified content": bytes.Replace(signed, []byte("real-device"), []byte("fake-device"), 1),
			"truncated":        signed[:len(signed)-5],
			"trailing data":    append(append([]byte(nil), signed...), 0),
			"unsigned":         iostest.SignedData(content, indefinite),
		} {
			_, err := verifier.Parse(response, "challenge")
			require.ErrorIs(t, err, errInvalidDeviceResponse, name)
		}
		otherAuthority := iostest.NewDeviceAuthority()
		_, err = NewDeviceResponseVerifier(otherAuthority.Certificate).Parse(signed, "challenge")
		require.ErrorIs(t, err, errInvalidDeviceResponse, "same CA name with a different key")
		_, err = ParseDeviceResponse(signed, "challenge")
		require.ErrorIs(t, err, errInvalidDeviceResponse, "production must never trust a test CA")
	}
	for name, template := range map[string]*x509.Certificate{
		"CA signer":                  {IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign},
		"encryption only":            {KeyUsage: x509.KeyUsageKeyEncipherment},
		"unknown critical extension": {ExtraExtensions: []pkix.Extension{{Id: asn1.ObjectIdentifier{1, 2, 3, 4}, Critical: true, Value: []byte{5, 0}}}},
	} {
		identity := iostest.NewIssuedIdentity(&authority, template)
		_, err := verifier.Parse(identity.SignResponse(content, false), "challenge")
		require.ErrorIs(t, err, errInvalidDeviceResponse, name)
	}
	_, err = verifier.Parse(authority.SignResponse(content, false), "challenge")
	require.ErrorIs(t, err, errInvalidDeviceResponse, "the CA itself is not a device")
}

// TestAppleDeviceTrustAnchor checks that the embedded pin is Apple's documented device issuer.
func TestAppleDeviceTrustAnchor(t *testing.T) {
	block, _ := pem.Decode(appleDeviceCAPEM)
	require.NotNil(t, block)
	certificate, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	assert.Equal(t, "Apple iPhone Device CA", certificate.Subject.CommonName)
	assert.True(t, certificate.IsCA)
	assert.NotZero(t, certificate.KeyUsage&x509.KeyUsageCertSign)
}

// TestDeviceResponseLegacySignatures covers RSA/SHA-1 responses without signed attributes.
func TestDeviceResponseLegacySignatures(t *testing.T) {
	authority := iostest.NewDeviceAuthority()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "legacy iPhone"},
		NotBefore: time.Now().Add(-2 * time.Hour), NotAfter: time.Now().Add(-time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature,
	}, authority.Certificate, &key.PublicKey, authority.Key)
	require.NoError(t, err)
	certificate, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	content, err := plist.Marshal(map[string]string{"UDID": "legacy-device", "CHALLENGE": "challenge"}, plist.XMLFormat)
	require.NoError(t, err)
	for _, digest := range []asn1.ObjectIdentifier{pkcs7.OIDDigestAlgorithmSHA1, pkcs7.OIDDigestAlgorithmSHA256} {
		signed, err := pkcs7.NewSignedData(content)
		require.NoError(t, err)
		signed.SetDigestAlgorithm(digest)
		require.NoError(t, signed.SignWithoutAttr(certificate, key, pkcs7.SignerInfoConfig{}))
		response, err := signed.Finish()
		require.NoError(t, err)
		attributes, err := NewDeviceResponseVerifier(authority.Certificate).Parse(response, "challenge")
		require.NoError(t, err)
		assert.Equal(t, "legacy-device", attributes.UDID)
	}
}

// FuzzParseDeviceResponse exercises the public CMS parser against malformed untrusted envelopes.
func FuzzParseDeviceResponse(f *testing.F) {
	content, err := plist.Marshal(map[string]string{"UDID": "device", "CHALLENGE": "challenge"}, plist.XMLFormat)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(iostest.SignedData(content, false))
	f.Add(iostest.SignedData(content, true))
	f.Add([]byte("not CMS"))
	f.Fuzz(func(t *testing.T, response []byte) {
		_, _ = ParseDeviceResponse(response, "challenge")
	})
}

// TestParseDeviceResponseRejectsUnsigned prevents a link holder fabricating device attributes.
func TestParseDeviceResponseRejectsUnsigned(t *testing.T) {
	_, err := ParseDeviceResponse(deviceResponse(t, map[string]string{"UDID": "invented", "CHALLENGE": "known"}), "known")
	require.ErrorIs(t, err, errInvalidDeviceResponse)
}
