package crypto

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidatePasswordPolicyAccepted(t *testing.T) {
	assert.NoError(t, ValidatePasswordPolicy("Sup3rSecret!"))
	assert.NoError(t, ValidatePasswordPolicy("aB3!aB3!"))
	// Multibyte runes count once each, and accented letters keep their case class.
	assert.NoError(t, ValidatePasswordPolicy("Änapé-Sec3t"))
}

func TestValidatePasswordPolicyRejected(t *testing.T) {
	cases := []struct {
		name     string
		password string
		missing  string
	}{
		{"too short", "aB3!x", "at least 8 characters"},
		// 8 UTF-8 bytes but only 4 characters, a byte count would let it through.
		{"multibyte shorter than it looks", "Ää１!", "at least 8 characters"},
		{"no uppercase", "sup3rsecret!", "an uppercase letter"},
		{"no lowercase", "SUP3RSECRET!", "a lowercase letter"},
		{"no digit", "SuperSecret!", "a digit"},
		{"no special character", "Sup3rSecret", "a special character"},
		{"empty", "", "at least 8 characters"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePasswordPolicy(tc.password)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tc.missing)
		})
	}
}

// Every failing rule is reported at once, not one 400 at a time.
func TestValidatePasswordPolicyListsAllFailures(t *testing.T) {
	err := ValidatePasswordPolicy("abc")
	assert.Error(t, err)
	for _, expected := range []string{"at least 8 characters", "an uppercase letter", "a digit", "a special character"} {
		assert.Contains(t, err.Error(), expected)
	}
	assert.NotContains(t, err.Error(), "a lowercase letter")
}

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("Sup3rSecret!")
	assert.NoError(t, err)
	assert.True(t, strings.HasPrefix(hash, "$2"), "expected a bcrypt hash, got %q", hash)
	assert.True(t, VerifyPassword(hash, "Sup3rSecret!"))
	assert.False(t, VerifyPassword(hash, "wrong-password"))
	assert.False(t, VerifyPassword("not-a-hash", "Sup3rSecret!"))
}
