package bucket

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"xprem/internal/helpers"
	"xprem/internal/providers/azure"
	"xprem/internal/types"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/sas"
)

type AzureBucket struct {
	ContainerName string
	KeyPrefix     string
}

func (b *AzureBucket) prefixedKey(key string) string {
	return b.KeyPrefix + key
}

func (b *AzureBucket) containerClient() (*container.Client, error) {
	if b.ContainerName == "" {
		return nil, errors.New("ContainerName not set")
	}
	client, err := azure.GetClient()
	if err != nil {
		return nil, err
	}
	return client.ServiceClient().NewContainerClient(b.ContainerName), nil
}

func (b *AzureBucket) GetBranches(appId string) ([]string, error) {
	ctx := context.Background()
	cc, err := b.containerClient()
	if err != nil {
		return nil, err
	}
	appPrefix := b.prefixedKey(appId + "/")
	pager := cc.NewListBlobsHierarchyPager("/", &container.ListBlobsHierarchyOptions{Prefix: &appPrefix})
	var branches []string
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list branches: %w", err)
		}
		for _, blobPrefix := range page.Segment.BlobPrefixes {
			if blobPrefix.Name == nil {
				continue
			}
			branches = append(branches, strings.TrimSuffix(strings.TrimPrefix(*blobPrefix.Name, appPrefix), "/"))
		}
	}
	return branches, nil
}

func (b *AzureBucket) GetRuntimeVersions(appId string, branch string) ([]types.RuntimeVersionWithStats, error) {
	ctx := context.Background()
	cc, err := b.containerClient()
	if err != nil {
		return nil, err
	}
	branchPrefix := b.prefixedKey(appId + "/" + branch + "/")
	pager := cc.NewListBlobsHierarchyPager("/", &container.ListBlobsHierarchyOptions{Prefix: &branchPrefix})
	var runtimeVersions []types.RuntimeVersionWithStats
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list runtime versions: %w", err)
		}
		for _, blobPrefix := range page.Segment.BlobPrefixes {
			if blobPrefix.Name == nil {
				continue
			}
			rvPrefix := *blobPrefix.Name
			rv := strings.TrimSuffix(strings.TrimPrefix(rvPrefix, branchPrefix), "/")

			updatesPager := cc.NewListBlobsHierarchyPager("/", &container.ListBlobsHierarchyOptions{Prefix: &rvPrefix})
			var updateTimestamps []int64
			for updatesPager.More() {
				updatesPage, err := updatesPager.NextPage(ctx)
				if err != nil {
					return nil, fmt.Errorf("list updates: %w", err)
				}
				for _, updatePrefix := range updatesPage.Segment.BlobPrefixes {
					if updatePrefix.Name == nil {
						continue
					}
					upd := strings.TrimSuffix(strings.TrimPrefix(*updatePrefix.Name, rvPrefix), "/")
					if _, err := cc.NewBlobClient(*updatePrefix.Name+".check").GetProperties(ctx, nil); err != nil {
						continue
					}
					if ts, err := strconv.ParseInt(upd, 10, 64); err == nil {
						updateTimestamps = append(updateTimestamps, ts)
					}
				}
			}
			if len(updateTimestamps) == 0 {
				continue
			}
			sort.Slice(updateTimestamps, func(i, j int) bool { return updateTimestamps[i] < updateTimestamps[j] })
			runtimeVersions = append(runtimeVersions, types.RuntimeVersionWithStats{
				RuntimeVersion:  rv,
				CreatedAt:       helpers.NormalizeTimestamp(updateTimestamps[0]).Format(time.RFC3339),
				LastUpdatedAt:   helpers.NormalizeTimestamp(updateTimestamps[len(updateTimestamps)-1]).Format(time.RFC3339),
				NumberOfUpdates: len(updateTimestamps),
			})
		}
	}
	return runtimeVersions, nil
}

