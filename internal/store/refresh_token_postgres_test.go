// Integration tests for the refresh-token ledger; the single-use claim is
// enforced by SQL (a conditional UPDATE ... RETURNING). Same harness and skip
// rules as user_postgres_test.go.
package store_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"expo-open-ota/internal/database"
	"expo-open-ota/internal/database/postgres"
	"expo-open-ota/internal/database/postgres/pgdb"
	"expo-open-ota/internal/store"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupRefreshTokenStore(t *testing.T) (*store.PostgresRefreshTokenStore, *store.PostgresUserStore, *pgxpool.Pool) {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_DATABASE_URL must be set in CI: these tests cover SQL that the in-memory fakes cannot reach")
		}
		t.Skip("TEST_DATABASE_URL not set. Start a Postgres and set it to run the ledger tests")
	}
	t.Setenv("ADMIN_EMAIL", "seed-admin@example.com")
	t.Setenv("ADMIN_PASSWORD", "Sup3rSecret!")
	postgres.RunDBMigrations(dbURL)

	pool, err := pgxpool.New(context.Background(), dbURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	engine := &database.Engine{Queries: pgdb.New(pool), DB: pool}
	return store.NewPostgresRefreshTokenStore(engine), store.NewPostgresUserStore(engine), pool
}

func insertRefreshToken(t *testing.T, tokenStore *store.PostgresRefreshTokenStore, userId string, familyId string, expiresAt time.Time) string {
	t.Helper()
	id := uuid.NewString()
	require.NoError(t, tokenStore.InsertRefreshToken(context.Background(), store.InsertRefreshTokenParameters{
		ID:        id,
		UserID:    userId,
		FamilyID:  familyId,
		ExpiresAt: expiresAt,
	}))
	return id
}

func TestRefreshTokenLedgerLifecycle(t *testing.T) {
	tokenStore, userStore, pool := setupRefreshTokenStore(t)
	ctx := context.Background()
	resetUsers(t, pool)
	user := insertUser(t, userStore, "ledger@example.com", false)
	familyId := uuid.NewString()

	tokenId := insertRefreshToken(t, tokenStore, user.Id, familyId, time.Now().Add(time.Hour))
	successorId := uuid.NewString()
	consumed, err := tokenStore.RotateRefreshToken(ctx, store.RotateRefreshTokenParameters{
		OldID:     tokenId,
		NewID:     successorId,
		ExpiresAt: time.Now().Add(time.Hour),
	})
	require.NoError(t, err)
	assert.Equal(t, familyId, consumed.FamilyId)
	assert.Equal(t, user.Id, consumed.UserId)

	// The successor landed in the same transaction, in the same family and
	// under the same account, without the caller having to say so.
	successor, err := tokenStore.GetRefreshToken(ctx, successorId, refreshGrace)
	require.NoError(t, err)
	assert.Equal(t, familyId, successor.FamilyId)
	assert.Equal(t, user.Id, successor.UserId)
	assert.Nil(t, successor.UsedAt)

	// Rotated once, never again: the second attempt finds nothing to claim, while
	// the row is still readable and carries its rotation stamp and successor.
	notFoundErr := (*store.ErrResourceNotFound)(nil)
	_, err = tokenStore.RotateRefreshToken(ctx, store.RotateRefreshTokenParameters{
		OldID: tokenId, NewID: uuid.NewString(), ExpiresAt: time.Now().Add(time.Hour),
	})
	require.ErrorAs(t, err, &notFoundErr)
	stored, err := tokenStore.GetRefreshToken(ctx, tokenId, refreshGrace)
	require.NoError(t, err)
	require.NotNil(t, stored.UsedAt)
	require.NotNil(t, stored.ReplacedBy)
	assert.Equal(t, successorId, *stored.ReplacedBy)

	// The grace verdict is computed by the database, not by the caller's clock.
	assert.True(t, stored.UsedRecently, "a rotation that just happened is inside the grace")
	stale, err := tokenStore.GetRefreshToken(ctx, tokenId, 0)
	require.NoError(t, err)
	assert.False(t, stale.UsedRecently, "a zero grace admits nothing")

	// An expired token is not claimable either, whether or not it was used.
	expiredId := insertRefreshToken(t, tokenStore, user.Id, familyId, time.Now().Add(-time.Minute))
	_, err = tokenStore.RotateRefreshToken(ctx, store.RotateRefreshTokenParameters{
		OldID: expiredId, NewID: uuid.NewString(), ExpiresAt: time.Now().Add(time.Hour),
	})
	require.ErrorAs(t, err, &notFoundErr)
	// And the refused rotation wrote nothing: no orphan successor.
	_, err = tokenStore.GetRefreshToken(ctx, successorId, refreshGrace)
	require.NoError(t, err)

	// An id nothing ever issued reads as not-found rather than as an error.
	_, err = tokenStore.GetRefreshToken(ctx, uuid.NewString(), refreshGrace)
	require.ErrorAs(t, err, &notFoundErr)
}

// refreshGrace mirrors the service's replay window closely enough for the
// store tests; the exact value only matters to the service.
const refreshGrace = 30 * time.Second

