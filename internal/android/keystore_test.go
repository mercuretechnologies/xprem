package android

import (
	"os"
	"testing"
	"xprem/internal/android/androidtest"
	"xprem/internal/validation"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func assertFieldError(t *testing.T, err error, field string) {
	t.Helper()
	var valErr *validation.Error
	require.ErrorAs(t, err, &valErr)
	assert.Equal(t, field, valErr.Field)
}

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	require.NoError(t, err)
	return data
}

func TestValidateAndroidKeystoreJKS(t *testing.T) {
	data := androidtest.JKSKeystore("store-pass", "key-pass", "upload")

	assert.NoError(t, ValidateKeystore(data, "store-pass", "key-pass", "upload"))
	// JKS aliases are case-insensitive.
	assert.NoError(t, ValidateKeystore(data, "store-pass", "key-pass", "UPLOAD"))

	assertFieldError(t, ValidateKeystore(data, "wrong", "key-pass", "upload"), "keystorePassword")
	assertFieldError(t, ValidateKeystore(data, "store-pass", "wrong", "upload"), "keyPassword")

	err := ValidateKeystore(data, "store-pass", "key-pass", "release")
	assertFieldError(t, err, "keyAlias")
	assert.Contains(t, err.Error(), "upload")
}

// keytool-generated PKCS12 files carry the alias as friendlyName.
func TestValidateAndroidKeystorePKCS12FromKeytool(t *testing.T) {
	single := readTestdata(t, "single.p12")
	assert.NoError(t, ValidateKeystore(single, "store-pass", "store-pass", "upload"))
	assert.NoError(t, ValidateKeystore(single, "store-pass", "store-pass", "UPLOAD"))
	assertFieldError(t, ValidateKeystore(single, "wrong", "wrong", "upload"), "keystorePassword")
	assertFieldError(t, ValidateKeystore(single, "store-pass", "other", "upload"), "keyPassword")

	err := ValidateKeystore(single, "store-pass", "store-pass", "release")
	assertFieldError(t, err, "keyAlias")
	assert.Contains(t, err.Error(), "upload")

	multi := readTestdata(t, "multi.p12")
	assert.NoError(t, ValidateKeystore(multi, "store-pass", "store-pass", "upload"))
	assert.NoError(t, ValidateKeystore(multi, "store-pass", "store-pass", "release"))

	err = ValidateKeystore(multi, "store-pass", "store-pass", "nope")
	assertFieldError(t, err, "keyAlias")
	assert.Contains(t, err.Error(), "upload")
	assert.Contains(t, err.Error(), "release")
}

// go-pkcs12 writes no friendlyName, so the alias cannot be checked.
func TestValidateAndroidKeystorePKCS12WithoutAlias(t *testing.T) {
	data := androidtest.PKCS12Keystore("store-pass")

	assert.NoError(t, ValidateKeystore(data, "store-pass", "store-pass", "any-alias"))

	assertFieldError(t, ValidateKeystore(data, "wrong", "wrong", "any-alias"), "keystorePassword")
	assertFieldError(t, ValidateKeystore(data, "store-pass", "other", "any-alias"), "keyPassword")
}

func TestValidateAndroidKeystorePKCS12WithoutPrivateKey(t *testing.T) {
	assertFieldError(t, ValidateKeystore(androidtest.PKCS12TrustStore("store-pass"), "store-pass", "store-pass", "upload"), "keystore")
}

func TestValidateAndroidKeystoreRejectsUnknownFormats(t *testing.T) {
	assertFieldError(t, ValidateKeystore([]byte("not a keystore at all"), "p", "p", "a"), "keystore")

	jceks := []byte{0xCE, 0xCE, 0xCE, 0xCE, 0x00, 0x00, 0x00, 0x02}
	assertFieldError(t, ValidateKeystore(jceks, "p", "p", "a"), "keystore")

	truncatedDER := []byte{0x30, 0x82, 0x01, 0x00}
	assertFieldError(t, ValidateKeystore(truncatedDER, "p", "p", "a"), "keystore")
}
