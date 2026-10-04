package bucket

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"xprem/internal/objectstore"

	"golang.org/x/sync/errgroup"
)

// ErrAppIdCollidesWithV1Branch refuses a re-path that would nest a v1
// branch named like the app under itself.
var ErrAppIdCollidesWithV1Branch = fmt.Errorf("app id collides with a v1 branch of the same name; rename that branch in the bucket, then reboot to retry the migration")

const migrationHistoryKey = ".migrationhistory"

// migrationConcurrency is how many objects moveRootKeysUnder moves in
// parallel, tunable with BUCKET_MIGRATION_CONCURRENCY.
func migrationConcurrency() int {
	if v := os.Getenv("BUCKET_MIGRATION_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
		log.Printf("⚠️ Ignoring invalid BUCKET_MIGRATION_CONCURRENCY=%q, using the default", v)
	}
	return 16
}

type moveProgress struct {
	moved atomic.Int64
}

func (p *moveProgress) tick() {
	if n := p.moved.Add(1); n%500 == 0 {
		log.Printf("🚚 [BUCKET] v1 re-path progress: %d objects moved...", n)
	}
}

// MoveRootEntriesUnder re-paths the v1 layout ({branch}/{rv}/{updateId}/…)
// under {appId}/. The move is idempotent, so an interrupted run converges on
// retry.
func (b *Bucket) MoveRootEntriesUnder(ctx context.Context, appId string) error {
	if b.localRoot != "" {
		return moveLocalRootEntriesUnder(b.localRoot, appId)
	}
	return moveRootKeysUnder(ctx, b.ObjectStore, appId)
}

// moveLocalRootEntriesUnder renames each directory shaped like a v1 branch
// into {root}/{appId}/.
func moveLocalRootEntriesUnder(root, appId string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", root, err)
	}
	// A v1 branch named like the app has its marker one level higher than a
	// v2 {appId}/ directory.
	if looksLikeV1Branch(root, appId) {
		return fmt.Errorf("%w: %q", ErrAppIdCollidesWithV1Branch, appId)
	}
	var toMove []string
	for _, entry := range entries {
		name := entry.Name()
		if name == appId || name == migrationHistoryKey || !entry.IsDir() {
			continue
		}
		if looksLikeV1Branch(root, name) {
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
		if err := os.Rename(filepath.Join(root, name), filepath.Join(targetDir, name)); err != nil {
			return fmt.Errorf("move %s: %w", name, err)
		}
	}
	return nil
}

// looksLikeV1Branch reports whether {root}/{name} holds a .check or
// update-metadata.json at {rv}/{updateId}/.
func looksLikeV1Branch(root, name string) bool {
	branchDir := filepath.Join(root, name)
	runtimeVersions, err := os.ReadDir(branchDir)
	if err != nil {
		return false
	}
	for _, runtimeVersion := range runtimeVersions {
		if !runtimeVersion.IsDir() {
			continue
		}
		updates, err := os.ReadDir(filepath.Join(branchDir, runtimeVersion.Name()))
		if err != nil {
			continue
		}
		for _, update := range updates {
			if !update.IsDir() {
				continue
			}
			updateDir := filepath.Join(branchDir, runtimeVersion.Name(), update.Name())
			for _, marker := range []string{".check", "update-metadata.json"} {
				if _, err := os.Stat(filepath.Join(updateDir, marker)); err == nil {
					return true
				}
			}
		}
	}
	return false
}

// moveRootKeysUnder moves the keys of every confirmed v1 update, each copied
// then deleted, markers last so an interrupted run still finds them on retry.
func moveRootKeysUnder(ctx context.Context, objectStore objectstore.Store, appId string) error {
	appPrefix := appId + "/"

	nested, err := objectStore.List(ctx, appPrefix)
	if err != nil {
		return fmt.Errorf("list objects: %w", err)
	}
	for _, key := range nested {
		if _, ok := v1BranchTripleFromMarker(key); ok {
			return fmt.Errorf("%w: %q", ErrAppIdCollidesWithV1Branch, appId)
		}
	}

	keys, err := objectStore.List(ctx, "")
	if err != nil {
		return fmt.Errorf("list objects: %w", err)
	}
	confirmed := map[string]bool{}
	for _, key := range keys {
		if strings.HasPrefix(key, appPrefix) {
			continue
		}
		if triple, ok := v1BranchTripleFromMarker(key); ok {
			confirmed[triple] = true
		}
	}
	if len(confirmed) == 0 {
		return nil
	}

	progress := &moveProgress{}
	move := func(ctx context.Context, key string) error {
		if err := objectStore.Copy(ctx, key, appPrefix+key); err != nil {
			return err
		}
		if err := objectStore.Delete(ctx, key); err != nil {
			return fmt.Errorf("delete %s after copy: %w", key, err)
		}
		progress.tick()
		return nil
	}

	var markers []string
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(migrationConcurrency())
	for _, key := range keys {
		if strings.HasPrefix(key, appPrefix) || key == migrationHistoryKey || !inConfirmedTriple(key, confirmed) {
			continue
		}
		if _, isMarker := v1BranchTripleFromMarker(key); isMarker {
			markers = append(markers, key)
			continue
		}
		g.Go(func() error { return move(gctx, key) })
	}
	if err := g.Wait(); err != nil {
		return err
	}

	gm, gmctx := errgroup.WithContext(ctx)
	gm.SetLimit(migrationConcurrency())
	for _, key := range markers {
		gm.Go(func() error { return move(gmctx, key) })
	}
	return gm.Wait()
}

// v1BranchTripleFromMarker returns the {branch}/{rv}/{updateId} of a key
// that is a v1 marker file at exactly that depth. The same marker one level
// deeper belongs to a v2 branch of another app.
func v1BranchTripleFromMarker(key string) (string, bool) {
	parts := strings.Split(key, "/")
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

// inConfirmedTriple accepts any depth under the triple: nested v1 assets
// move along with their branch.
func inConfirmedTriple(key string, confirmed map[string]bool) bool {
	parts := strings.SplitN(key, "/", 4)
	if len(parts) < 4 {
		return false
	}
	if parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return false
	}
	return confirmed[parts[0]+"/"+parts[1]+"/"+parts[2]]
}
