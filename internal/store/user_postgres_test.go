// Integration tests for the guarded user queries: the "at least one admin"
// invariant is enforced by SQL (FOR UPDATE on the admin rows), which the
// in-memory fakes cannot exercise. They need a real Postgres and skip unless
// TEST_DATABASE_URL is set, e.g.:
//
//	docker run -d --name eoo-pg -e POSTGRES_PASSWORD=test -p 55432:5432 postgres:16-alpine
//	TEST_DATABASE_URL="postgres://postgres:test@localhost:55432/postgres?sslmode=disable" go test ./internal/store/
//
// The package is store_test to avoid an import cycle (store -> database/postgres -> migrations -> store).
package store_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"expo-open-ota/internal/database"
	"expo-open-ota/internal/database/postgres"
	"expo-open-ota/internal/database/postgres/pgdb"
	"expo-open-ota/internal/store"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func setupUserStore(t *testing.T) (*store.PostgresUserStore, *pgxpool.Pool) {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_DATABASE_URL must be set in CI: these tests cover SQL that the in-memory fakes cannot reach")
		}
		t.Skip("TEST_DATABASE_URL not set, start a Postgres and set it to run the guarded-query tests")
	}
	// The seed migration fails fast on an empty database without the bootstrap pair.
	t.Setenv("ADMIN_EMAIL", "seed-admin@example.com")
	t.Setenv("ADMIN_PASSWORD", "Sup3rSecret!")
	postgres.RunDBMigrations(dbURL)

	pool, err := pgxpool.New(context.Background(), dbURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return store.NewPostgresUserStore(&database.Engine{Queries: pgdb.New(pool), DB: pool}), pool
}

func resetUsers(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), "DELETE FROM users")
	require.NoError(t, err)
}

func insertUser(t *testing.T, userStore *store.PostgresUserStore, email string, isAdmin bool) store.User {
	t.Helper()
	user, err := userStore.InsertUser(context.Background(), store.InsertUserParameters{
		ID:           uuid.NewString(),
		Email:        email,
		PasswordHash: "irrelevant",
		IsAdmin:      isAdmin,
		// Must be true: the guards count admins as `is_admin AND enabled`, so a
		// disabled fixture admin wouldn't protect the guard and tests would pass falsely.
		Enabled: true,
	})
	require.NoError(t, err)
	return user
}

func countAdmins(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var count int64
	require.NoError(t, pool.QueryRow(context.Background(), "SELECT COUNT(*) FROM users WHERE is_admin").Scan(&count))
	return count
}

func TestGuardedQueriesRefuseRemovingLastAdmin(t *testing.T) {
	userStore, pool := setupUserStore(t)
	ctx := context.Background()
	resetUsers(t, pool)

	admin := insertUser(t, userStore, "solo-admin@example.com", true)
	member := insertUser(t, userStore, "member@example.com", false)

	// The sole admin can neither be demoted nor deleted.
	require.ErrorIs(t, userStore.UpdateUserIsAdmin(ctx, admin.Id, false), store.ErrWouldLeaveNoAdmin)
	require.ErrorIs(t, userStore.DeleteUserByID(ctx, admin.Id), store.ErrWouldLeaveNoAdmin)

	// Members pass the guard, and unknown ids still read as not-found.
	require.NoError(t, userStore.DeleteUserByID(ctx, member.Id))
	notFoundErr := (*store.ErrResourceNotFound)(nil)
	require.ErrorAs(t, userStore.DeleteUserByID(ctx, uuid.NewString()), &notFoundErr)
	require.ErrorAs(t, userStore.UpdateUserIsAdmin(ctx, uuid.NewString(), false), &notFoundErr)

	// With a second admin the demotion goes through, once.
	insertUser(t, userStore, "second-admin@example.com", true)
	require.NoError(t, userStore.UpdateUserIsAdmin(ctx, admin.Id, false))
	require.EqualValues(t, 1, countAdmins(t, pool))
}

