package bucket

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"xprem/internal/helpers"
	"xprem/internal/types"
)

func (b *LocalBucket) DeleteUpdateFolder(appId string, branch string, runtimeVersion string, updateId string) error {
	if b.BasePath == "" {
		return errors.New("BasePath not set")
	}
	dirPath := filepath.Join(b.rootPath(), appId, branch, runtimeVersion, updateId)
	return os.RemoveAll(dirPath)
}

func (b *LocalBucket) RequestUploadUrlForFileUpdate(appId string, branch string, runtimeVersion string, updateId string, fileName string) (*UploadRequest, error) {
	if b.BasePath == "" {
		return nil, errors.New("BasePath not set")
	}
	dirPath := filepath.Join(b.rootPath(), appId, branch, runtimeVersion, updateId)
	err := os.MkdirAll(dirPath, 0o700)
	if err != nil {
		return nil, err
	}
	return b.localUploadRequest(appId, branch, filepath.Join(dirPath, fileName))
}

func (b *LocalBucket) GetUpdates(appId string, branch string, runtimeVersion string) ([]types.Update, error) {
	if b.BasePath == "" {
		return nil, errors.New("BasePath not set")
	}
	dirPath := filepath.Join(b.rootPath(), appId, branch, runtimeVersion)
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []types.Update{}, nil
		}
		return nil, err
	}
	var updates []types.Update
	for _, entry := range entries {
		if entry.IsDir() {
			updateId, err := strconv.ParseInt(entry.Name(), 10, 64)
			if err == nil {
				updates = append(updates, types.Update{
					AppId:          appId,
					Branch:         branch,
					RuntimeVersion: runtimeVersion,
					UpdateId:       strconv.FormatInt(updateId, 10),
					CreatedAt:      helpers.NormalizeTimestampToDuration(updateId),
				})
			}
		}
	}
	return updates, nil
}

func (b *LocalBucket) GetFile(update types.Update, assetPath string) (*types.BucketFile, error) {
	if b.BasePath == "" {
		return nil, errors.New("BasePath not set")
	}

	expectedBase := filepath.Join(b.rootPath(), update.AppId, update.Branch, update.RuntimeVersion, update.UpdateId)
	filePath := filepath.Join(expectedBase, assetPath)
	// Use filepath.Rel so sibling dirs sharing a string prefix (e.g. ".../123" vs ".../1234")
	// aren't treated as nested, and so "." (the base itself) is accepted.
	rel, err := filepath.Rel(expectedBase, filePath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return nil, errors.New("invalid asset path")
	}

	file, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}

	return &types.BucketFile{
		Reader:    file,
		CreatedAt: info.ModTime(),
	}, nil
}

func (b *LocalBucket) GetBranches(appId string) ([]string, error) {
	if b.BasePath == "" {
		return nil, errors.New("BasePath not set")
	}
	entries, err := os.ReadDir(filepath.Join(b.rootPath(), appId))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var branches []string
	for _, entry := range entries {
		if entry.IsDir() {
			branches = append(branches, entry.Name())
		}
	}
	return branches, nil
}

func (b *LocalBucket) GetRuntimeVersions(appId string, branch string) ([]types.RuntimeVersionWithStats, error) {
	if b.BasePath == "" {
		return nil, errors.New("BasePath not set")
	}
	dirPath := filepath.Join(b.rootPath(), appId, branch)
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, err
	}
	var runtimeVersions []types.RuntimeVersionWithStats
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		runtimeVersion := entry.Name()
		updatesPath := filepath.Join(dirPath, runtimeVersion)
		updates, err := os.ReadDir(updatesPath)
		if err != nil {
			continue
		}
		var updateTimestamps []int64
		for _, update := range updates {
			if !update.IsDir() {
				continue
			}
			if _, err := os.Stat(filepath.Join(updatesPath, update.Name(), ".check")); err != nil {
				continue
			}
			timestamp, err := strconv.ParseInt(update.Name(), 10, 64)
			if err != nil {
				continue
			}
			updateTimestamps = append(updateTimestamps, timestamp)
		}
		if len(updateTimestamps) == 0 {
			continue
		}

		sort.Slice(updateTimestamps, func(i, j int) bool { return updateTimestamps[i] < updateTimestamps[j] })

		runtimeVersions = append(runtimeVersions, types.RuntimeVersionWithStats{
			RuntimeVersion:  runtimeVersion,
			CreatedAt:       helpers.NormalizeTimestamp(updateTimestamps[0]).Format(time.RFC3339),
			LastUpdatedAt:   helpers.NormalizeTimestamp(updateTimestamps[len(updateTimestamps)-1]).Format(time.RFC3339),
			NumberOfUpdates: len(updateTimestamps),
		})
	}

	return runtimeVersions, nil
}

