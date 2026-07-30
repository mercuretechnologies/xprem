package cdn

import (
	"context"
	"fmt"
	"time"
	"xprem/config"
	"xprem/internal/bucket"
	"xprem/internal/providers/aws"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3DirectCDN redirects asset requests to presigned URLs served directly by
// S3, the S3 counterpart of GCSDirectCDN: the bucket stays private and S3
// validates each URL.
type S3DirectCDN struct{}

func (c *S3DirectCDN) isCDNAvailable() bool {
	if config.GetEnv("STORAGE_MODE") != "s3" || config.GetEnv("S3_BUCKET_NAME") == "" {
		return false
	}
	// Opt-out for buckets on an endpoint devices cannot reach (a MinIO on a
	// private network): the server keeps streaming assets itself.
	if config.GetEnv("DISABLE_S3_DIRECT_CDN") == "true" {
		return false
	}
	return true
}

func (c *S3DirectCDN) ComputeRedirectionURLForAsset(appId, branch, runtimeVersion, updateId, asset string) (string, error) {
	// Must match the full object key that the bucket backend writes:
	// {BUCKET_KEY_PREFIX}{appId}/{branch}/{rv}/{updateId}/{asset}. Dropping
	// the prefix here points the signed URL at a non-existent object.
	key := bucket.ResolveKeyPrefix() + fmt.Sprintf("%s/%s/%s/%s/%s", appId, branch, runtimeVersion, updateId, asset)
	return presignGetObject(key)
}

func presignGetObject(key string) (string, error) {
	s3Client, err := aws.GetS3Client()
	if err != nil {
		return "", fmt.Errorf("error getting S3 client: %w", err)
	}
	presignClient := s3.NewPresignClient(s3Client)
	presignResult, err := presignClient.PresignGetObject(context.TODO(), &s3.GetObjectInput{
		Bucket: awssdk.String(config.GetEnv("S3_BUCKET_NAME")),
		Key:    awssdk.String(key),
	}, func(opt *s3.PresignOptions) {
		opt.Expires = 15 * time.Minute
	})
	if err != nil {
		return "", fmt.Errorf("error presigning URL: %w", err)
	}
	return presignResult.URL, nil
}