func (b *AzureBucket) GetUpdates(appId string, branch string, runtimeVersion string) ([]types.Update, error) {
	ctx := context.Background()
	cc, err := b.containerClient()
	if err != nil {
		return nil, err
	}
	prefix := b.prefixedKey(appId + "/" + branch + "/" + runtimeVersion + "/")
	pager := cc.NewListBlobsHierarchyPager("/", &container.ListBlobsHierarchyOptions{Prefix: &prefix})
	var updates []types.Update
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list updates: %w", err)
		}
		for _, blobPrefix := range page.Segment.BlobPrefixes {
			if blobPrefix.Name == nil {
				continue
			}
			name := strings.TrimSuffix(strings.TrimPrefix(*blobPrefix.Name, prefix), "/")
			if id, err := strconv.ParseInt(name, 10, 64); err == nil {
				updates = append(updates, types.Update{
					AppId:          appId,
					Branch:         branch,
					RuntimeVersion: runtimeVersion,
					UpdateId:       strconv.FormatInt(id, 10),
					CreatedAt:      helpers.NormalizeTimestampToDuration(id),
				})
			}
		}
	}
	return updates, nil
}

func (b *AzureBucket) GetFile(update types.Update, assetPath string) (*types.BucketFile, error) {
	ctx := context.Background()
	cc, err := b.containerClient()
	if err != nil {
		return nil, err
	}
	key := b.prefixedKey(update.AppId + "/" + update.Branch + "/" + update.RuntimeVersion + "/" + update.UpdateId + "/" + assetPath)
	resp, err := cc.NewBlobClient(key).DownloadStream(ctx, nil)
	if err != nil {
		if bloberror.HasCode(err, bloberror.BlobNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("DownloadStream error: %w", err)
	}
	var created time.Time
	if resp.LastModified != nil {
		created = *resp.LastModified
	}
	return &types.BucketFile{Reader: resp.Body, CreatedAt: created}, nil
}

func (b *AzureBucket) RequestUploadUrlForFileUpdate(appId string, branch string, runtimeVersion string, updateId string, fileName string) (string, error) {
	if b.ContainerName == "" {
		return "", errors.New("ContainerName not set")
	}
	key := b.prefixedKey(fmt.Sprintf("%s/%s/%s/%s/%s", appId, branch, runtimeVersion, updateId, fileName))
	url, err := azure.SignBlobSAS(b.ContainerName, key, sas.BlobPermissions{Create: true, Write: true}, 15*time.Minute)
	if err != nil {
		return "", fmt.Errorf("error generating SAS URL: %w", err)
	}
	return url, nil
}

func (b *AzureBucket) UploadFileIntoUpdate(update types.Update, fileName string, file io.Reader) error {
	ctx := context.Background()
	cc, err := b.containerClient()
	if err != nil {
		return err
	}
	key := b.prefixedKey(fmt.Sprintf("%s/%s/%s/%s/%s", update.AppId, update.Branch, update.RuntimeVersion, update.UpdateId, fileName))
	if _, err := cc.NewBlockBlobClient(key).UploadStream(ctx, file, nil); err != nil {
		return fmt.Errorf("error uploading blob: %w", err)
	}
	return nil
}

// PutObject implements the audit archive's object write.
func (b *AzureBucket) PutObject(ctx context.Context, key string, body []byte) error {
	cc, err := b.containerClient()
	if err != nil {
		return err
	}
	if _, err := cc.NewBlockBlobClient(b.prefixedKey(key)).UploadStream(ctx, bytes.NewReader(body), nil); err != nil {
		return fmt.Errorf("error uploading blob: %w", err)
	}
	return nil
}

func (b *AzureBucket) DeleteUpdateFolder(appId, branch, runtimeVersion, updateId string) error {
	ctx := context.Background()
	cc, err := b.containerClient()
	if err != nil {
		return err
	}
	prefix := b.prefixedKey(fmt.Sprintf("%s/%s/%s/%s/", appId, branch, runtimeVersion, updateId))
	pager := cc.NewListBlobsFlatPager(&container.ListBlobsFlatOptions{Prefix: &prefix})
	sem := make(chan struct{}, runtime.NumCPU())
	var wg sync.WaitGroup
	errCh := make(chan error, 16)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("failed to list blobs: %w", err)
		}
		for _, item := range page.Segment.BlobItems {
			if item.Name == nil {
				continue
			}
			name := *item.Name
			wg.Add(1)
			sem <- struct{}{}
			go func(name string) {
				defer wg.Done()
				defer func() { <-sem }()
				if _, err := cc.NewBlobClient(name).Delete(ctx, nil); err != nil {
					// Non-blocking send: only the first error is returned,
					// further ones must not deadlock the waiting goroutines.
					select {
					case errCh <- fmt.Errorf("failed to delete blob %s: %w", name, err):
					default:
					}
				}
			}(name)
		}
	}
	wg.Wait()
	close(errCh)
	for e := range errCh {
		if e != nil {
			return e
		}
	}
	return nil
}

