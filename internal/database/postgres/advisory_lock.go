package postgres

import (
	"context"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TryAdvisoryLock claims a session advisory lock, or reports that another
// process already holds it. It is how a background worker that must run on one
// replica at a time elects itself, without the cost of a transaction: a
// transaction-scoped lock would have to stay open for the whole job, and a job
// that talks to something slow would then pin a transaction id, which is what
// stops vacuum from advancing.
//
// The lock belongs to the CONNECTION that took it, so the connection is pinned
// out of the pool for the whole job rather than left to the pool where every
// query may land on a different session.
//
// db is the engine's handle. Anything that is not a pool has no session to pin,
// which is the stateless and test wiring: the lock is then a no-op and the
// caller runs unlocked. Every caller so far is safe that way, because what the
// lock spares is duplicated work, never correctness.
//
// The returned release is safe to call twice. Releasing a pooled connection
// twice panics the process, and a helper handed to a caller should not turn a
// duplicated defer into a crash.
func TryAdvisoryLock(ctx context.Context, db any, lockID int64, name string) (release func(), locked bool, err error) {
	pool, isPool := db.(*pgxpool.Pool)
	if !isPool {
		return func() {}, true, nil
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("acquiring a connection for the %s lock: %w", name, err)
	}
	var held bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", lockID).Scan(&held); err != nil {
		conn.Release()
		return nil, false, fmt.Errorf("taking the %s lock: %w", name, err)
	}
	if !held {
		conn.Release()
		return nil, false, nil
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			// Background context: the unlock must run even when the job's own
			// context is already cancelled. A session that failed to unlock
			// must not go back to the pool still holding the lock, since the
			// lock would then leak for the life of the connection, so it is
			// closed instead: ending a session releases every advisory lock it
			// holds.
			if _, err := conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", lockID); err != nil {
				_ = conn.Conn().Close(context.Background())
			}
			conn.Release()
		})
	}, true, nil
}
