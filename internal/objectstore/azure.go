package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"time"
	"xprem/internal/providers/azure"
	"xprem/internal/types"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/sas"
	"golang.org/x/sync/errgroup"
)

type azureStore struct {
	container string
}

func (s *azureStore) client() (*container.Client, error) {
	if s.container == "" {
		return nil, errors.New("azure store: container name not set")
	}
	client, err := azure.GetClient()
	if err != nil {
		return nil, err
	}
	return client.ServiceClient().NewContainerClient(s.container), nil
}

func (s *azureStore) Exists(ctx context.Context, key string) (bool, error) {
	containerClient, err := s.client()
	if err != nil {
		return false, err
	}
	if _, err := containerClient.NewBlobClient(key).GetProperties(ctx, nil); err != nil {
		if bloberror.HasCode(err, bloberror.BlobNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("GetProperties error: %w", err)
	}
	return true, nil
}

func (s *azureStore) Get(ctx context.Context, key string) (*types.BucketFile, error) {
	containerClient, err := s.client()
	if err != nil {
		return nil, err
	}
	resp, err := containerClient.NewBlobClient(key).DownloadStream(ctx, nil)
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

func (s *azureStore) Put(ctx context.Context, key string, body io.Reader) error {
	containerClient, err := s.client()
	if err != nil {
		return err
	}
	if _, err := containerClient.NewBlockBlobClient(key).UploadStream(ctx, body, nil); err != nil {
		return fmt.Errorf("error uploading blob: %w", err)
	}
	return nil
}

func (s *azureStore) Delete(ctx context.Context, key string) error {
	containerClient, err := s.client()
	if err != nil {
		return err
	}
	if _, err := containerClient.NewBlobClient(key).Delete(ctx, nil); err != nil && !bloberror.HasCode(err, bloberror.BlobNotFound) {
		return fmt.Errorf("delete %s: %w", key, err)
	}
	return nil
}

func (s *azureStore) DeletePrefix(ctx context.Context, prefix string) error {
	if err := requirePrefix(prefix); err != nil {
		return err
	}
	keys, err := s.List(ctx, prefix)
	if err != nil {
		return err
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.NumCPU())
	for _, key := range keys {
		g.Go(func() error { return s.Delete(gctx, key) })
	}
	return g.Wait()
}

// Copy authorizes the source through a short-lived read SAS, then polls the
// copy: same-account copies usually finish synchronously, but the API is
// asynchronous by contract.
func (s *azureStore) Copy(ctx context.Context, from, to string) error {
	containerClient, err := s.client()
	if err != nil {
		return err
	}
	sourceURL, err := azure.SignBlobSAS(s.container, from, sas.BlobPermissions{Read: true}, 10*time.Minute)
	if err != nil {
		return fmt.Errorf("sign source %s: %w", from, err)
	}
	destination := containerClient.NewBlobClient(to)
	resp, err := destination.StartCopyFromURL(ctx, sourceURL, nil)
	if err != nil {
		return fmt.Errorf("copy %s -> %s: %w", from, to, err)
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
		props, err := destination.GetProperties(ctx, nil)
		if err != nil {
			return err
		}
		if props.CopyStatus != nil {
			status = *props.CopyStatus
		}
	}
	if status != blob.CopyStatusTypeSuccess {
		return fmt.Errorf("copy %s -> %s finished with status %s", from, to, status)
	}
	return nil
}

func (s *azureStore) List(ctx context.Context, prefix string) ([]string, error) {
	containerClient, err := s.client()
	if err != nil {
		return nil, err
	}
	var keys []string
	pager := containerClient.NewListBlobsFlatPager(&container.ListBlobsFlatOptions{Prefix: &prefix})
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list blobs: %w", err)
		}
		for _, item := range page.Segment.BlobItems {
			if item.Name != nil {
				keys = append(keys, *item.Name)
			}
		}
	}
	return keys, nil
}

func (s *azureStore) ListPrefixes(ctx context.Context, prefix string) ([]string, error) {
	containerClient, err := s.client()
	if err != nil {
		return nil, err
	}
	var names []string
	pager := containerClient.NewListBlobsHierarchyPager("/", &container.ListBlobsHierarchyOptions{Prefix: &prefix})
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list prefixes: %w", err)
		}
		for _, blobPrefix := range page.Segment.BlobPrefixes {
			if blobPrefix.Name != nil {
				names = append(names, strings.TrimSuffix(strings.TrimPrefix(*blobPrefix.Name, prefix), "/"))
			}
		}
	}
	return names, nil
}

// PresignPut carries the header Azure requires on the PUT, so the uploader
// stays provider-agnostic.
func (s *azureStore) PresignPut(_ context.Context, key string) (*UploadRequest, error) {
	if s.container == "" {
		return nil, errors.New("azure store: container name not set")
	}
	url, err := azure.SignBlobSAS(s.container, key, sas.BlobPermissions{Create: true, Write: true}, 15*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("error generating SAS URL: %w", err)
	}
	return &UploadRequest{URL: url, Method: "PUT", Headers: map[string]string{"x-ms-blob-type": "BlockBlob"}}, nil
}
