package bucket

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"xprem/internal/providers/aws"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

func (b *S3Bucket) GetInstanceID(ctx context.Context) (string, error) {
	if b.BucketName == "" {
		return "", errors.New("BucketName not set")
	}
	s3Client, errS3 := aws.GetS3Client()
	if errS3 != nil {
		return "", errS3
	}
	resp, err := s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: awssdk.String(b.BucketName),
		Key:    awssdk.String(b.prefixedKey(".instanceid")),
	})
	if err != nil {
		var noSuchKey *s3types.NoSuchKey
		if errors.As(err, &noSuchKey) {
			return "", nil
		}
		return "", fmt.Errorf("GetObject error: %w", err)
	}
	defer resp.Body.Close()
	content, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(content)), nil
}

func (b *S3Bucket) PersistInstanceID(ctx context.Context, id string) error {
	if b.BucketName == "" {
		return errors.New("BucketName not set")
	}
	s3Client, errS3 := aws.GetS3Client()
	if errS3 != nil {
		return errS3
	}
	_, err := s3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: awssdk.String(b.BucketName),
		Key:    awssdk.String(b.prefixedKey(".instanceid")),
		Body:   bytes.NewReader([]byte(id + "\n")),
	})
	if err != nil {
		return fmt.Errorf("PutObject error: %w", err)
	}
	return nil
}
