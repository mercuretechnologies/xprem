package bucket

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"xprem/internal/providers/aws"
	"xprem/internal/types"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type S3Bucket struct {
	BucketName string
	KeyPrefix  string
}

func (b *S3Bucket) prefixedKey(key string) string {
	return b.KeyPrefix + key
}

// deletePrefix removes every object under prefix.
func (b *S3Bucket) deletePrefix(ctx context.Context, prefix string) error {
	if b.BucketName == "" {
		return errors.New("BucketName not set")
	}

	s3Client, err := aws.GetS3Client()
	if err != nil {
		return fmt.Errorf("error getting S3 client: %w", err)
	}

	listInput := &s3.ListObjectsV2Input{
		Bucket: awssdk.String(b.BucketName),
		Prefix: awssdk.String(prefix),
	}

	var objects []s3types.ObjectIdentifier

	paginator := s3.NewListObjectsV2Paginator(s3Client, listInput)
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("failed to list objects: %w", err)
		}

		for _, obj := range page.Contents {
			objects = append(objects, s3types.ObjectIdentifier{
				Key: obj.Key,
			})
		}
	}

	if len(objects) == 0 {
		return nil
	}

	const batchSize = 1000
	for i := 0; i < len(objects); i += batchSize {
		end := i + batchSize
		if end > len(objects) {
			end = len(objects)
		}

		deleteInput := &s3.DeleteObjectsInput{
			Bucket: awssdk.String(b.BucketName),
			Delete: &s3types.Delete{
				Objects: objects[i:end],
				Quiet:   awssdk.Bool(true),
			},
		}

		output, err := s3Client.DeleteObjects(ctx, deleteInput)
		if err != nil {
			return fmt.Errorf("failed to delete objects: %w", err)
		}
		if len(output.Errors) > 0 {
			first := output.Errors[0]
			return fmt.Errorf("failed to delete %d objects: key %q (%s): %s", len(output.Errors), awssdk.ToString(first.Key), awssdk.ToString(first.Code), awssdk.ToString(first.Message))
		}
	}

	return nil
}

func (b *S3Bucket) objectExists(ctx context.Context, key string) (bool, error) {
	if b.BucketName == "" {
		return false, errors.New("BucketName not set")
	}
	s3Client, err := aws.GetS3Client()
	if err != nil {
		return false, err
	}
	_, err = s3Client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: awssdk.String(b.BucketName),
		Key:    awssdk.String(key),
	})
	if err != nil {
		var notFound *s3types.NotFound
		var noSuchKey *s3types.NoSuchKey
		if errors.As(err, &notFound) || errors.As(err, &noSuchKey) {
			return false, nil
		}
		return false, fmt.Errorf("HeadObject error: %w", err)
	}
	return true, nil
}

// getObject returns nil, nil when the key does not exist.
func (b *S3Bucket) getObject(ctx context.Context, key string) (*types.BucketFile, error) {
	if b.BucketName == "" {
		return nil, errors.New("BucketName not set")
	}
	s3Client, err := aws.GetS3Client()
	if err != nil {
		return nil, err
	}
	resp, err := s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: awssdk.String(b.BucketName),
		Key:    awssdk.String(key),
	})
	if err != nil {
		var noSuchKey *s3types.NoSuchKey
		if errors.As(err, &noSuchKey) {
			return nil, nil
		}
		return nil, fmt.Errorf("GetObject error: %w", err)
	}
	return &types.BucketFile{
		Reader:    resp.Body,
		CreatedAt: awssdk.ToTime(resp.LastModified),
	}, nil
}

func (b *S3Bucket) putObject(ctx context.Context, key string, body io.Reader) error {
	if b.BucketName == "" {
		return errors.New("BucketName not set")
	}
	s3Client, err := aws.GetS3Client()
	if err != nil {
		return err
	}
	_, err = s3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: awssdk.String(b.BucketName),
		Key:    awssdk.String(key),
		Body:   body,
	})
	if err != nil {
		return fmt.Errorf("PutObject error: %w", err)
	}
	return nil
}

// PutObject implements the audit archive's object write.
func (b *S3Bucket) PutObject(ctx context.Context, key string, body []byte) error {
	if b.BucketName == "" {
		return errors.New("BucketName not set")
	}
	s3Client, err := aws.GetS3Client()
	if err != nil {
		return err
	}
	input := &s3.PutObjectInput{
		Bucket: awssdk.String(b.BucketName),
		Key:    awssdk.String(b.prefixedKey(key)),
		Body:   bytes.NewReader(body),
	}
	if _, err := s3Client.PutObject(ctx, input); err != nil {
		return fmt.Errorf("PutObject error: %w", err)
	}
	return nil
}

// deleteObject is a no-op when the key does not exist.
func (b *S3Bucket) deleteObject(ctx context.Context, key string) error {
	if b.BucketName == "" {
		return errors.New("BucketName not set")
	}
	s3Client, err := aws.GetS3Client()
	if err != nil {
		return err
	}
	_, err = s3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: awssdk.String(b.BucketName),
		Key:    awssdk.String(key),
	})
	if err != nil {
		return fmt.Errorf("DeleteObject error: %w", err)
	}
	return nil
}
