package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"
	"unicode/utf8"
	"xprem/internal/bucket"
	"xprem/internal/database"
	"xprem/internal/database/postgres/pgdb"

	"github.com/jackc/pgx/v5/pgtype"
)

const (
	buildCleanupStartDelay     = 30 * time.Second
	buildOutboxInterval        = time.Minute
	buildStagingSweepInterval  = 15 * time.Minute
	buildCleanupBatchTimeout   = 5 * time.Minute
	buildCleanupItemTimeout    = 30 * time.Second
	buildOutboxBatchSize       = 4 // Two deletes per row leave 1 minute for SQL and commit.
	buildStagingSweepBatchSize = 9 // One delete per row leaves 30 seconds for SQL and commit.
	buildOutboxMaxBackoff      = 6 * time.Hour
	buildStagingStaleAfter     = 24 * time.Hour
	buildUnfinishedStaleAfter  = 24 * time.Hour
)

// BuildCleanupStorage removes artifacts and cache objects; absent objects are not an error.
type BuildCleanupStorage interface {
	DeleteBuildArtifact(context.Context, bucket.BuildArtifact, bool) error
	DeleteBuildCache(context.Context, bucket.BuildCacheObject) error
}

// BuildCleanup drains the build_artifact_cleanup outbox, sweeps stale staging
// uploads and fails abandoned builds. Final artifacts of ready builds are never touched.
type BuildCleanup struct {
	db      database.DBTX
	storage BuildCleanupStorage
}

func NewBuildCleanup(db database.DBTX, storage BuildCleanupStorage) *BuildCleanup {
	return &BuildCleanup{db: db, storage: storage}
}

// Start runs the cleanup loops until the returned stop function is called.
func (c *BuildCleanup) Start(parent context.Context) func() {
	ctx, cancel := context.WithCancel(parent)
	var wg sync.WaitGroup
	wg.Add(4)
	go func() {
		defer wg.Done()
		c.loop(ctx, buildStagingSweepInterval, "build cache", c.SweepCache)
	}()
	go func() {
		defer wg.Done()
		c.loop(ctx, buildOutboxInterval, "outbox", c.DrainOutbox)
	}()
	go func() {
		defer wg.Done()
		c.loop(ctx, buildStagingSweepInterval, "staging sweep", c.SweepStaging)
	}()
	go func() {
		defer wg.Done()
		c.loop(ctx, buildStagingSweepInterval, "stale builds", c.FailStaleBuilds)
	}()
	return func() {
		cancel()
		wg.Wait()
	}
}

func (c *BuildCleanup) loop(ctx context.Context, interval time.Duration, name string, run func(context.Context) (int, error)) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(buildCleanupStartDelay):
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		batchCtx, cancel := context.WithTimeout(ctx, buildCleanupBatchTimeout)
		count, err := run(batchCtx)
		cancel()
		if err != nil && ctx.Err() == nil {
			log.Printf("🧹 [BUILD-CLEANUP] %s failed: %v", name, err)
		} else if count > 0 {
			log.Printf("🧹 [BUILD-CLEANUP] %s handled %d builds", name, count)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// DrainOutbox deletes the final and staging objects of one batch of due
// outbox rows and removes each row once both deletes succeeded.
func (c *BuildCleanup) DrainOutbox(ctx context.Context) (int, error) {
	tx, err := c.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	q := pgdb.New(tx)
	rows, err := q.ListDueBuildArtifactCleanup(ctx, buildOutboxBatchSize)
	if err != nil {
		return 0, err
	}
	done := 0
	for _, row := range rows {
		ref := bucket.BuildArtifact{IdentifierID: row.AppIdentifierID.String(), BuildID: row.BuildID.String(), Type: row.ArtifactType}
		if err := c.deleteArtifact(ctx, ref, true, true); err != nil {
			backoff := buildOutboxBackoff(row.Attempts)
			log.Printf("🧹 [BUILD-CLEANUP] build %s artifact delete failed (attempt %d, retry in %s): %v", ref.BuildID, row.Attempts+1, backoff, err)
			lastError := truncateError(err)
			if err := q.DeferBuildArtifactCleanup(ctx, pgdb.DeferBuildArtifactCleanupParams{ID: row.ID, LastError: &lastError, Backoff: pgtype.Interval{Microseconds: backoff.Microseconds(), Valid: true}}); err != nil {
				return done, err
			}
			continue
		}
		if err := q.DeleteBuildArtifactCleanup(ctx, row.ID); err != nil {
			return done, err
		}
		done++
	}
	return done, tx.Commit(ctx)
}

// SweepStaging deletes the staging upload of builds untouched for a day whose
// upload is finished or abandoned. Ready builds are swept once, the others
// again after a day, so a retried upload is not left behind either.
func (c *BuildCleanup) SweepStaging(ctx context.Context) (int, error) {
	tx, err := c.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	q := pgdb.New(tx)
	rows, err := q.ListStaleBuildStaging(ctx, pgdb.ListStaleBuildStagingParams{StaleAfter: pgtype.Interval{Microseconds: buildStagingStaleAfter.Microseconds(), Valid: true}, BatchSize: buildStagingSweepBatchSize})
	if err != nil {
		return 0, err
	}
	swept := 0
	for _, row := range rows {
		ref := bucket.BuildArtifact{IdentifierID: row.AppIdentifierID.String(), BuildID: row.ID.String(), Type: row.ArtifactType}
		if err := c.deleteArtifact(ctx, ref, true, false); err != nil {
			log.Printf("🧹 [BUILD-CLEANUP] build %s staging delete failed: %v", ref.BuildID, err)
			continue
		}
		if err := q.MarkBuildStagingSwept(ctx, row.ID); err != nil {
			return swept, err
		}
		swept++
	}
	return swept, tx.Commit(ctx)
}

// FailStaleBuilds marks failed the builds left building or uploading for a day,
// such as those whose CLI was interrupted before reporting the outcome.
func (c *BuildCleanup) FailStaleBuilds(ctx context.Context) (int, error) {
	count, err := pgdb.New(c.db).FailStaleBuilds(ctx, pgtype.Interval{Microseconds: buildUnfinishedStaleAfter.Microseconds(), Valid: true})
	return int(count), err
}

func (c *BuildCleanup) deleteArtifact(ctx context.Context, ref bucket.BuildArtifact, staging, final bool) error {
	var errs []error
	if staging {
		if err := c.deleteOne(ctx, ref, true); err != nil {
			errs = append(errs, fmt.Errorf("staging: %w", err))
		}
	}
	if final {
		if err := c.deleteOne(ctx, ref, false); err != nil {
			errs = append(errs, fmt.Errorf("final: %w", err))
		}
	}
	return errors.Join(errs...)
}

func (c *BuildCleanup) deleteOne(ctx context.Context, ref bucket.BuildArtifact, staging bool) error {
	itemCtx, cancel := context.WithTimeout(ctx, buildCleanupItemTimeout)
	defer cancel()
	return c.storage.DeleteBuildArtifact(itemCtx, ref, staging)
}

func buildOutboxBackoff(attempts int32) time.Duration {
	if attempts < 0 {
		attempts = 0
	}
	if attempts > 20 {
		return buildOutboxMaxBackoff
	}
	backoff := time.Minute << uint(attempts)
	if backoff > buildOutboxMaxBackoff {
		return buildOutboxMaxBackoff
	}
	return backoff
}

func truncateError(err error) string {
	message := err.Error()
	if len(message) > 1000 {
		end := 1000
		for end > 0 && !utf8.RuneStart(message[end]) {
			end--
		}
		return message[:end]
	}
	return message
}
