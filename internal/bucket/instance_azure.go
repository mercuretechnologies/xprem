package bucket

import (
	"bytes"
	"context"
	"io"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
)

func (b *AzureBucket) GetInstanceID(ctx context.Context) (string, error) {
	cc, err := b.containerClient()
	if err != nil {
		return "", err
	}
	resp, err := cc.NewBlobClient(b.prefixedKey(".instanceid")).DownloadStream(ctx, nil)
	if err != nil {
		if bloberror.HasCode(err, bloberror.BlobNotFound) {
			return "", nil
		}
		return "", err
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	if _, err := io.Copy(buf, resp.Body); err != nil {
		return "", err
	}
	return strings.TrimSpace(buf.String()), nil
}

func (b *AzureBucket) PersistInstanceID(ctx context.Context, id string) error {
	cc, err := b.containerClient()
	if err != nil {
		return err
	}
	_, err = cc.NewBlockBlobClient(b.prefixedKey(".instanceid")).UploadBuffer(ctx, []byte(id+"\n"), nil)
	return err
}
