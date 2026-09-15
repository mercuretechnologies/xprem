package ios

import (
	"bytes"
	"crypto/subtle"
	"crypto/x509"
	_ "embed"
	"encoding/asn1"
	"encoding/pem"
	"errors"
	"time"

	"github.com/smallstep/pkcs7"
	"howett.net/plist"
)

// RegistrationProfileInput describes the Profile Service configuration profile of one registration link.
type RegistrationProfileInput struct {
	PayloadUUID string
	AppName     string
	EnrollURL   string
	Challenge   string
}

// RegistrationProfile renders an unsigned .mobileconfig that makes iOS post its device attributes to EnrollURL.
func RegistrationProfile(input RegistrationProfileInput) ([]byte, error) {
	document := map[string]any{
		"PayloadType":         "Profile Service",
		"PayloadVersion":      1,
		"PayloadIdentifier":   "dev.xprem.device-registration." + input.PayloadUUID,
		"PayloadUUID":         input.PayloadUUID,
		"PayloadDisplayName":  "Register this iPhone for " + input.AppName,
		"PayloadDescription":  "Sends this iPhone's identifier and model so it can be added to the Apple Developer account of " + input.AppName + " and install its Ad Hoc builds.",
		"PayloadOrganization": "xprem",
		"PayloadContent": map[string]any{
			"URL":              input.EnrollURL,
			"DeviceAttributes": []string{"UDID", "PRODUCT", "VERSION", "DEVICE_NAME", "SERIAL"},
			"Challenge":        input.Challenge,
		},
	}
	return plist.MarshalIndent(document, plist.XMLFormat, "\t")
}

// DeviceAttributes is what an iPhone sends back after installing a registration profile.
type DeviceAttributes struct {
	UDID       string `plist:"UDID"`
	Product    string `plist:"PRODUCT"`
	Version    string `plist:"VERSION"`
	DeviceName string `plist:"DEVICE_NAME"`
	Serial     string `plist:"SERIAL"`
	Challenge  string `plist:"CHALLENGE"`
}

var errInvalidDeviceResponse = errors.New("invalid device response")

// appleDeviceCAPEM pins the device-issuing CA published by Apple, not the system TLS roots.
// Source: https://developer.apple.com/library/archive/documentation/NetworkingInternet/Conceptual/iPhoneOTAConfiguration/profile-service/profile-service.html
//
//go:embed apple_iphone_device_ca.pem
var appleDeviceCAPEM []byte

// DeviceResponseVerifier authenticates a CMS response against a pinned device-issuing CA.
type DeviceResponseVerifier struct {
	authority *x509.Certificate
}

// NewDeviceResponseVerifier selects a device CA; injection allows tests to use their own signing keys.
func NewDeviceResponseVerifier(authority *x509.Certificate) *DeviceResponseVerifier {
	return &DeviceResponseVerifier{authority: authority}
}

// ParseDeviceResponse verifies Apple's device signature before reading attributes and checking the challenge.
func ParseDeviceResponse(data []byte, challenge string) (*DeviceAttributes, error) {
	block, _ := pem.Decode(appleDeviceCAPEM)
	if block == nil {
		return nil, errInvalidDeviceResponse
	}
	authority, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, errInvalidDeviceResponse
	}
	return NewDeviceResponseVerifier(authority).Parse(data, challenge)
}

// Parse verifies the signature and the direct chain to the pinned device CA, then checks the plist.
func (v *DeviceResponseVerifier) Parse(data []byte, challenge string) (*DeviceAttributes, error) {
	if len(data) > 64<<10 || challenge == "" {
		return nil, errInvalidDeviceResponse
	}
	content, err := signedDataContent(data)
	if err != nil {
		return nil, errInvalidDeviceResponse
	}
	signed, err := pkcs7.Parse(data)
	if err != nil || !bytes.Equal(content, signed.Content) {
		return nil, errInvalidDeviceResponse
	}
	signer := signed.GetOnlySigner()
	if signer == nil || signer.IsCA || len(signer.UnhandledCriticalExtensions) != 0 ||
		(signer.KeyUsage != 0 && signer.KeyUsage&x509.KeyUsageDigitalSignature == 0) {
		return nil, errInvalidDeviceResponse
	}
	if len(signed.Signers[0].AuthenticatedAttributes) != 0 {
		var contentType asn1.ObjectIdentifier
		if err := signed.UnmarshalSignedAttribute(pkcs7.OIDAttributeContentType, &contentType); err != nil || !contentType.Equal(pkcs7.OIDData) {
			return nil, errInvalidDeviceResponse
		}
	}
	if v.authority == nil || !v.authority.IsCA || !v.authority.BasicConstraintsValid ||
		v.authority.KeyUsage&x509.KeyUsageCertSign == 0 || !bytes.Equal(signer.RawIssuer, v.authority.RawSubject) {
		return nil, errInvalidDeviceResponse
	}
	// Trust is anchored directly at Apple's device CA. CheckSignature verifies the
	// certificate's original signed bytes and accepts the historical SHA-1 signatures
	// used by this private PKI; it does not weaken the process-wide X.509 policy.
	if err := v.authority.CheckSignature(signer.SignatureAlgorithm, signer.RawTBSCertificate, signer.Signature); err != nil {
		return nil, errInvalidDeviceResponse
	}
	// Apple's Profile Service policy explicitly ignores device certificate dates.
	// Widen only this parsed copy so PKCS#7's signing-time check follows that policy;
	// the certificate signature above was checked against its untouched RawTBS bytes.
	signer.NotBefore = time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)
	signer.NotAfter = time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
	if err := signed.Verify(); err != nil {
		return nil, errInvalidDeviceResponse
	}
	var attributes DeviceAttributes
	if _, err := plist.Unmarshal(content, &attributes); err != nil {
		return nil, errInvalidDeviceResponse
	}
	if attributes.UDID == "" || subtle.ConstantTimeCompare([]byte(attributes.Challenge), []byte(challenge)) != 1 {
		return nil, errInvalidDeviceResponse
	}
	return &attributes, nil
}
