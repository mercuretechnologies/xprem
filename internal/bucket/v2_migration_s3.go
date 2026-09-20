package bucket

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"
	"xprem/internal/providers/aws"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"golang.org/x/sync/errgroup"
)

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
			Bucket:      awssdk.String(b.BucketName),
			CopySource:  awssdk.String(source),
			Key:         awssdk.String(newKey),
			IfNoneMatch: awssdk.String("*"),
		}); err != nil {
			var apiErr smithy.APIError
			if !errors.As(err, &apiErr) || apiErr.ErrorCode() != "PreconditionFailed" {
				return fmt.Errorf("copy %s -> %s: %w", key, newKey, err)
			}
			// A previous run may have copied this object before failing to
			// delete its source. Compare bytes before completing that move;
			// ETags can change when S3 copies a multipart object.
			sourceHash, readErr := migrationObjectHash(ctx, client, b.BucketName, key)
			if readErr != nil {
				return readErr
			}
			destinationHash, readErr := migrationObjectHash(ctx, client, b.BucketName, newKey)
			if readErr != nil {
				return readErr
			}
			if !bytes.Equal(sourceHash, destinationHash) {
				return fmt.Errorf("copy %s -> %s: destination differs from source: %w", key, newKey, err)
			}
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

func migrationObjectHash(ctx context.Context, client *s3.Client, bucketName, key string) ([]byte, error) {
	object, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: awssdk.String(bucketName),
		Key:    awssdk.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("read %s to verify existing migration copy: %w", key, err)
	}
	defer object.Body.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, object.Body); err != nil {
		return nil, fmt.Errorf("read %s to verify existing migration copy: %w", key, err)
	}
	return hash.Sum(nil), nil
}
