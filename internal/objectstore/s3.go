package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"
	"xprem/internal/providers/aws"
	"xprem/internal/types"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type s3Store struct {
	bucket string
}

func (s *s3Store) client() (*s3.Client, error) {
	if s.bucket == "" {
		return nil, errors.New("s3 store: bucket name not set")
	}
	return aws.GetS3Client()
}

func (s *s3Store) Exists(ctx context.Context, key string) (bool, error) {
	client, err := s.client()
	if err != nil {
		return false, err
	}
	_, err = client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: awssdk.String(s.bucket),
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

func (s *s3Store) Get(ctx context.Context, key string) (*types.BucketFile, error) {
	client, err := s.client()
	if err != nil {
		return nil, err
	}
	resp, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: awssdk.String(s.bucket),
		Key:    awssdk.String(key),
	})
	if err != nil {
		var noSuchKey *s3types.NoSuchKey
		if errors.As(err, &noSuchKey) {
			return nil, nil
		}
		return nil, fmt.Errorf("GetObject error: %w", err)
	}
	return &types.BucketFile{Reader: resp.Body, CreatedAt: *resp.LastModified}, nil
}

func (s *s3Store) Put(ctx context.Context, key string, body io.Reader) error {
	client, err := s.client()
	if err != nil {
		return err
	}
	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: awssdk.String(s.bucket),
		Key:    awssdk.String(key),
		Body:   body,
	})
	if err != nil {
		return fmt.Errorf("PutObject error: %w", err)
	}
	return nil
}

func (s *s3Store) Delete(ctx context.Context, key string) error {
	client, err := s.client()
	if err != nil {
		return err
	}
	_, err = client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: awssdk.String(s.bucket),
		Key:    awssdk.String(key),
	})
	if err != nil {
		return fmt.Errorf("DeleteObject error: %w", err)
	}
	return nil
}

func (s *s3Store) DeletePrefix(ctx context.Context, prefix string) error {
	if err := requirePrefix(prefix); err != nil {
		return err
	}
	client, err := s.client()
	if err != nil {
		return err
	}
	keys, err := s.List(ctx, prefix)
	if err != nil {
		return err
	}
	const batchSize = 1000
	for start := 0; start < len(keys); start += batchSize {
		end := min(start+batchSize, len(keys))
		objects := make([]s3types.ObjectIdentifier, 0, end-start)
		for _, key := range keys[start:end] {
			objects = append(objects, s3types.ObjectIdentifier{Key: awssdk.String(key)})
		}
		_, err := client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: awssdk.String(s.bucket),
			Delete: &s3types.Delete{Objects: objects, Quiet: awssdk.Bool(true)},
		})
		if err != nil {
			return fmt.Errorf("DeleteObjects error: %w", err)
		}
	}
	return nil
}

func (s *s3Store) Copy(ctx context.Context, from, to string) error {
	client, err := s.client()
	if err != nil {
		return err
	}
	_, err = client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     awssdk.String(s.bucket),
		CopySource: awssdk.String(s.bucket + "/" + escapeCopySourceKey(from)),
		Key:        awssdk.String(to),
	})
	if err != nil {
		return fmt.Errorf("copy %s -> %s: %w", from, to, err)
	}
	return nil
}

// escapeCopySourceKey escapes a key per segment: CopySource takes
// "bucket/key" with the slashes between segments left as they are, and S3
// decodes it like a query string, so a literal "+" must be encoded.
func escapeCopySourceKey(key string) string {
	segments := strings.Split(key, "/")
	for i, segment := range segments {
		segments[i] = url.QueryEscape(segment)
	}
	return strings.Join(segments, "/")
}

func (s *s3Store) List(ctx context.Context, prefix string) ([]string, error) {
	client, err := s.client()
	if err != nil {
		return nil, err
	}
	var keys []string
	paginator := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{
		Bucket: awssdk.String(s.bucket),
		Prefix: awssdk.String(prefix),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("ListObjectsV2 error: %w", err)
		}
		for _, object := range page.Contents {
			keys = append(keys, *object.Key)
		}
	}
	return keys, nil
}

func (s *s3Store) ListPrefixes(ctx context.Context, prefix string) ([]string, error) {
	client, err := s.client()
	if err != nil {
		return nil, err
	}
	var names []string
	paginator := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{
		Bucket:    awssdk.String(s.bucket),
		Prefix:    awssdk.String(prefix),
		Delimiter: awssdk.String("/"),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("ListObjectsV2 error: %w", err)
		}
		for _, common := range page.CommonPrefixes {
			names = append(names, strings.TrimSuffix(strings.TrimPrefix(*common.Prefix, prefix), "/"))
		}
	}
	return names, nil
}

func (s *s3Store) PresignPut(ctx context.Context, key string) (*UploadRequest, error) {
	client, err := s.client()
	if err != nil {
		return nil, err
	}
	presigned, err := s3.NewPresignClient(client).PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket: awssdk.String(s.bucket),
		Key:    awssdk.String(key),
	}, func(opt *s3.PresignOptions) {
		opt.Expires = 15 * time.Minute
	})
	if err != nil {
		return nil, fmt.Errorf("error presigning URL: %w", err)
	}
	return &UploadRequest{URL: presigned.URL, Method: "PUT"}, nil
}
