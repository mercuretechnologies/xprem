package bucket

import (
	"fmt"
	"os"
	"path/filepath"
)

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