func TestRevokingAFamilyLeavesOtherFamiliesAlone(t *testing.T) {
	tokenStore, userStore, pool := setupRefreshTokenStore(t)
	ctx := context.Background()
	resetUsers(t, pool)
	user := insertUser(t, userStore, "ledger@example.com", false)

	leaked := uuid.NewString()
	otherDevice := uuid.NewString()
	leakedId := insertRefreshToken(t, tokenStore, user.Id, leaked, time.Now().Add(time.Hour))
	survivingId := insertRefreshToken(t, tokenStore, user.Id, otherDevice, time.Now().Add(time.Hour))

	require.NoError(t, tokenStore.DeleteRefreshTokenFamily(ctx, leaked))

	notFoundErr := (*store.ErrResourceNotFound)(nil)
	_, err := tokenStore.GetRefreshToken(ctx, leakedId, refreshGrace)
	require.ErrorAs(t, err, &notFoundErr)
	_, err = tokenStore.RotateRefreshToken(ctx, store.RotateRefreshTokenParameters{
		OldID: survivingId, NewID: uuid.NewString(), ExpiresAt: time.Now().Add(time.Hour),
	})
	require.NoError(t, err)
}

func TestPurgeOnlyDropsExpiredTokensOfThatUser(t *testing.T) {
	tokenStore, userStore, pool := setupRefreshTokenStore(t)
	ctx := context.Background()
	resetUsers(t, pool)
	user := insertUser(t, userStore, "ledger@example.com", false)
	other := insertUser(t, userStore, "other@example.com", false)

	expiredId := insertRefreshToken(t, tokenStore, user.Id, uuid.NewString(), time.Now().Add(-time.Minute))
	liveId := insertRefreshToken(t, tokenStore, user.Id, uuid.NewString(), time.Now().Add(time.Hour))
	otherExpiredId := insertRefreshToken(t, tokenStore, other.Id, uuid.NewString(), time.Now().Add(-time.Minute))

	require.NoError(t, tokenStore.DeleteExpiredRefreshTokens(ctx, user.Id))

	notFoundErr := (*store.ErrResourceNotFound)(nil)
	_, err := tokenStore.GetRefreshToken(ctx, expiredId, refreshGrace)
	require.ErrorAs(t, err, &notFoundErr)
	_, err = tokenStore.GetRefreshToken(ctx, liveId, refreshGrace)
	require.NoError(t, err)
	_, err = tokenStore.GetRefreshToken(ctx, otherExpiredId, refreshGrace)
	require.NoError(t, err, "the purge is scoped to one account")
}

// TestDeletingAUserCascadesToItsRefreshTokens verifies deleting an account
// cascades to its refresh token rows.
func TestDeletingAUserCascadesToItsRefreshTokens(t *testing.T) {
	tokenStore, userStore, pool := setupRefreshTokenStore(t)
	ctx := context.Background()
	resetUsers(t, pool)
	insertUser(t, userStore, "remaining-admin@example.com", true)
	user := insertUser(t, userStore, "ledger@example.com", false)

	tokenId := insertRefreshToken(t, tokenStore, user.Id, uuid.NewString(), time.Now().Add(time.Hour))
	require.NoError(t, userStore.DeleteUserByID(ctx, user.Id))

	notFoundErr := (*store.ErrResourceNotFound)(nil)
	_, err := tokenStore.GetRefreshToken(ctx, tokenId, refreshGrace)
	require.ErrorAs(t, err, &notFoundErr)
}

// TestConcurrentConsumeClaimsATokenOnce has two requests present the same
// refresh token at the same instant, repeatedly; exactly one must claim it.
func TestConcurrentConsumeClaimsATokenOnce(t *testing.T) {
	tokenStore, userStore, pool := setupRefreshTokenStore(t)
	ctx := context.Background()
	resetUsers(t, pool)
	user := insertUser(t, userStore, "ledger@example.com", false)

	for range 20 {
		tokenId := insertRefreshToken(t, tokenStore, user.Id, uuid.NewString(), time.Now().Add(time.Hour))

		var start sync.WaitGroup
		var done sync.WaitGroup
		start.Add(1)
		results := make([]error, 2)
		for i := range results {
			done.Add(1)
			go func() {
				defer done.Done()
				start.Wait()
				_, results[i] = tokenStore.RotateRefreshToken(ctx, store.RotateRefreshTokenParameters{
					OldID: tokenId, NewID: uuid.NewString(), ExpiresAt: time.Now().Add(time.Hour),
				})
			}()
		}
		start.Done()
		done.Wait()

		claimed := 0
		for _, err := range results {
			if err == nil {
				claimed++
				continue
			}
			notFoundErr := (*store.ErrResourceNotFound)(nil)
			require.ErrorAs(t, err, &notFoundErr)
		}
		require.Equal(t, 1, claimed, "exactly one caller may claim a refresh token")
	}
}

func TestBumpUserSessionVersion(t *testing.T) {
	_, userStore, pool := setupRefreshTokenStore(t)
	ctx := context.Background()
	resetUsers(t, pool)
	user := insertUser(t, userStore, "ledger@example.com", false)

	// Read back rather than trusting the insert's return value, which does not
	// carry the column.
	fresh, err := userStore.GetUserByID(ctx, user.Id)
	require.NoError(t, err)
	require.EqualValues(t, 0, fresh.SessionVersion)

	require.NoError(t, userStore.BumpUserSessionVersion(ctx, user.Id))
	bumped, err := userStore.GetUserByID(ctx, user.Id)
	require.NoError(t, err)
	assert.EqualValues(t, 1, bumped.SessionVersion)

	notFoundErr := (*store.ErrResourceNotFound)(nil)
	require.ErrorAs(t, userStore.BumpUserSessionVersion(ctx, uuid.NewString()), &notFoundErr)
}
