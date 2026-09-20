package bucket

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sync"
	"time"
	"xprem/internal/providers/azure"
	"xprem/internal/types"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
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

// uploadHeaders are the headers the uploader must send verbatim on its PUT.
func (b *AzureBucket) uploadHeaders() map[string]string {
	return map[string]string{"x-ms-blob-type": "BlockBlob"}
}

func (b *AzureBucket) objectExists(ctx context.Context, key string) (bool, error) {
	cc, err := b.containerClient()
	if err != nil {
		return false, err
	}
	_, err = cc.NewBlobClient(key).GetProperties(ctx, nil)
	if err != nil {
		if bloberror.HasCode(err, bloberror.BlobNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("GetProperties error: %w", err)
	}
	return true, nil
}

// getObject returns nil, nil when the key does not exist.
func (b *AzureBucket) getObject(ctx context.Context, key string) (*types.BucketFile, error) {
	cc, err := b.containerClient()
	if err != nil {
		return nil, err
	}
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

func (b *AzureBucket) putObject(ctx context.Context, key string, body io.Reader) error {
	cc, err := b.containerClient()
	if err != nil {
		return err
	}
	if _, err := cc.NewBlockBlobClient(key).UploadStream(ctx, body, nil); err != nil {
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

// deletePrefix removes every blob under prefix.
func (b *AzureBucket) deletePrefix(ctx context.Context, prefix string) error {
	cc, err := b.containerClient()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	pager := cc.NewListBlobsFlatPager(&container.ListBlobsFlatOptions{Prefix: &prefix})
	sem := make(chan struct{}, runtime.NumCPU())
	var wg sync.WaitGroup
	errCh := make(chan error, 16)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			cancel()
			wg.Wait()
			close(errCh)
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

// deleteObject is a no-op when the blob does not exist.
func (b *AzureBucket) deleteObject(ctx context.Context, key string) error {
	cc, err := b.containerClient()
	if err != nil {
		return err
	}
	_, err = cc.NewBlobClient(key).Delete(ctx, nil)
	if bloberror.HasCode(err, bloberror.BlobNotFound) {
		return nil
	}
	return err
}
