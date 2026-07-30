package bucket

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"xprem/internal/providers/aws"

	"cloud.google.com/go/storage"
	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"golang.org/x/sync/errgroup"
	"google.golang.org/api/iterator"
)

// v2_migration.go, one-shot data re-path from the v1 bucket layout
// ({prefix}/{branch}/{rv}/{updateId}/…) to the v2 layout
// ({prefix}/{appId}/{branch}/{rv}/{updateId}/…). Driven by the migration
// 20260422_v2_scope_data_under_appid registered in internal/bucketmigrations/.
//
// Each backend exposes a MoveRootEntriesUnder(appId) method that is
// idempotent under interruption: entries already moved are detected
// (the appId prefix is preserved on retry) and re-running the migration
// only processes what's still at the root.
//
// .migrationhistory itself is deployment-global and explicitly excluded -
// it must stay at the bucket root so the migration ledger keeps working
// across deploys.

// ErrAppIdCollidesWithV1Branch is returned when a v1 bucket contains a
// branch whose name happens to equal the target appId. Auto-migrating
// would nest v1 data under itself and corrupt the layout, so the
// migration refuses and the operator renames the branch before retrying.
//
// EXPO_APP_ID is an Expo project id and therefore a UUID, so a branch
// colliding with it is close to unreachable in practice, the guard is
// here because the corruption it prevents is silent and unrecoverable,
// not because it is expected to fire.
var ErrAppIdCollidesWithV1Branch = fmt.Errorf("app id collides with a v1 branch of the same name; rename that branch in the bucket, then reboot to retry the migration")

// migrationConcurrency is how many objects MoveRootEntriesUnder moves in
// parallel on the remote backends, tunable with BUCKET_MIGRATION_CONCURRENCY.
// Copy+delete is a pair of network round-trips per object, so the move is
// latency-bound: the default cuts hours to minutes on large buckets while
// staying far below S3/GCS per-prefix rate limits.
func migrationConcurrency() int {
	if v := os.Getenv("BUCKET_MIGRATION_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
		log.Printf("⚠️ Ignoring invalid BUCKET_MIGRATION_CONCURRENCY=%q, using the default", v)
	}
	return 16
}

// moveProgress counts moved objects across workers and logs a heartbeat line
// every 500 objects, so a long migration shows life in the pod logs instead
// of going silent for its whole duration.
type moveProgress struct {
	moved atomic.Int64
}

func (p *moveProgress) tick() {
	if n := p.moved.Add(1); n%500 == 0 {
		log.Printf("🚚 [BUCKET] v1 re-path progress: %d objects moved...", n)
	}
}

// MoveRootEntriesUnder walks the LocalBucket root and moves every
// immediate child directory that LOOKS LIKE a v1 branch into
// {rootPath}/{appId}/. An entry is considered a v1 branch when it
// contains a .check or update-metadata.json file at depth 3, the exact
// shape produced by the v1 publish pipeline
// ({branch}/{runtimeVersion}/{updateId}/.check). v2 directories hold the
// same files at depth 4 ({appId}/{branch}/{rv}/{updateId}/.check) and so
// are correctly identified as non-branch-shaped and left alone. Uses
// os.Rename per entry, atomic on POSIX when src/dst share a filesystem.
func (b *LocalBucket) MoveRootEntriesUnder(appId string) error {
	root := b.rootPath()
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", root, err)
	}

	// Pre-flight collision check: if {root}/{appId}/ itself has the
	// shape of a v1 branch (marker at depth 2 inside it), it was a
	// coincidentally-named v1 branch BEFORE this migration started.
	// looksLikeV1Branch walks depth 3 from the root, so it returns
	// true for a v1 branch named appId and false for a v2-shaped
	// {appId}/ (which has its marker one level deeper).
	if b.looksLikeV1Branch(appId) {
		return fmt.Errorf("%w: %q", ErrAppIdCollidesWithV1Branch, appId)
	}

	// Figure out what actually needs moving before creating the target
	// dir, on a bucket that's already fully v2 we don't want to litter
	// an empty {appId}/ entry.
	var toMove []string
	for _, e := range entries {
		name := e.Name()
		if name == appId || name == ".migrationhistory" || !e.IsDir() {
			continue
		}
		if b.looksLikeV1Branch(name) {
			toMove = append(toMove, name)
		}
	}
	if len(toMove) == 0 {
		return nil
	}

	targetDir := filepath.Join(root, appId)
	if err := os.MkdirAll(targetDir, os.ModePerm); err != nil {
		return fmt.Errorf("mkdir %s: %w", targetDir, err)
	}
	for _, name := range toMove {
		src := filepath.Join(root, name)
		dst := filepath.Join(targetDir, name)
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("move %s: %w", name, err)
		}
	}
	return nil
}

