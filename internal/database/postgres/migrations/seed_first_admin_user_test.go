package migrations

import (
	"strings"
	"testing"
)

// The seed migration is the fail-fast for a control-plane boot without the
// bootstrap credentials: a users table with no user would be a dashboard
// nobody can ever log into.
func TestResolveSeedAdminCredentialsFailsFastWhenUnset(t *testing.T) {
	cases := []struct {
		name     string
		email    string
		password string
	}{
		{"both missing", "", ""},
		{"email missing", "", "Sup3rSecret!"},
		{"password missing", "admin@example.com", ""},
		{"blank email", "   ", "Sup3rSecret!"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ADMIN_EMAIL", tc.email)
			t.Setenv("ADMIN_PASSWORD", tc.password)
			_, _, err := resolveSeedAdminCredentials()
			if err == nil {
				t.Fatal("expected an error when ADMIN_EMAIL/ADMIN_PASSWORD are not both set")
			}
			if !strings.Contains(err.Error(), "ADMIN_EMAIL and ADMIN_PASSWORD") {
				t.Fatalf("error should tell the operator which variables to set, got: %v", err)
			}
		})
	}
}

func TestResolveSeedAdminCredentialsRejectsMalformedEmail(t *testing.T) {
	t.Setenv("ADMIN_EMAIL", "not-an-email")
	t.Setenv("ADMIN_PASSWORD", "Sup3rSecret!")
	_, _, err := resolveSeedAdminCredentials()
	if err == nil {
		t.Fatal("expected an error for a malformed ADMIN_EMAIL")
	}
}

// ParseAddress accepts "Admin <admin@example.com>", but storing that string
// would leave a seeded admin that can never match a login lookup.
func TestResolveSeedAdminCredentialsRejectsMailboxFormEmail(t *testing.T) {
	t.Setenv("ADMIN_EMAIL", "Admin <admin@example.com>")
	t.Setenv("ADMIN_PASSWORD", "Sup3rSecret!")
	_, _, err := resolveSeedAdminCredentials()
	if err == nil {
		t.Fatal("expected an error for a mailbox-form ADMIN_EMAIL")
	}
}

// The first admin is held to the same password policy as every dashboard
// user, and the failure must spell the whole policy out, the operator
// reading the migration log has no UI checklist.
func TestResolveSeedAdminCredentialsEnforcesPasswordPolicy(t *testing.T) {
	t.Setenv("ADMIN_EMAIL", "admin@example.com")
	t.Setenv("ADMIN_PASSWORD", "weak")
	_, _, err := resolveSeedAdminCredentials()
	if err == nil {
		t.Fatal("expected an error for a weak ADMIN_PASSWORD")
	}
	for _, expected := range []string{
		"ADMIN_PASSWORD",
		"at least 8 characters, an uppercase letter, a lowercase letter, a digit and a special character",
		"Set a compliant ADMIN_PASSWORD and restart",
	} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("error should state the full password policy, missing %q in: %v", expected, err)
		}
	}
}

func TestResolveSeedAdminCredentialsNormalizesEmail(t *testing.T) {
	t.Setenv("ADMIN_EMAIL", "  Admin@Example.COM ")
	t.Setenv("ADMIN_PASSWORD", "Sup3rSecret!")
	email, password, err := resolveSeedAdminCredentials()
	if err != nil {
		t.Fatalf("expected valid credentials to resolve, got: %v", err)
	}
	if email != "admin@example.com" {
		t.Fatalf("expected the email lowercased and trimmed, got %q", email)
	}
	if password != "Sup3rSecret!" {
		t.Fatalf("password must be passed through untouched, got %q", password)
	}
}
