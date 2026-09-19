package bucket

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"time"
	"xprem/config"
	"xprem/internal/providers/aws"
	"xprem/internal/types"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func (b *S3Bucket) buildArtifactKey(ref BuildArtifact, staging bool) string {
	return b.prefixedKey(ref.Key(staging))
}

func (b *S3Bucket) GetBuildArtifact(ctx context.Context, ref BuildArtifact, staging bool) (*types.BucketFile, error) {
	key := b.buildArtifactKey(ref, staging)
	return b.getObject(ctx, key)
}

func (b *S3Bucket) PutBuildArtifact(ctx context.Context, ref BuildArtifact, staging bool, body io.Reader) error {
	key := b.buildArtifactKey(ref, staging)
	return b.putObject(ctx, key, body)
}

func (b *S3Bucket) DeleteBuildArtifact(ctx context.Context, ref BuildArtifact, staging bool) error {
	key := b.buildArtifactKey(ref, staging)
	return b.deleteObject(ctx, key)
}

func (b *S3Bucket) RequestBuildArtifactUploadURL(ctx context.Context, _ string, ref BuildArtifact) (*UploadRequest, error) {
	key := b.buildArtifactKey(ref, true)
	if b.BucketName == "" {
		return nil, errors.New("BucketName not set")
	}
	s3Client, err := aws.GetS3Client()
	if err != nil {
		return nil, fmt.Errorf("error getting S3 client: %w", err)
	}
	presignResult, err := s3.NewPresignClient(s3Client).PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket: awssdk.String(b.BucketName),
		Key:    awssdk.String(key),
	}, func(opt *s3.PresignOptions) {
		opt.Expires = buildUploadExpiry
	})
	if err != nil {
		return nil, fmt.Errorf("error presigning URL: %w", err)
	}
	return &UploadRequest{URL: presignResult.URL, Method: "PUT"}, nil
}

func (b *S3Bucket) RequestBuildArtifactDownloadURL(ctx context.Context, ref BuildArtifact, expiresAt time.Time) (string, error) {
	if config.GetEnv("DISABLE_S3_DIRECT_CDN") == "true" {
		return "", nil
	}
	key := b.buildArtifactKey(ref, false)
	if b.BucketName == "" {
		return "", errors.New("BucketName not set")
	}
	s3Client, err := aws.GetS3Client()
	if err != nil {
		return "", err
	}
	expires := time.Until(expiresAt).Truncate(time.Second)
	if expires < time.Second {
		return "", ErrBuildDownloadExpired
	}
	presigned, err := s3.NewPresignClient(s3Client).PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket:                     awssdk.String(b.BucketName),
		Key:                        awssdk.String(key),
		ResponseContentDisposition: awssdk.String(ref.downloadDisposition()),
		ResponseContentType:        awssdk.String(ref.downloadContentType()),
		ResponseCacheControl:       awssdk.String("private, no-store"),
	}, func(opt *s3.PresignOptions) {
		opt.Expires = expires
	})
	if err != nil {
		return "", err
	}
	// Credential retrieval can delay signing. Never expose a URL that would
	// outlive the deadline even if that lookup took several seconds.
	signedURL, err := url.Parse(presigned.URL)
	if err != nil {
		return "", err
	}
	signedAt, err := time.Parse("20060102T150405Z", signedURL.Query().Get("X-Amz-Date"))
	if err != nil {
		return "", err
	}
	if signedAt.Add(expires).After(expiresAt) {
		return "", ErrBuildDownloadExpired
	}
	return presigned.URL, nil
}