func (b *LocalBucket) UploadFileIntoUpdate(update types.Update, fileName string, file io.Reader) error {
	filePath := filepath.Join(b.rootPath(), update.AppId, update.Branch, update.RuntimeVersion, update.UpdateId, fileName)
	err := os.MkdirAll(filepath.Dir(filePath), 0o700)
	if err != nil {
		return err
	}
	out, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, file)
	if err != nil {
		return err
	}
	return nil
}

func (b *LocalBucket) CopyFileIntoUpdate(source types.Update, target types.Update, fileName string) error {
	sourcePath := filepath.Join(b.rootPath(), source.AppId, source.Branch, source.RuntimeVersion, source.UpdateId, fileName)
	in, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer in.Close()
	return b.UploadFileIntoUpdate(target, fileName, in)
}

func (b *LocalBucket) CreateUpdateFrom(previousUpdate *types.Update, newUpdateId string) (*types.Update, error) {
	if previousUpdate == nil {
		return nil, errors.New("previousUpdate is nil")
	}
	if previousUpdate.UpdateId == "" {
		return nil, errors.New("previousUpdate.UpdateId is empty")
	}
	if newUpdateId == "" {
		return nil, errors.New("newUpdateId is empty")
	}

	previousUpdatePath := filepath.Join(b.rootPath(), previousUpdate.AppId, previousUpdate.Branch, previousUpdate.RuntimeVersion, previousUpdate.UpdateId)
	newUpdatePath := filepath.Join(b.rootPath(), previousUpdate.AppId, previousUpdate.Branch, previousUpdate.RuntimeVersion, newUpdateId)

	err := os.MkdirAll(newUpdatePath, 0o700)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(previousUpdatePath)
	if err != nil {
		return nil, err
	}

	var wg sync.WaitGroup
	errChan := make(chan error, len(entries))
	sem := make(chan struct{}, runtime.NumCPU())

	for _, entry := range entries {
		name := entry.Name()
		if name == "update-metadata.json" || name == ".check" {
			continue
		}

		srcPath := filepath.Join(previousUpdatePath, name)
		dstPath := filepath.Join(newUpdatePath, name)

		wg.Add(1)
		go func(entry fs.DirEntry, src, dst string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			var err error
			if entry.IsDir() {
				err = copyDirParallel(src, dst)
			} else {
				err = copyFile(src, dst)
			}
			if err != nil {
				errChan <- err
			}
		}(entry, srcPath, dstPath)
	}

	wg.Wait()
	close(errChan)

	for e := range errChan {
		if e != nil {
			return nil, e
		}
	}

	updateId, err := strconv.ParseInt(newUpdateId, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("error parsing update ID: %w", err)
	}
	return &types.Update{
		AppId:          previousUpdate.AppId,
		Branch:         previousUpdate.Branch,
		RuntimeVersion: previousUpdate.RuntimeVersion,
		UpdateId:       newUpdateId,
		CreatedAt:      helpers.NormalizeTimestampToDuration(updateId),
	}, nil
}

func copyDirParallel(srcDir, dstDir string) error {
	err := os.MkdirAll(dstDir, 0o700)
	if err != nil {
		return err
	}

	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return err
	}

	var wg sync.WaitGroup
	errChan := make(chan error, len(entries))
	sem := make(chan struct{}, runtime.NumCPU())
	for _, entry := range entries {
		srcPath := filepath.Join(srcDir, entry.Name())
		dstPath := filepath.Join(dstDir, entry.Name())

		wg.Add(1)
		go func(entry fs.DirEntry, src, dst string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			var err error
			if entry.IsDir() {
				err = copyDirParallel(src, dst)
			} else {
				err = copyFile(src, dst)
			}
			if err != nil {
				errChan <- err
			}
		}(entry, srcPath, dstPath)
	}
	wg.Wait()
	close(errChan)
	for e := range errChan {
		if e != nil {
			return e
		}
	}
	return nil
}
