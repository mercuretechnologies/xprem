package crypto

import (
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"hash"
	"io"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	PrefixActive = "eoo_"
	EntropyBytes = 32
)

func CreateHash(data []byte, hashingAlgorithm, encoding string) (string, error) {
	var h hash.Hash
	switch hashingAlgorithm {
	case "sha256":
		h = sha256.New()
	case "sha512":
		h = sha512.New()
	case "md5":
		h = md5.New()
	default:
		return "", fmt.Errorf("unsupported hashing algorithm: %s", hashingAlgorithm)
	}
	if _, err := h.Write(data); err != nil {
		return "", fmt.Errorf("unable to write data into hasher: %w", err)
	}
	sum := h.Sum(nil)
	switch encoding {
	case "hex":
		return hex.EncodeToString(sum), nil
	case "base64":
		return base64.StdEncoding.EncodeToString(sum), nil
	default:
		return "", fmt.Errorf("unsupported encoding: %s", encoding)
	}
}

// ConvertSHA256HashToUUID builds the update id from the first 16 bytes of a hex
// SHA-256 digest. The version (8, RFC 9562) and variant bits are set so strict
// UUID validators accept the id; without them about 7 ids in 8 are rejected.
func ConvertSHA256HashToUUID(value string) string {
	if len(value) < 32 {
		return ""
	}
	var id uuid.UUID
	if _, err := hex.Decode(id[:], []byte(value[:32])); err != nil {
		return ""
	}
	id[6] = (id[6] & 0x0f) | 0x80
	id[8] = (id[8] & 0x3f) | 0x80
	return id.String()
}

func GetBase64URLEncoding(encodedString string) string {
	base64EncodedString := strings.ReplaceAll(encodedString, "+", "-")
	base64EncodedString = strings.ReplaceAll(base64EncodedString, "/", "_")
	base64EncodedString = strings.TrimRight(base64EncodedString, "=")
	return base64EncodedString
}

func SignRSASHA256(data, privateKeyPEM string) (string, error) {
	block, _ := pem.Decode([]byte(privateKeyPEM))
	if block == nil {
		return "", errors.New("invalid private key PEM format")
	}
	var privateKey *rsa.PrivateKey
	var err error
	if privateKey, err = x509.ParsePKCS1PrivateKey(block.Bytes); err != nil {
		parsedKey, parseErr := x509.ParsePKCS8PrivateKey(block.Bytes)
		if parseErr != nil {
			return "", fmt.Errorf("failed to parse private key: %w", parseErr)
		}
		var ok bool
		privateKey, ok = parsedKey.(*rsa.PrivateKey)
		if !ok {
			return "", errors.New("key is not an RSA private key")
		}
	}
	hashed := sha256.Sum256([]byte(data))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, hashed[:])
	if err != nil {
		return "", fmt.Errorf("failed to sign data: %w", err)
	}
	return base64.StdEncoding.EncodeToString(signature), nil
}

// GenerateRSAKeyPair generates a new 2048-bit RSA key pair formatted as PEM strings.
func GenerateRSAKeyPair() (string, string, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", fmt.Errorf("failed to generate rsa private key: %w", err)
	}
	privBlock := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	}
	privPEM := pem.EncodeToMemory(privBlock)
	pubASN1, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal public key: %w", err)
	}
	pubBlock := &pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pubASN1,
	}
	pubPEM := pem.EncodeToMemory(pubBlock)
	return string(pubPEM), string(privPEM), nil
}

// SealAESGCM encrypts plaintext using a 32-byte master key and returns a base64 encoded string.
// aad is authenticated but not encrypted: it binds the ciphertext to the context it belongs
// to, so a blob sealed under one context cannot be opened under another. UnsealAESGCM must be
// given the exact same aad. Pass nil for data that has no context to bind to.
func SealAESGCM(plaintext []byte, masterKey []byte, aad []byte) (string, error) {
	if len(masterKey) != 32 {
		return "", errors.New("master key must be exactly 32 bytes for AES-256")
	}
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return "", fmt.Errorf("failed to create cipher block: %w", err)
	}
	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}
	// Create a unique nonce for this encryption execution
	nonce := make([]byte, aesGCM.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("failed to generate nonce: %w", err)
	}
	// Encrypt the data, appending the ciphertext directly to the nonce payload
	ciphertext := aesGCM.Seal(nonce, nonce, plaintext, aad)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// UnsealAESGCM decrypts a base64 encoded ciphertext string using the 32-byte master key.
