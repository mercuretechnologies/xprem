package bucket

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"xprem/internal/providers/aws"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

func (b *S3Bucket) RetrieveMigrationHistory() ([]string, error) {
	history, _, err := b.readMigrationHistory()
	return history, err
}

func (b *S3Bucket) readMigrationHistory() ([]string, *string, error) {
	if b.BucketName == "" {
		return nil, nil, errors.New("BucketName not set")
	}
	s3Client, err := aws.GetS3Client()
	if err != nil {
		return nil, nil, err
	}
	resp, err := s3Client.GetObject(context.TODO(), &s3.GetObjectInput{
		Bucket: awssdk.String(b.BucketName),
		Key:    awssdk.String(b.prefixedKey(".migrationhistory")),
	})
	if err != nil {
		var noSuchKey *s3types.NoSuchKey
		if errors.As(err, &noSuchKey) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("GetObject error: %w", err)
	}
	defer resp.Body.Close()
	history, err := readS3MigrationHistory(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	if resp.ETag == nil || *resp.ETag == "" {
		return nil, nil, errors.New("migration history response has no ETag")
	}
	return history, resp.ETag, nil
}

func readS3MigrationHistory(body io.Reader) ([]string, error) {
	// Read the entire body first: a reader error after a complete line must not
	// turn a truncated history into a successful read and subsequent overwrite.
	content, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("read migration history: %w", err)
	}
	reader := strings.NewReader(string(content))
	var history []string
	for {
		var line string
		_, err := fmt.Fscanln(reader, &line)
		if errors.Is(err, io.EOF) {
			return history, nil
		}
		if err != nil {
			return nil, fmt.Errorf("parse migration history: %w", err)
		}
		history = append(history, line)
	}
}

func (b *S3Bucket) writeMigrationHistory(history []string, etag *string) error {
	s3Client, err := aws.GetS3Client()
	if err != nil {
		return err
	}
	input := &s3.PutObjectInput{
		Bucket:  awssdk.String(b.BucketName),
		Key:     awssdk.String(b.prefixedKey(".migrationhistory")),
		Body:    strings.NewReader(migrationHistoryContent(history)),
		IfMatch: etag,
	}
	if etag == nil {
		input.IfNoneMatch = awssdk.String("*")
	}
	_, err = s3Client.PutObject(context.TODO(), input)
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && (apiErr.ErrorCode() == "PreconditionFailed" || apiErr.ErrorCode() == "ConditionalRequestConflict") {
			return fmt.Errorf("%w: %w", errMigrationHistoryConflict, err)
		}
		return fmt.Errorf("PutObject error: %w", err)
	}
	return nil
}

func (b *S3Bucket) ApplyMigration(migrationId string) error {
	return updateMigrationHistory(migrationId, false, b.readMigrationHistory, b.writeMigrationHistory)
}

func (b *S3Bucket) RemoveMigrationFromHistory(migrationId string) error {
	return updateMigrationHistory(migrationId, true, b.readMigrationHistory, b.writeMigrationHistory)
}
