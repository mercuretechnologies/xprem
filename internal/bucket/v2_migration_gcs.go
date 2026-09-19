package bucket

import (
	"context"
	"fmt"
	"strings"

	"cloud.google.com/go/storage"
	"golang.org/x/sync/errgroup"
	"google.golang.org/api/iterator"
)

// MoveRootEntriesUnder mirrors the S3 strategy on GCS: two-pass
// structural detection (see S3.MoveRootEntriesUnder for the rationale),
// then copy via CopierFrom and delete source. GCS's native rewrite
// handles large-object semantics within a single bucket.
func (b *GCSBucket) MoveRootEntriesUnder(appId string) error {
	ctx := context.Background()
	bh, err := b.bucketHandle(ctx)
	if err != nil {
		return err
	}
	appPrefix := b.prefixedKey(appId + "/")

	// Pre-flight collision check (see S3 implementation for rationale).
	itCol := bh.Objects(ctx, &storage.Query{Prefix: appPrefix})
	for {
		attrs, err := itCol.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return fmt.Errorf("list objects: %w", err)
		}
		relKey := strings.TrimPrefix(attrs.Name, b.KeyPrefix)
		if _, ok := v1BranchTripleFromMarker(relKey); ok {
			return fmt.Errorf("%w: %q", ErrAppIdCollidesWithV1Branch, appId)
		}
	}

	confirmed := map[string]bool{}
	it1 := bh.Objects(ctx, &storage.Query{Prefix: b.KeyPrefix})
	for {
		attrs, err := it1.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return fmt.Errorf("list objects: %w", err)
		}
		key := attrs.Name
		if strings.HasPrefix(key, appPrefix) {
			continue
		}
		relKey := strings.TrimPrefix(key, b.KeyPrefix)
		if triple, ok := v1BranchTripleFromMarker(relKey); ok {
			confirmed[triple] = true
		}
	}
	if len(confirmed) == 0 {
		return nil
	}

	progress := &moveProgress{}
	moveKey := func(ctx context.Context, key string) error {
		newKey := appPrefix + strings.TrimPrefix(key, b.KeyPrefix)
		src := bh.Object(key)
		dst := bh.Object(newKey)
		if _, err := dst.CopierFrom(src).Run(ctx); err != nil {
			return fmt.Errorf("copy %s -> %s: %w", key, newKey, err)
		}
		if err := src.Delete(ctx); err != nil {
			return fmt.Errorf("delete %s after copy: %w", key, err)
		}
		progress.tick()
		return nil
	}

	// Same concurrent pass-2 strategy as S3, markers-last included; see the
	// S3 implementation for the rationale.
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(migrationConcurrency())
	var markers []string
	var listErr error
	it2 := bh.Objects(gctx, &storage.Query{Prefix: b.KeyPrefix})
	for {
		attrs, err := it2.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			listErr = fmt.Errorf("list objects: %w", err)
			break
		}
		key := attrs.Name
		if strings.HasPrefix(key, appPrefix) {
			continue
		}
		relKey := strings.TrimPrefix(key, b.KeyPrefix)
		if relKey == ".migrationhistory" {
			continue
		}
		if !inConfirmedTriple(relKey, confirmed) {
			continue
		}
		if _, isMarker := v1BranchTripleFromMarker(relKey); isMarker {
			markers = append(markers, key)
			continue
		}
		g.Go(func() error { return moveKey(gctx, key) })
	}
	if err := g.Wait(); err != nil {
		return err
	}
	if listErr != nil {
		return listErr
	}

	gm, gmctx := errgroup.WithContext(ctx)
	gm.SetLimit(migrationConcurrency())
	for _, key := range markers {
		gm.Go(func() error { return moveKey(gmctx, key) })
	}
	return gm.Wait()
}