// aad must match the value passed to SealAESGCM, otherwise decryption fails.
func UnsealAESGCM(base64Ciphertext string, masterKey []byte, aad []byte) ([]byte, error) {
	if len(masterKey) != 32 {
		return nil, errors.New("master key must be exactly 32 bytes for AES-256")
	}
	ciphertext, err := base64.StdEncoding.DecodeString(base64Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("failed to decode base64 ciphertext: %w", err)
	}
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create cipher block: %w", err)
	}
	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}
	nonceSize := aesGCM.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, errors.New("ciphertext too short")
	}
	// Extract the nonce from the front and the actual encrypted text from the back
	nonce, encryptedPayload := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := aesGCM.Open(nil, nonce, encryptedPayload, aad)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt (key may be invalid, data was tampered with, or the blob belongs to another context): %w", err)
	}
	return plaintext, nil
}

func GenerateSelfSignedCodeSigningCertificateFromPEM(publicKeyPEM string, privateKeyPEM string, commonName string, serialNumber *big.Int, notBefore time.Time) (string, error) {
	privBlock, _ := pem.Decode([]byte(privateKeyPEM))
	if privBlock == nil {
		return "", errors.New("invalid private key PEM format")
	}
	var privateKey *rsa.PrivateKey
	var err error
	if privateKey, err = x509.ParsePKCS1PrivateKey(privBlock.Bytes); err != nil {
		parsedKey, parseErr := x509.ParsePKCS8PrivateKey(privBlock.Bytes)
		if parseErr != nil {
			return "", fmt.Errorf("failed to parse private key: %w", parseErr)
		}
		var ok bool
		privateKey, ok = parsedKey.(*rsa.PrivateKey)
		if !ok {
			return "", errors.New("key is not an RSA private key")
		}
	}
	pubBlock, _ := pem.Decode([]byte(publicKeyPEM))
	if pubBlock == nil {
		return "", errors.New("invalid public key PEM format")
	}
	var publicKey *rsa.PublicKey
	if publicKey, err = x509.ParsePKCS1PublicKey(pubBlock.Bytes); err != nil {
		parsedPub, parseErr := x509.ParsePKIXPublicKey(pubBlock.Bytes)
		if parseErr != nil {
			return "", fmt.Errorf("failed to parse public key: %w", parseErr)
		}
		var ok bool
		publicKey, ok = parsedPub.(*rsa.PublicKey)
		if !ok {
			return "", errors.New("key is not an RSA public key")
		}
	}
	notAfter := notBefore.AddDate(10, 0, 0) // Exactly 10 years from app creation date
	return generateSelfSignedCodeSigningCertificate(publicKey, privateKey, commonName, serialNumber, notBefore, notAfter)
}

// GenerateSelfSignedCodeSigningCertificate creates a PEM certificate string tailored for Expo OTA code signing.
// It matches the critical extensions defined in @expo/code-signing-certificates.
func generateSelfSignedCodeSigningCertificate(publicKey *rsa.PublicKey, privateKey *rsa.PrivateKey, commonName string, serialNumber *big.Int, notBefore time.Time, notAfter time.Time) (string, error) {
	// OID 2.5.29.15 = id-ce-keyUsage
	keyUsageBytes, _ := asn1.Marshal(asn1.BitString{
		Bytes:     []byte{0x80},
		BitLength: 1,
	})
	// OID 2.5.29.37 = id-ce-extKeyUsage
	extKeyUsageBytes, _ := asn1.Marshal([]asn1.ObjectIdentifier{{1, 3, 6, 1, 5, 5, 7, 3, 3}}) // 1.3.6.1.5.5.7.3.3 = Code Signing
	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName: commonName,
		},
		Issuer: pkix.Name{
			CommonName: commonName,
		},
		NotBefore: notBefore,
		NotAfter:  notAfter,
		ExtraExtensions: []pkix.Extension{
			{
				Id:       asn1.ObjectIdentifier{2, 5, 29, 15},
				Critical: true,
				Value:    keyUsageBytes,
			},
			{
				Id:       asn1.ObjectIdentifier{2, 5, 29, 37},
				Critical: true,
				Value:    extKeyUsageBytes,
			},
		},
	}
	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, publicKey, privateKey)
	if err != nil {
		return "", fmt.Errorf("failed to sign and create x509 certificate: %w", err)
	}
	pemBlock := &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: derBytes,
	}
	return string(pem.EncodeToMemory(pemBlock)), nil
}

// GenerateAPIKey creates a secure, unique plaintext API token for the user
func GenerateAPIKey() (string, error) {
	tokenBytes := make([]byte, EntropyBytes)
	_, err := rand.Read(tokenBytes)
	if err != nil {
		return "", fmt.Errorf("failed to source system entropy: %w", err)
	}
	secretBody := hex.EncodeToString(tokenBytes)
	plaintextKey := fmt.Sprintf("%s%s", PrefixActive, secretBody)
	return plaintextKey, nil
}

func HashPlaintextAPIKey(plaintextKey string) (string, error) {
	hashedValue, err := CreateHash([]byte(plaintextKey), "sha256", "hex")
	if err != nil {
		return "", fmt.Errorf("failed to compute token hash: %w", err)
	}
	return hashedValue, nil
}