// TestGuardedQueriesUnderConcurrentRemovals has two admins remove each other at
// the same instant, repeatedly; exactly one operation must win and exactly one
// admin must remain.
func TestGuardedQueriesUnderConcurrentRemovals(t *testing.T) {
	userStore, pool := setupUserStore(t)
	ctx := context.Background()

	type operation func(targetId string) error
	demote := func(targetId string) error { return userStore.UpdateUserIsAdmin(ctx, targetId, false) }
	remove := func(targetId string) error { return userStore.DeleteUserByID(ctx, targetId) }

	scenarios := []struct {
		name          string
		first, second operation
	}{
		{"demote vs demote", demote, demote},
		{"delete vs delete", remove, remove},
		{"demote vs delete", demote, remove},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			for i := 0; i < 20; i++ {
				resetUsers(t, pool)
				adminA := insertUser(t, userStore, "admin-a@example.com", true)
				adminB := insertUser(t, userStore, "admin-b@example.com", true)

				start := make(chan struct{})
				results := make([]error, 2)
				var wg sync.WaitGroup
				wg.Add(2)
				go func() { defer wg.Done(); <-start; results[0] = scenario.first(adminB.Id) }()
				go func() { defer wg.Done(); <-start; results[1] = scenario.second(adminA.Id) }()
				close(start)
				wg.Wait()

				succeeded := 0
				for _, opErr := range results {
					if opErr == nil {
						succeeded++
					} else {
						require.True(t, errors.Is(opErr, store.ErrWouldLeaveNoAdmin),
							"the losing operation must fail with ErrWouldLeaveNoAdmin, got: %v", opErr)
					}
				}
				require.Equal(t, 1, succeeded, "exactly one of the two concurrent removals must win")
				require.EqualValues(t, 1, countAdmins(t, pool), "exactly one admin must remain")
			}
		})
	}
}

// TestSessionVersionMovesOnlyWhenAccessIsLost verifies the session version
// bumps only when access is actually lost, not on every guarded update.
func TestSessionVersionMovesOnlyWhenAccessIsLost(t *testing.T) {
	userStore, pool := setupUserStore(t)
	ctx := context.Background()
	resetUsers(t, pool)

	// Stays admin and enabled throughout, so the "at least one admin" guard
	// never refuses an operation on the account under test.
	insertUser(t, userStore, "solo-admin@example.com", true)
	member := insertUser(t, userStore, "member@example.com", false)

	versionOf := func() int32 {
		t.Helper()
		user, err := userStore.GetUserByID(ctx, member.Id)
		require.NoError(t, err)
		return user.SessionVersion
	}
	require.EqualValues(t, 0, versionOf())

	// A new password always ends the sessions minted under the old one.
	require.NoError(t, userStore.UpdateUserPassword(ctx, member.Id, "new-hash"))
	require.EqualValues(t, 1, versionOf())

	// Losing access ends them too.
	require.NoError(t, userStore.UpdateUserEnabled(ctx, member.Id, false))
	require.EqualValues(t, 2, versionOf())

	// Regaining access does not: approving an account must not sign anybody out.
	require.NoError(t, userStore.UpdateUserEnabled(ctx, member.Id, true))
	require.EqualValues(t, 2, versionOf())

	// Neither does gaining the admin flag.
	require.NoError(t, userStore.UpdateUserIsAdmin(ctx, member.Id, true))
	require.EqualValues(t, 2, versionOf())

	// Losing it does.
	require.NoError(t, userStore.UpdateUserIsAdmin(ctx, member.Id, false))
	require.EqualValues(t, 3, versionOf())

	// An idempotent write is not a transition: repeating either call on an
	// account already in that state must leave the version alone.
	require.NoError(t, userStore.UpdateUserIsAdmin(ctx, member.Id, false))
	require.NoError(t, userStore.UpdateUserEnabled(ctx, member.Id, true))
	require.EqualValues(t, 3, versionOf())
}
