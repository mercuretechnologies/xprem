package bucket

import (
	"context"
	"errors"
	"io"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"xprem/internal/helpers"
	"xprem/internal/objectstore"
	"xprem/internal/types"

	"golang.org/x/sync/errgroup"
)

// UpdateStore is the update folder store: {appId}/{branch}/{runtimeVersion}/{updateId}/.
type UpdateStore struct {
	objectStore  objectstore.Store
	localUploads bool
}

// Branches lists the branches of an app; the reserved directories that share
// their level are not branches.
func (u *UpdateStore) Branches(ctx context.Context, appId string) ([]string, error) {
	if err := validateSegment("appId", appId); err != nil {
		return nil, err
	}
	names, err := u.objectStore.ListPrefixes(ctx, appId+"/")
	if err != nil {
		return nil, err
	}
	branches := names[:0]
	for _, name := range names {
		if !ReservedBranchName(name) {
			branches = append(branches, name)
		}
	}
	return branches, nil
}

// RuntimeVersions describes the runtime versions of a branch by their
// published updates; a runtime version with none is left out.
func (u *UpdateStore) RuntimeVersions(ctx context.Context, appId, branch string) ([]types.RuntimeVersionWithStats, error) {
	if err := validateSegment("appId", appId); err != nil {
		return nil, err
	}
	if err := validateBranch(branch); err != nil {
		return nil, err
	}
	branchPrefix := appId + "/" + branch + "/"
	runtimeVersions, err := u.objectStore.ListPrefixes(ctx, branchPrefix)
	if err != nil {
		return nil, err
	}
	var stats []types.RuntimeVersionWithStats
	for _, runtimeVersion := range runtimeVersions {
		updates, err := u.objectStore.ListPrefixes(ctx, branchPrefix+runtimeVersion+"/")
		if err != nil {
			return nil, err
		}
		var timestamps []int64
		for _, updateId := range updates {
			checked, err := u.objectStore.Exists(ctx, updatePrefix(appId, branch, runtimeVersion, updateId)+".check")
			if err != nil {
				return nil, err
			}
			if !checked {
				continue
			}
			timestamp, err := strconv.ParseInt(updateId, 10, 64)
			if err != nil {
				continue
			}
			timestamps = append(timestamps, timestamp)
		}
		if len(timestamps) == 0 {
			continue
		}
		sort.Slice(timestamps, func(i, j int) bool { return timestamps[i] < timestamps[j] })
		stats = append(stats, types.RuntimeVersionWithStats{
			RuntimeVersion:  runtimeVersion,
			CreatedAt:       helpers.NormalizeTimestamp(timestamps[0]).Format(time.RFC3339),
			LastUpdatedAt:   helpers.NormalizeTimestamp(timestamps[len(timestamps)-1]).Format(time.RFC3339),
			NumberOfUpdates: len(timestamps),
		})
	}
	return stats, nil
}

func (u *UpdateStore) List(ctx context.Context, appId, branch, runtimeVersion string) ([]types.Update, error) {
	if err := validateSegment("appId", appId); err != nil {
		return nil, err
	}
	if err := validateBranch(branch); err != nil {
		return nil, err
	}
	if err := validateSegment("runtimeVersion", runtimeVersion); err != nil {
		return nil, err
	}
	names, err := u.objectStore.ListPrefixes(ctx, appId+"/"+branch+"/"+runtimeVersion+"/")
	if err != nil {
		return nil, err
	}
	var updates []types.Update
	for _, name := range names {
		updateId, err := strconv.ParseInt(name, 10, 64)
		if err != nil {
			continue
		}
		updates = append(updates, types.Update{
			AppId:          appId,
			Branch:         branch,
			RuntimeVersion: runtimeVersion,
			UpdateId:       strconv.FormatInt(updateId, 10),
			CreatedAt:      helpers.NormalizeTimestampToDuration(updateId),
		})
	}
	return updates, nil
}

func updateFileKey(update types.Update, path string) (string, error) {
	if err := validateUpdate(&update); err != nil {
		return "", err
	}
	if err := validateRelativePath("assetPath", path); err != nil {
		return "", err
	}
	return updatePrefix(update.AppId, update.Branch, update.RuntimeVersion, update.UpdateId) + path, nil
}

// GetFile returns nil, nil when the update has no such file.
func (u *UpdateStore) GetFile(ctx context.Context, update types.Update, path string) (*types.BucketFile, error) {
	key, err := updateFileKey(update, path)
	if err != nil {
		return nil, err
	}
	return u.objectStore.Get(ctx, key)
}

func (u *UpdateStore) PutFile(ctx context.Context, update types.Update, path string, body io.Reader) error {
	key, err := updateFileKey(update, path)
	if err != nil {
		return err
	}
	return u.objectStore.Put(ctx, key, body)
}

func (u *UpdateStore) PresignPut(ctx context.Context, appId, branch, runtimeVersion, updateId, path string) (*objectstore.UploadRequest, error) {
	key, err := updateFileKey(types.Update{AppId: appId, Branch: branch, RuntimeVersion: runtimeVersion, UpdateId: updateId}, path)
	if err != nil {
		return nil, err
	}
	return presignUpload(ctx, u.objectStore, u.localUploads, appId, branch, key)
}

func (u *UpdateStore) Delete(ctx context.Context, appId, branch, runtimeVersion, updateId string) error {
	if err := validateUpdateRef(appId, branch, runtimeVersion, updateId); err != nil {
		return err
	}
	return u.objectStore.DeletePrefix(ctx, updatePrefix(appId, branch, runtimeVersion, updateId))
}

// CreateFrom copies an update's files into a new update id, leaving out the
// markers the new update earns on its own: .check and update-metadata.json.
func (u *UpdateStore) CreateFrom(ctx context.Context, previousUpdate *types.Update, newUpdateId string) (*types.Update, error) {
	if err := validateUpdate(previousUpdate); err != nil {
		return nil, err
	}
	if err := validateSegment("newUpdateId", newUpdateId); err != nil {
		return nil, err
	}
	updateId, err := strconv.ParseInt(newUpdateId, 10, 64)
	if err != nil {
		return nil, errors.New("newUpdateId must be a timestamp")
	}
	sourcePrefix := updatePrefix(previousUpdate.AppId, previousUpdate.Branch, previousUpdate.RuntimeVersion, previousUpdate.UpdateId)
	targetPrefix := updatePrefix(previousUpdate.AppId, previousUpdate.Branch, previousUpdate.RuntimeVersion, newUpdateId)

	g, ctx := errgroup.WithContext(ctx)
	keys, err := u.objectStore.List(ctx, sourcePrefix)
	if err != nil {
		return nil, err
	}
	g.SetLimit(runtime.NumCPU())
	for _, key := range keys {
		relative := strings.TrimPrefix(key, sourcePrefix)
		if relative == "update-metadata.json" || relative == ".check" {
			continue
		}
		g.Go(func() error {
			copyCtx, cancel := context.WithTimeout(ctx, createFromCopyTimeout)
			defer cancel()
			return u.objectStore.Copy(copyCtx, key, targetPrefix+relative)
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return &types.Update{
		AppId:          previousUpdate.AppId,
		Branch:         previousUpdate.Branch,
		RuntimeVersion: previousUpdate.RuntimeVersion,
		UpdateId:       newUpdateId,
		CreatedAt:      helpers.NormalizeTimestampToDuration(updateId),
	}, nil
}