// copyBlobAndWait runs a server-side copy and waits for it to complete.
// Same-account copies usually finish synchronously, but the Azure API is
// asynchronous by contract, so the copy status must be polled.
func copyBlobAndWait(ctx context.Context, cc *container.Client, sourceURL, destKey string) error {
	destBlob := cc.NewBlobClient(destKey)
	resp, err := destBlob.StartCopyFromURL(ctx, sourceURL, nil)
	if err != nil {
		return err
	}
	status := blob.CopyStatusTypePending
	if resp.CopyStatus != nil {
		status = *resp.CopyStatus
	}
	for status == blob.CopyStatusTypePending {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
		props, err := destBlob.GetProperties(ctx, nil)
		if err != nil {
			return err
		}
		if props.CopyStatus != nil {
			status = *props.CopyStatus
		}
	}
	if status != blob.CopyStatusTypeSuccess {
		return fmt.Errorf("copy finished with status %s", status)
	}
	return nil
}

func (b *AzureBucket) CopyFileIntoUpdate(source types.Update, target types.Update, fileName string) error {
	if b.ContainerName == "" {
		return errors.New("ContainerName not set")
	}
	cc, err := b.containerClient()
	if err != nil {
		return err
	}
	srcKey := b.prefixedKey(fmt.Sprintf("%s/%s/%s/%s/%s", source.AppId, source.Branch, source.RuntimeVersion, source.UpdateId, fileName))
	dstKey := b.prefixedKey(fmt.Sprintf("%s/%s/%s/%s/%s", target.AppId, target.Branch, target.RuntimeVersion, target.UpdateId, fileName))
	// StartCopyFromURL authorizes the source through its URL, so the
	// same-account source gets a short-lived read SAS.
	srcURL, err := azure.SignBlobSAS(b.ContainerName, srcKey, sas.BlobPermissions{Read: true}, 10*time.Minute)
	if err != nil {
		return fmt.Errorf("sign source %s: %w", srcKey, err)
	}
	copyCtx, cancel := context.WithTimeout(context.Background(), copyFileTimeout)
	defer cancel()
	if err := copyBlobAndWait(copyCtx, cc, srcURL, dstKey); err != nil {
		return fmt.Errorf("copy %s -> %s: %w", srcKey, dstKey, err)
	}
	return nil
}

