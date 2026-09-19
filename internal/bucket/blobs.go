package bucket

import (
	"context"
	"io"
	"xprem/internal/types"
)

const (
	casDir         = "cas"
	blobHashLength = 43
)

// BlobObjectKey is {appId}/cas/{hash}, without the bucket key prefix.
func BlobObjectKey(appId, hash string) string {
	return appId + "/" + casDir + "/" + hash
}

func prefixedBlobKey(prefix, appId, hash string) string {
	return prefix + BlobObjectKey(appId, hash)
}

type BlobStorage interface {
	BlobExists(ctx context.Context, appId, hash string) (bool, error)
	GetBlob(ctx context.Context, appId, hash string) (*types.BucketFile, error)
	PutBlob(ctx context.Context, appId, hash string, body io.Reader) error
	RequestBlobUploadURL(ctx context.Context, appId, hash, branch string) (*UploadRequest, error)
}
