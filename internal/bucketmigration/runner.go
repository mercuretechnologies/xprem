package bucketmigration

import (
	"fmt"
	"log"
	"time"
	"xprem/internal/bucket"
	"xprem/internal/cache"
)

const (
	lockKey = "migration-lock"
	// The TTL only has to cover the gap between two renewals plus scheduling
	// jitter: the holder renews the lock every lockRenewInterval for as long
	// as a migration is running. A holder that dies stops renewing, so the
	// lock frees itself within one TTL and a waiting replica picks the work
	// up where the idempotent migration left off.
	lockTTLSeconds    = 60
	lockRenewInterval = 20 * time.Second
	// How often waiting replicas re-read the migration history to see if the
	// holder is done. One small bucket read per tick.
	waitPollInterval = 5 * time.Second
)

// Pending returns the registered migrations that are not yet recorded in the
// bucket's migration history, in application order.
func Pending(b bucket.Bucket) ([]Migration, error) {
	applied, err := b.RetrieveMigrationHistory()
	if err != nil {
		return nil, fmt.Errorf("read history: %w", err)
	}
	appliedSet := make(map[string]bool, len(applied))
	for _, id := range applied {
		appliedSet[id] = true
	}
	var pending []Migration
	for _, m := range All() {
		if !appliedSet[m.ID()] {
			pending = append(pending, m)
		}
	}
	return pending, nil
}

func RunMigrations(b bucket.Bucket) error {
	pending, err := Pending(b)
	if err != nil {
		return err
	}
	for _, m := range pending {
		fmt.Printf("🔼 Applying migration: %s\n", m.ID())
		if err := m.Up(b); err != nil {
			return fmt.Errorf("migration %s failed: %w", m.ID(), err)
		}
		if err := b.ApplyMigration(m.ID()); err != nil {
			return fmt.Errorf("record migration %s: %w", m.ID(), err)
		}
	}
	return nil
}

func RollbackLastMigration(b bucket.Bucket) error {
	ag, err := b.RetrieveMigrationHistory()
	if err != nil {
		return fmt.Errorf("read history: %w", err)
	}
	if len(ag) == 0 {
		fmt.Println("No migration to rollback.")
		return nil
	}
	last := ag[len(ag)-1]
	var target Migration
	for _, m := range All() {
		if m.ID() == last {
			target = m
			break
		}
	}
	if target == nil {
		return fmt.Errorf("migration %s not found", last)
	}
	fmt.Printf("🔽 Rolling back: %s\n", last)
	if err := target.Down(b); err != nil {
		return fmt.Errorf("rollback %s failed: %w", last, err)
	}
	return b.RemoveMigrationFromHistory(last)
}

// EnsureMigrations brings the bucket fully up to date before the caller starts
// serving traffic. Exactly one instance applies the pending migrations, under
// a cache lock it keeps renewing while the work runs; every other instance
// waits here until the history shows nothing pending, instead of skipping
// ahead and serving from a half-migrated bucket. The lock is always released
// when the holder is done, on failure included, so a crashed attempt can be
// retried by the very next boot instead of sitting out a stale lock.
func EnsureMigrations() error {
	b := bucket.GetBucket()
	c := cache.GetCache()
	waiting := false
	for {
		pending, err := Pending(b)
		if err != nil {
			return err
		}
		if len(pending) == 0 {
			if waiting {
				log.Println("✅ [BUCKET] Migrations finished on another instance, resuming boot.")
			}
			return nil
		}
		ok, err := c.TryLock(lockKey, lockTTLSeconds)
		if err != nil {
			return fmt.Errorf("acquire migration lock: %w", err)
		}
		if !ok {
			if !waiting {
				log.Printf("⏳ [BUCKET] %d migration(s) pending but another instance holds the lock. Holding boot until it finishes...", len(pending))
				waiting = true
			}
			time.Sleep(waitPollInterval)
			continue
		}
		log.Printf("✅ [BUCKET] Migration lock acquired, applying %d migration(s)...", len(pending))
		if err := runWhileRenewingLock(b, c); err != nil {
			return err
		}
		log.Println("🎉 [BUCKET] Migrations completed successfully.")
		return nil
	}
}

// runWhileRenewingLock runs the pending migrations while a background ticker
// keeps the cache lock alive, then releases the lock whatever the outcome.
func runWhileRenewingLock(b bucket.Bucket, c cache.Cache) error {
	stopRenew := make(chan struct{})
	renewDone := make(chan struct{})
	go func() {
		defer close(renewDone)
		ticker := time.NewTicker(lockRenewInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stopRenew:
				return
			case <-ticker.C:
				ttl := lockTTLSeconds
				if err := c.Set(lockKey, "locked", &ttl); err != nil {
					log.Printf("⚠️ [BUCKET] Failed to renew the migration lock: %v", err)
				}
			}
		}
	}()
	err := RunMigrations(b)
	close(stopRenew)
	<-renewDone
	c.Delete(lockKey)
	if err != nil {
		return fmt.Errorf("bucket migration failed: %w", err)
	}
	return nil
}
