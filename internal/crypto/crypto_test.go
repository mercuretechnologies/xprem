package crypto

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestCreateHash(t *testing.T) {
	data := []byte("test data")
	tests := []struct {
		name      string
		algorithm string
		encoding  string
		expectErr bool
	}{
		{"SHA256 Hex", "sha256", "hex", false},
		{"SHA512 Base64", "sha512", "base64", false},
		{"MD5 Hex", "md5", "hex", false},
		{"Unsupported Algorithm", "sha1", "hex", true},
		{"Unsupported Encoding", "sha256", "binary", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash, err := CreateHash(data, tt.algorithm, tt.encoding)
			if (err != nil) != tt.expectErr {
				t.Errorf("unexpected error: %v", err)
			}
			if !tt.expectErr && hash == "" {
				t.Errorf("expected non-empty hash")
			}
		})
	}
}

func TestConvertSHA256HashToUUID(t *testing.T) {
	input := "1234567890abcdef1234567890abcdef12345678"
	expected := "12345678-90ab-8def-9234-567890abcdef"
	result := ConvertSHA256HashToUUID(input)
	if result != expected {
		t.Errorf("expected %s, got %s", expected, result)
	}

	shortInput := "short"
	result = ConvertSHA256HashToUUID(shortInput)
	if result != "" {
		t.Errorf("expected empty string for short input, got %s", result)
	}

	notHex := "zz34567890abcdef1234567890abcdef12345678"
	result = ConvertSHA256HashToUUID(notHex)
	if result != "" {
		t.Errorf("expected empty string for non-hex input, got %s", result)
	}
}

// Every nibble value in the version and variant positions must come out as a
// version 8, RFC 4122 variant UUID, with the other 122 bits untouched.
func TestConvertSHA256HashToUUIDSetsVersionAndVariant(t *testing.T) {
	const hexDigits = "0123456789abcdef"
	for _, version := range hexDigits {
		for variantIndex, variant := range hexDigits {
			input := "1234567890ab" + string(version) + "def" + string(variant) + "234567890abcdef12345678"
			result := ConvertSHA256HashToUUID(input)
			parsed, err := uuid.Parse(result)
			if err != nil {
				t.Fatalf("input %s: %q is not a UUID: %v", input, result, err)
			}
			if parsed.Version() != 8 {
				t.Errorf("input %s: expected version 8, got %d (%s)", input, parsed.Version(), result)
			}
			if parsed.Variant() != uuid.RFC4122 {
				t.Errorf("input %s: expected RFC 4122 variant, got %v (%s)", input, parsed.Variant(), result)
			}
			if result[19] != hexDigits[variantIndex&0x3|0x8] {
				t.Errorf("input %s: low variant bits changed: %s", input, result)
			}
			if result[:14] != "12345678-90ab-" || result[15:18] != "def" || result[20:] != "234-567890abcdef" {
				t.Errorf("input %s: bits outside version and variant changed: %s", input, result)
			}
		}
	}
}

func TestGetBase64URLEncoding(t *testing.T) {
	input := base64.StdEncoding.EncodeToString([]byte("test data"))
	expected := strings.ReplaceAll(strings.ReplaceAll(input, "+", "-"), "/", "_")
	expected = strings.TrimRight(expected, "=")
	result := GetBase64URLEncoding(input)
	if result != expected {
		t.Errorf("expected %s, got %s", expected, result)
	}
}

func TestSignRSASHA256(t *testing.T) {
	data := "test data"

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate private key: %v", err)
	}

	privateKeyBytes := x509.MarshalPKCS1PrivateKey(privateKey)
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: privateKeyBytes,
	})

	signature, err := SignRSASHA256(data, string(privateKeyPEM))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	signatureBytes, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		t.Fatalf("failed to decode signature: %v", err)
	}

	hash := sha256.Sum256([]byte(data))
	err = rsa.VerifyPKCS1v15(&privateKey.PublicKey, crypto.SHA256, hash[:], signatureBytes)
	if err != nil {
		t.Errorf("signature verification failed: %v", err)
	}

	invalidPEM := "-----BEGIN PRIVATE KEY-----\ninvalid\n-----END PRIVATE KEY-----"
	_, err = SignRSASHA256(data, invalidPEM)
	if err == nil {
		t.Errorf("expected error for invalid private key, got none")
	}
}

func TestSealUnsealAESGCMRoundTrip(t *testing.T) {
	masterKey := []byte("0123456789abcdef0123456789abcdef")
	plaintext := []byte("-----BEGIN RSA PRIVATE KEY-----\nsecret\n-----END RSA PRIVATE KEY-----")
	aad := []byte("app-1|private")

	sealed, err := SealAESGCM(plaintext, masterKey, aad)
	if err != nil {
		t.Fatalf("failed to seal: %v", err)
	}
	opened, err := UnsealAESGCM(sealed, masterKey, aad)
	if err != nil {
		t.Fatalf("failed to unseal with the matching aad: %v", err)
	}
	if string(opened) != string(plaintext) {
		t.Error("plaintext did not survive the seal/unseal round trip")
	}
}

// The property the aad exists for: a blob is only openable under the exact
// context it was sealed with, even though the master key is the same.
func TestUnsealAESGCMRejectsMismatchedAAD(t *testing.T) {
	masterKey := []byte("0123456789abcdef0123456789abcdef")
	sealed, err := SealAESGCM([]byte("app one private key"), masterKey, []byte("app-1|private"))
	if err != nil {
		t.Fatalf("failed to seal: %v", err)
	}

	for _, aad := range [][]byte{
		[]byte("app-2|private"), // same half, another app
		[]byte("app-1|public"),  // same app, other half
		nil,                     // no context at all
	} {
		if _, err := UnsealAESGCM(sealed, masterKey, aad); err == nil {
			t.Errorf("expected unseal to fail under aad %q, got nil", aad)
		}
	}
}

func TestSealAESGCMNilAADRoundTrips(t *testing.T) {
	masterKey := []byte("0123456789abcdef0123456789abcdef")
	sealed, err := SealAESGCM([]byte("unbound"), masterKey, nil)
	if err != nil {
		t.Fatalf("failed to seal: %v", err)
	}
	opened, err := UnsealAESGCM(sealed, masterKey, nil)
	if err != nil {
		t.Fatalf("failed to unseal: %v", err)
	}
	if string(opened) != "unbound" {
		t.Error("nil-aad payload did not survive the round trip")
	}
}

func TestUnsealAESGCMRejectsWrongMasterKey(t *testing.T) {
	aad := []byte("app-1|private")
	sealed, err := SealAESGCM([]byte("secret"), []byte("0123456789abcdef0123456789abcdef"), aad)
	if err != nil {
		t.Fatalf("failed to seal: %v", err)
	}
	if _, err := UnsealAESGCM(sealed, []byte("fedcba9876543210fedcba9876543210"), aad); err == nil {
		t.Error("expected unseal to fail under a different master key, got nil")
	}
}