// looksLikeV1Branch returns true when {rootPath}/{name} contains a .check
// or update-metadata.json file at depth 3 (branch/rv/updateId/.check).
// The check short-circuits on the first match so a branch with many
// runtime versions is cheap to classify.
func (b *LocalBucket) looksLikeV1Branch(name string) bool {
	branchDir := filepath.Join(b.rootPath(), name)
	rvs, err := os.ReadDir(branchDir)
	if err != nil {
		return false
	}
	for _, rv := range rvs {
		if !rv.IsDir() {
			continue
		}
		updates, err := os.ReadDir(filepath.Join(branchDir, rv.Name()))
		if err != nil {
			continue
		}
		for _, u := range updates {
			if !u.IsDir() {
				continue
			}
			updateDir := filepath.Join(branchDir, rv.Name(), u.Name())
			if _, err := os.Stat(filepath.Join(updateDir, ".check")); err == nil {
				return true
			}
			if _, err := os.Stat(filepath.Join(updateDir, "update-metadata.json")); err == nil {
				return true
			}
		}
	}
	return false
}

// MoveRootEntriesUnder copies every object that belongs to a confirmed v1
// branch into the new {KeyPrefix}{appId}/ namespace, then deletes the
// source. Copy+delete is S3's only move primitive; each object copy is
// its own atomic API call, so interruptions leave the bucket in a
// consistent (maybe duplicated) state and re-running converges.
//
// Two-pass structural detection: first pass collects confirmed v1
// (branch, rv, updateId) triples by looking for a v1-only marker file
// (.check or update-metadata.json) at exactly segment 4. Second pass
// moves only objects whose first 3 segments land in a confirmed triple.
// This is the equivalent of looksLikeV1Branch for LocalBucket and is
// what keeps a bucket co-hosting v2 data for other apps safe, those
// keys have their marker at segment 5, so their triple never gets
// confirmed and they are left alone.
func (b *S3Bucket) MoveRootEntriesUnder(appId string) error {
	client, err := aws.GetS3Client()
	if err != nil {
		return err
	}
	ctx := context.TODO()
	appPrefix := b.prefixedKey(appId + "/")

	// Pre-flight collision check: scan objects under appPrefix for a v1
	// marker shape ({appId}/{rv}/{updateId}/.check, 4 segments after
	// the key prefix). v2 objects under the same appPrefix sit at 5
	// segments and are ignored here.
	pc := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{
		Bucket: awssdk.String(b.BucketName),
		Prefix: awssdk.String(appPrefix),
	})
	for pc.HasMorePages() {
		page, err := pc.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("list objects: %w", err)
		}
		for _, obj := range page.Contents {
			relKey := strings.TrimPrefix(*obj.Key, b.KeyPrefix)
			if _, ok := v1BranchTripleFromMarker(relKey); ok {
				return fmt.Errorf("%w: %q", ErrAppIdCollidesWithV1Branch, appId)
			}
		}
	}

	confirmed := map[string]bool{}
	p1 := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{
		Bucket: awssdk.String(b.BucketName),
		Prefix: awssdk.String(b.KeyPrefix),
	})
	for p1.HasMorePages() {
		page, err := p1.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("list objects: %w", err)
		}
		for _, obj := range page.Contents {
			key := *obj.Key
			if strings.HasPrefix(key, appPrefix) {
				continue
			}
			relKey := strings.TrimPrefix(key, b.KeyPrefix)
			if triple, ok := v1BranchTripleFromMarker(relKey); ok {
				confirmed[triple] = true
			}
		}
	}
	if len(confirmed) == 0 {
		return nil
	}

	progress := &moveProgress{}
	moveKey := func(ctx context.Context, key string) error {
		newKey := appPrefix + strings.TrimPrefix(key, b.KeyPrefix)

		// CopySource is `bucket/key` with ONLY the key URL-escaped.
		// url.PathEscape(bucket+"/"+key) would also escape the
		// bucket/key separator, producing an invalid CopySource that
		// S3 rejects with InvalidArgument.
		source := b.BucketName + "/" + escapeKeyForCopySource(key)
		if _, err := client.CopyObject(ctx, &s3.CopyObjectInput{
			Bucket:     awssdk.String(b.BucketName),
			CopySource: awssdk.String(source),
			Key:        awssdk.String(newKey),
		}); err != nil {
			return fmt.Errorf("copy %s -> %s: %w", key, newKey, err)
		}
		if _, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: awssdk.String(b.BucketName),
			Key:    awssdk.String(key),
		}); err != nil {
			return fmt.Errorf("delete %s after copy: %w", key, err)
		}
		progress.tick()
		return nil
	}

	// Pass 2 moves objects concurrently (the move is latency-bound, see
	// migrationConcurrency), with one deliberate exception: marker files are
	// collected and moved only after every other object has landed. The
	// markers are what pass 1 uses to confirm a triple, so as long as a
	// marker is still at the root, an interrupted run re-confirms its triple
	// on retry and finishes the job. Moving markers alongside their siblings
	// could strand assets at the root with nothing left to re-confirm them.
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(migrationConcurrency())
	var markers []string
	var listErr error
	p2 := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{
		Bucket: awssdk.String(b.BucketName),
		Prefix: awssdk.String(b.KeyPrefix),
	})
	for p2.HasMorePages() {
		page, err := p2.NextPage(gctx)
		if err != nil {
			listErr = fmt.Errorf("list objects: %w", err)
			break
		}
		for _, obj := range page.Contents {
			key := *obj.Key
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
	}
	// A worker error cancels gctx, which also aborts the listing above; the
	// worker error is the interesting one, so report it first.
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

// v1BranchTripleFromMarker returns (triple, true) iff relKey is exactly
// {branch}/{rv}/{updateId}/{marker} where {marker} is a v1-only sentinel
// file. The 4-segment requirement is important: the same sentinel at
// segment 5 ({appId}/{branch}/{rv}/{updateId}/.check) identifies a v2
// branch belonging to some OTHER app and must not seed a triple here,
// otherwise we'd re-parent that app's data under the current appId.
func v1BranchTripleFromMarker(relKey string) (string, bool) {
	parts := strings.Split(relKey, "/")
	if len(parts) != 4 {
		return "", false
	}
	if parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", false
	}
	if parts[3] != ".check" && parts[3] != "update-metadata.json" {
		return "", false
	}
	return parts[0] + "/" + parts[1] + "/" + parts[2], true
}

