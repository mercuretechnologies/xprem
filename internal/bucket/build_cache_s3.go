package bucket

import (
	"context"
	"time"
	"xprem/config"
	"xprem/internal/providers/aws"
	"xprem/internal/types"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func (b *S3Bucket) GetBuildCache(ctx context.Context, ref BuildCacheObject) (*types.BucketFile, error) {
	key := ref.Key()
	return b.getObject(ctx, b.prefixedKey(key))
}
func (b *S3Bucket) DeleteBuildCache(ctx context.Context, ref BuildCacheObject) error {
	key := ref.Key()
	return b.deleteObject(ctx, b.prefixedKey(key))
}
func (b *S3Bucket) RequestBuildCacheUploadURL(ctx context.Context, ref BuildCacheObject) (*UploadRequest, error) {
	key := ref.Key()
	client, err := aws.GetS3Client()
	if err != nil {
		return nil, err
	}
	result, err := s3.NewPresignClient(client).PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket: awssdk.String(b.BucketName), Key: awssdk.String(b.prefixedKey(key)),
	}, func(o *s3.PresignOptions) { o.Expires = buildUploadExpiry })
	if err != nil {
		return nil, err
	}
	return &UploadRequest{URL: result.URL, Method: "PUT"}, nil
}
func (b *S3Bucket) RequestBuildCacheDownloadURL(ctx context.Context, ref BuildCacheObject, expiry time.Time) (string, error) {
	key := ref.Key()
	if config.GetEnv("DISABLE_S3_DIRECT_CDN") == "true" {
		return "", nil
	}
	client, err := aws.GetS3Client()
	if err != nil {
		return "", err
	}
	result, err := s3.NewPresignClient(client).PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: awssdk.String(b.BucketName), Key: awssdk.String(b.prefixedKey(key)), ResponseCacheControl: awssdk.String("private, no-store"),
	}, func(o *s3.PresignOptions) { o.Expires = time.Until(expiry) })
	if err != nil {
		return "", err
	}
	return result.URL, nil
}
