package bucket

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
	"xprem/internal/providers/aws"
	"xprem/internal/types"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func (b *S3Bucket) blobKey(appId, hash string) string {
	return prefixedBlobKey(b.KeyPrefix, appId, hash)
}

func (b *S3Bucket) BlobExists(ctx context.Context, appId, hash string) (bool, error) {
	return b.objectExists(ctx, b.blobKey(appId, hash))
}

func (b *S3Bucket) GetBlob(ctx context.Context, appId, hash string) (*types.BucketFile, error) {
	return b.getObject(ctx, b.blobKey(appId, hash))
}

func (b *S3Bucket) PutBlob(ctx context.Context, appId, hash string, body io.Reader) error {
	return b.putObject(ctx, b.blobKey(appId, hash), body)
}

func (b *S3Bucket) RequestBlobUploadURL(ctx context.Context, appId, hash, _ string) (*UploadRequest, error) {
	if b.BucketName == "" {
		return nil, errors.New("BucketName not set")
	}
	s3Client, err := aws.GetS3Client()
	if err != nil {
		return nil, fmt.Errorf("error getting S3 client: %w", err)
	}
	presignClient := s3.NewPresignClient(s3Client)
	presignResult, err := presignClient.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket: awssdk.String(b.BucketName),
		Key:    awssdk.String(b.blobKey(appId, hash)),
	}, func(opt *s3.PresignOptions) {
		opt.Expires = 15 * time.Minute
	})
	if err != nil {
		return nil, fmt.Errorf("error presigning URL: %w", err)
	}
	return &UploadRequest{URL: presignResult.URL, Method: "PUT"}, nil
}