// inConfirmedTriple returns true when relKey's first three segments
// match a triple that was positively confirmed as v1 in pass 1. Any
// depth under the triple is allowed, v1 nested assets like
// branch/rv/updateId/assets/foo.png must be moved along with their
// branch.
func inConfirmedTriple(relKey string, confirmed map[string]bool) bool {
	parts := strings.SplitN(relKey, "/", 4)
	if len(parts) < 4 {
		return false
	}
	if parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return false
	}
	return confirmed[parts[0]+"/"+parts[1]+"/"+parts[2]]
}

// escapeKeyForCopySource URL-escapes an S3 object key for use in the
// CopySource header. The key is escaped per path segment so literal
// slashes separating segments survive, url.PathEscape on the whole key
// would turn them into %2F, which S3 accepts as part of the key but not
// as a bucket/key separator.
func escapeKeyForCopySource(key string) string {
	segs := strings.Split(key, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

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

// UnwrapBucket returns the underlying concrete backend when b is a
// validatingBucket decorator. External packages (the migration) need this
// so they can type-assert on *LocalBucket / *S3Bucket / *GCSBucket to
// call MoveRootEntriesUnder without going through the validating
// middleware (which would reject the root-level listing).
func UnwrapBucket(b Bucket) Bucket {
	if vb, ok := b.(*validatingBucket); ok {
		return vb.Inner
	}
	return b
}