func (b *AzureBucket) CreateUpdateFrom(previousUpdate *types.Update, newUpdateId string) (*types.Update, error) {
	if b.ContainerName == "" {
		return nil, errors.New("ContainerName not set")
	}
	if previousUpdate == nil {
		return nil, errors.New("previousUpdate is nil")
	}
	if previousUpdate.UpdateId == "" {
		return nil, errors.New("previousUpdate.UpdateId is empty")
	}
	if newUpdateId == "" {
		return nil, errors.New("newUpdateId is empty")
	}
	ctx := context.Background()
	cc, err := b.containerClient()
	if err != nil {
		return nil, err
	}
	sourcePrefix := b.prefixedKey(fmt.Sprintf("%s/%s/%s/%s/", previousUpdate.AppId, previousUpdate.Branch, previousUpdate.RuntimeVersion, previousUpdate.UpdateId))
	targetPrefix := b.prefixedKey(fmt.Sprintf("%s/%s/%s/%s/", previousUpdate.AppId, previousUpdate.Branch, previousUpdate.RuntimeVersion, newUpdateId))

	pager := cc.NewListBlobsFlatPager(&container.ListBlobsFlatOptions{Prefix: &sourcePrefix})
	var wg sync.WaitGroup
	errChan := make(chan error, 16)
	sem := make(chan struct{}, runtime.NumCPU())

	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list blobs: %w", err)
		}
		for _, item := range page.Segment.BlobItems {
			if item.Name == nil {
				continue
			}
			relPath := strings.TrimPrefix(*item.Name, sourcePrefix)
			if relPath == "update-metadata.json" || relPath == ".check" || strings.HasSuffix(relPath, "/") {
				continue
			}
			src := *item.Name
			dst := targetPrefix + relPath

			wg.Add(1)
			go func(srcKey, dstKey string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				// StartCopyFromURL authorizes the source through its URL, so
				// the same-account source gets a short-lived read SAS.
				srcURL, err := azure.SignBlobSAS(b.ContainerName, srcKey, sas.BlobPermissions{Read: true}, 10*time.Minute)
				if err != nil {
					select {
					case errChan <- fmt.Errorf("sign source %s: %w", srcKey, err):
					default:
					}
					return
				}
				copyCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
				defer cancel()
				if err := copyBlobAndWait(copyCtx, cc, srcURL, dstKey); err != nil {
					select {
					case errChan <- fmt.Errorf("copy %s -> %s: %w", srcKey, dstKey, err):
					default:
					}
				}
			}(src, dst)
		}
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

func (b *AzureBucket) RetrieveMigrationHistory() ([]string, error) {
	ctx := context.Background()
	cc, err := b.containerClient()
	if err != nil {
		return nil, err
	}
	resp, err := cc.NewBlobClient(b.prefixedKey(".migrationhistory")).DownloadStream(ctx, nil)
	if err != nil {
		if bloberror.HasCode(err, bloberror.BlobNotFound) {
			return nil, nil
		}
		return nil, err
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	if _, err := io.Copy(buf, resp.Body); err != nil {
		return nil, err
	}
	var migrations []string
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line != "" {
			migrations = append(migrations, line)
		}
	}
	return migrations, nil
}

func (b *AzureBucket) writeMigrationHistory(content string) error {
	ctx := context.Background()
	cc, err := b.containerClient()
	if err != nil {
		return err
	}
	_, err = cc.NewBlockBlobClient(b.prefixedKey(".migrationhistory")).UploadBuffer(ctx, []byte(content), nil)
	return err
}

func (b *AzureBucket) ApplyMigration(migrationId string) error {
	history, err := b.RetrieveMigrationHistory()
	if err != nil {
		return fmt.Errorf("RetrieveMigrationHistory error: %w", err)
	}
	for _, id := range history {
		if id == migrationId {
			return nil
		}
	}
	current := strings.Join(history, "\n")
	if current != "" {
		current += "\n"
	}
	return b.writeMigrationHistory(current + migrationId + "\n")
}

func (b *AzureBucket) RemoveMigrationFromHistory(migrationId string) error {
	history, err := b.RetrieveMigrationHistory()
	if err != nil {
		return fmt.Errorf("RetrieveMigrationHistory error: %w", err)
	}
	found := false
	var filtered []string
	for _, id := range history {
		if id == migrationId {
			found = true
			continue
		}
		filtered = append(filtered, id)
	}
	if !found {
		return nil
	}
	content := ""
	if len(filtered) > 0 {
		content = strings.Join(filtered, "\n") + "\n"
	}
	return b.writeMigrationHistory(content)
}
