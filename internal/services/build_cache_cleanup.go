package services

import (
	"context"
	"log"
	"xprem/internal/bucket"
	"xprem/internal/database/postgres/pgdb"
)

func (c *BuildCleanup) SweepCache(ctx context.Context) (int, error) {
	q := pgdb.New(c.db)
	for {
		expired, err := q.ExpireBuildCacheObjects(ctx)
		if err != nil {
			return 0, err
		}
		if expired < 100 {
			break
		}
	}
	total := 0
	for {
		processed, deleted, err := c.sweepCacheBatch(ctx)
		total += deleted
		if err != nil || processed < 4 {
			return total, err
		}
	}
}

// Commit each small batch so a slow bucket cannot roll back earlier deletions.
func (c *BuildCleanup) sweepCacheBatch(ctx context.Context) (int, int, error) {
	tx, err := c.db.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback(ctx)
	q := pgdb.New(tx)
	objects, err := q.DueBuildCacheCleanup(ctx)
	if err != nil {
		return 0, 0, err
	}
	deleted := 0
	for _, object := range objects {
		itemCtx, cancel := context.WithTimeout(ctx, buildCleanupItemTimeout)
		err = c.storage.DeleteBuildCache(itemCtx, bucket.BuildCacheObject{AppID: object.AppID.String(), IdentifierID: object.AppIdentifierID.String(), Namespace: object.Namespace, ID: object.ID.String()})
		cancel()
		if err != nil {
			log.Printf("[BUILD-CACHE] object %s cleanup failed: %v", object.ID.String(), err)
			err = q.RetryBuildCacheCleanup(ctx, object.ID)
		} else {
			err = q.DeleteBuildCacheCleanup(ctx, object.ID)
			deleted++
		}
		if err != nil {
			return len(objects), deleted, err
		}
	}
	return len(objects), deleted, tx.Commit(ctx)
}
