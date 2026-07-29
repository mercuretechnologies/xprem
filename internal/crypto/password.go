package crypto

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

// PasswordPolicyMinLength is the minimum length accepted for a dashboard user
// password. The policy below is mirrored in the dashboard
// (apps/dashboard/src/lib/password-policy.ts) so the UI can give per-rule
// feedback before the server is ever hit, keep both in sync.
const PasswordPolicyMinLength = 8

// PasswordPolicyDescription spells out every rule at once, for messages that
// must state the full policy up front, e.g. the seed migration's fail-fast,
// where the operator has no UI checklist to look at.
var PasswordPolicyDescription = fmt.Sprintf(
	"at least %d characters, an uppercase letter, a lowercase letter, a digit and a special character",
	PasswordPolicyMinLength,
)

// ValidatePasswordPolicy rejects passwords that miss any of the policy rules:
// minimum length, one uppercase letter, one lowercase letter, one digit and
// one special (non-alphanumeric) character. The error lists every failing
// rule at once so the user does not discover them one 400 at a time.
func ValidatePasswordPolicy(password string) error {
	var hasUpper, hasLower, hasDigit, hasSpecial bool
	for _, r := range password {
		switch {
		case unicode.IsUpper(r):
			hasUpper = true
		case unicode.IsLower(r):
			hasLower = true
		case unicode.IsDigit(r):
			hasDigit = true
		default:
			hasSpecial = true
		}
	}

	var missing []string
	// Count runes, not bytes: "Ääää" is 8 bytes but only 4 characters, and the
	// dashboard mirror counts characters too.
	if utf8.RuneCountInString(password) < PasswordPolicyMinLength {
		missing = append(missing, fmt.Sprintf("at least %d characters", PasswordPolicyMinLength))
	}
	if !hasUpper {
		missing = append(missing, "an uppercase letter")
	}
	if !hasLower {
		missing = append(missing, "a lowercase letter")
	}
	if !hasDigit {
		missing = append(missing, "a digit")
	}
	if !hasSpecial {
		missing = append(missing, "a special character")
	}
	if len(missing) > 0 {
		return fmt.Errorf("password does not meet the policy, it needs %s", strings.Join(missing, ", "))
	}
	return nil
}

// HashPassword hashes a user password with bcrypt. This is deliberately not
// HashPlaintextAPIKey: API keys carry 32 bytes of entropy so a fast SHA-256 is
// fine, but user passwords are low-entropy and need a slow, salted KDF.
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("failed to hash password: %w", err)
	}
	return string(hash), nil
}

// VerifyPassword reports whether password matches the stored bcrypt hash.
func VerifyPassword(hashedPassword string, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hashedPassword), []byte(password)) == nil
}
