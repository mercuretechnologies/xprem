package migrations

import (
	"context"
	"database/sql"
	"expo-open-ota/internal/crypto"
	"expo-open-ota/internal/store"
	"fmt"
	"log"
	"net/mail"
	"os"

	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(UpSeedFirstAdminUser, DownSeedFirstAdminUser)
}

// resolveSeedAdminCredentials reads and validates the bootstrap pair. Missing
// or malformed values are the fail-fast: goose surfaces the error and the
// server refuses to boot until the operator sets them.
func resolveSeedAdminCredentials() (email string, password string, err error) {
	email = store.NormalizeEmail(os.Getenv("ADMIN_EMAIL"))
	password = os.Getenv("ADMIN_PASSWORD")
	if email == "" || password == "" {
		return "", "", fmt.Errorf("control-plane mode requires both ADMIN_EMAIL and ADMIN_PASSWORD to be set: " +
			"they create the first admin user of the dashboard. Set them and restart the server " +
			"(they can be removed once this migration has run)")
	}
	// The addr comparison rejects mailbox forms like "Admin <admin@acme.dev>":
	// ParseAddress accepts them, but the stored string would never match a
	// login lookup and the seeded admin could not sign in.
	if addr, err := mail.ParseAddress(email); err != nil || addr.Address != email {
		return "", "", fmt.Errorf("ADMIN_EMAIL %q is not a plain email address, use the bare form, e.g. admin@example.com", email)
	}
	// The first admin is a dashboard user like any other, hold its bootstrap
	// password to the same policy users face in the UI, and spell the whole
	// policy out: the operator reading this log has no checklist in front of
	// them.
	if err := crypto.ValidatePasswordPolicy(password); err != nil {
		return "", "", fmt.Errorf(
			"ADMIN_PASSWORD seeds the first dashboard admin and must meet the dashboard password policy (%s): %w. "+
				"Set a compliant ADMIN_PASSWORD and restart the server, it can be changed from the dashboard afterwards",
			crypto.PasswordPolicyDescription, err)
	}
	return email, password, nil
}

// UpSeedFirstAdminUser creates the first dashboard user from ADMIN_EMAIL and
// ADMIN_PASSWORD. In control-plane mode dashboard logins are checked against
// the users table, so a database with no user would be a dashboard nobody can
// ever enter, that is why missing values fail the migration (and the boot)
// instead of being skipped.
//
// The env pair is only read here, once: after this migration is recorded the
// dashboard is managed from its own Users page and the variables can be
// dropped from the deployment.
func UpSeedFirstAdminUser(ctx context.Context, tx *sql.Tx) error {
	// A user row can already exist when goose replays history against a
	// restored database whose goose_db_version table was lost. The table being
	// non-empty means the bootstrap already happened, re-seeding could only
	// conflict with it.
	var userCount int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&userCount); err != nil {
		return fmt.Errorf("failed to count existing users: %w", err)
	}
	if userCount > 0 {
		log.Println("⏭️ [DATABASE] Users already exist, skipping first admin user creation.")
		return nil
	}

	email, password, err := resolveSeedAdminCredentials()
	if err != nil {
		return err
	}

	passwordHash, err := crypto.HashPassword(password)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx,
		"INSERT INTO users (id, email, password_hash, is_admin) VALUES ($1, $2, $3, TRUE)",
		uuid.New().String(), email, passwordHash,
	)
	if err != nil {
		return fmt.Errorf("failed to create the first admin user: %w", err)
	}
	log.Printf("👤 [DATABASE] Created the first admin user %s from ADMIN_EMAIL/ADMIN_PASSWORD.", email)
	return nil
}

// DownSeedFirstAdminUser keeps the users: rolling back the schema migration
// drops the table anyway, and deleting "the seeded user" specifically would
// guess at which row that is.
func DownSeedFirstAdminUser(ctx context.Context, tx *sql.Tx) error {
	return nil
}
