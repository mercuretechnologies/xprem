package bucket

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"xprem/internal/helpers"
	"xprem/internal/providers/aws"
	"xprem/internal/types"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"golang.org/x/sync/errgroup"
)

type S3Bucket struct {
	BucketName string
	KeyPrefix  string
}

func (b *S3Bucket) prefixedKey(key string) string {
	return b.KeyPrefix + key
}

func (b *S3Bucket) DeleteUpdateFolder(appId, branch, runtimeVersion, updateId string) error {
	if b.BucketName == "" {
		return errors.New("BucketName not set")
	}

	s3Client, err := aws.GetS3Client()
	if err != nil {
		return fmt.Errorf("error getting S3 client: %w", err)
	}

	prefix := b.prefixedKey(fmt.Sprintf("%s/%s/%s/%s/", appId, branch, runtimeVersion, updateId))

	listInput := &s3.ListObjectsV2Input{
		Bucket: awssdk.String(b.BucketName),
		Prefix: awssdk.String(prefix),
	}

	var objects []s3types.ObjectIdentifier

	paginator := s3.NewListObjectsV2Paginator(s3Client, listInput)
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(context.TODO())
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

		_, err := s3Client.DeleteObjects(context.TODO(), deleteInput)
		if err != nil {
			return fmt.Errorf("failed to delete objects: %w", err)
		}
	}

	return nil
}

func (b *S3Bucket) GetRuntimeVersions(appId string, branch string) ([]types.RuntimeVersionWithStats, error) {
	if b.BucketName == "" {
		return nil, errors.New("BucketName not set")
	}
	s3Client, errS3 := aws.GetS3Client()
	if errS3 != nil {
		return nil, errS3
	}

	branchPrefix := b.prefixedKey(appId + "/" + branch + "/")
	input := &s3.ListObjectsV2Input{
		Bucket:    awssdk.String(b.BucketName),
		Prefix:    awssdk.String(branchPrefix),
		Delimiter: awssdk.String("/"),
	}
	resp, err := s3Client.ListObjectsV2(context.TODO(), input)
	if err != nil {
		return nil, fmt.Errorf("ListObjectsV2 error: %w", err)
	}

	var runtimeVersions []types.RuntimeVersionWithStats
	prefixLen := len(branchPrefix)

	for _, commonPrefix := range resp.CommonPrefixes {
		runtimeVersion := (*commonPrefix.Prefix)[prefixLen : len(*commonPrefix.Prefix)-1]
		updatesPath := *commonPrefix.Prefix
		updateInput := &s3.ListObjectsV2Input{
			Bucket:    awssdk.String(b.BucketName),
			Prefix:    awssdk.String(updatesPath),
			Delimiter: awssdk.String("/"),
		}
		updateResp, err := s3Client.ListObjectsV2(context.TODO(), updateInput)
		if err != nil {
			return nil, fmt.Errorf("ListObjectsV2 error in updates: %w", err)
		}

		var updateTimestamps []int64
		for _, commonPrefix := range updateResp.CommonPrefixes {
			updateID := strings.TrimSuffix((*commonPrefix.Prefix)[len(updatesPath):], "/")
			_, err := s3Client.HeadObject(context.TODO(), &s3.HeadObjectInput{
				Bucket: awssdk.String(b.BucketName),
				Key:    awssdk.String(*commonPrefix.Prefix + ".check"),
			})
			if err != nil {
				continue
			}
			timestamp, err := strconv.ParseInt(updateID, 10, 64)
			if err != nil {
				continue
			}
			updateTimestamps = append(updateTimestamps, timestamp)
		}

		if len(updateTimestamps) == 0 {
			continue
		}

		sort.Slice(updateTimestamps, func(i, j int) bool { return updateTimestamps[i] < updateTimestamps[j] })

		runtimeVersions = append(runtimeVersions, types.RuntimeVersionWithStats{
			RuntimeVersion:  runtimeVersion,
			CreatedAt:       helpers.NormalizeTimestamp(updateTimestamps[0]).Format(time.RFC3339),
			LastUpdatedAt:   helpers.NormalizeTimestamp(updateTimestamps[len(updateTimestamps)-1]).Format(time.RFC3339),
			NumberOfUpdates: len(updateTimestamps),
		})
	}

	return runtimeVersions, nil
}

func (b *S3Bucket) GetBranches(appId string) ([]string, error) {
	if b.BucketName == "" {
		return nil, errors.New("BucketName not set")
	}
	s3Client, errS3 := aws.GetS3Client()
	if errS3 != nil {
		return nil, errS3
	}
	appPrefix := b.prefixedKey(appId + "/")
	input := &s3.ListObjectsV2Input{
		Bucket:    awssdk.String(b.BucketName),
		Prefix:    awssdk.String(appPrefix),
		Delimiter: awssdk.String("/"),
	}
	resp, err := s3Client.ListObjectsV2(context.TODO(), input)
	if err != nil {
		return nil, fmt.Errorf("ListObjectsV2 error: %w", err)
	}
	var branches []string
	for _, commonPrefix := range resp.CommonPrefixes {
		prefix := *commonPrefix.Prefix
		branch := strings.TrimPrefix(prefix[:len(prefix)-1], appPrefix)
		branches = append(branches, branch)
	}
	return branches, nil
}

func (b *S3Bucket) GetUpdates(appId string, branch string, runtimeVersion string) ([]types.Update, error) {
	if b.BucketName == "" {
		return nil, errors.New("BucketName not set")
	}
	s3Client, errS3 := aws.GetS3Client()
	if errS3 != nil {
		return nil, errS3
	}
	prefix := b.prefixedKey(appId + "/" + branch + "/" + runtimeVersion + "/")
	input := &s3.ListObjectsV2Input{
		Bucket:    awssdk.String(b.BucketName),
		Prefix:    awssdk.String(prefix),
		Delimiter: awssdk.String("/"),
	}
	resp, err := s3Client.ListObjectsV2(context.TODO(), input)
	if err != nil {
		return nil, fmt.Errorf("ListObjectsV2 error: %w", err)
	}
	var updates []types.Update
	for _, commonPrefix := range resp.CommonPrefixes {
		var updateId int64
		if _, err := fmt.Sscanf(*commonPrefix.Prefix, prefix+"%d/", &updateId); err == nil {
			updates = append(updates, types.Update{
				AppId:          appId,
				Branch:         branch,
				RuntimeVersion: runtimeVersion,
				UpdateId:       strconv.FormatInt(updateId, 10),
				CreatedAt:      helpers.NormalizeTimestampToDuration(updateId),
			})
		}
	}
	return updates, nil
}

func (b *S3Bucket) GetFile(update types.Update, assetPath string) (*types.BucketFile, error) {
	if b.BucketName == "" {
		return nil, errors.New("BucketName not set")
	}
	key := b.prefixedKey(update.AppId + "/" + update.Branch + "/" + update.RuntimeVersion + "/" + update.UpdateId + "/" + assetPath)

	s3Client, err := aws.GetS3Client()
	if err != nil {
		return nil, err
	}

	input := &s3.GetObjectInput{
		Bucket: awssdk.String(b.BucketName),
		Key:    awssdk.String(key),
	}
	resp, err := s3Client.GetObject(context.TODO(), input)
	if err != nil {
		var noSuchKey *s3types.NoSuchKey
		if errors.As(err, &noSuchKey) {
			return nil, nil
		}
		return nil, fmt.Errorf("GetObject error: %w", err)
	}

	return &types.BucketFile{
		Reader:    resp.Body,
		CreatedAt: *resp.LastModified,
	}, nil
}

func (b *S3Bucket) RequestUploadUrlForFileUpdate(appId string, branch string, runtimeVersion string, updateId string, fileName string) (string, error) {
	if b.BucketName == "" {
		return "", errors.New("BucketName not set")
	}

	s3Client, err := aws.GetS3Client()
	if err != nil {
		return "", fmt.Errorf("error getting S3 client: %w", err)
	}

	presignClient := s3.NewPresignClient(s3Client)

	key := b.prefixedKey(fmt.Sprintf("%s/%s/%s/%s/%s", appId, branch, runtimeVersion, updateId, fileName))

	input := &s3.PutObjectInput{
		Bucket: awssdk.String(b.BucketName),
		Key:    awssdk.String(key),
	}

	presignResult, err := presignClient.PresignPutObject(context.TODO(), input, func(opt *s3.PresignOptions) {
		opt.Expires = 15 * time.Minute
	})
	if err != nil {
		return "", fmt.Errorf("error presigning URL: %w", err)
	}

	return presignResult.URL, nil
}

func (b *S3Bucket) UploadFileIntoUpdate(update types.Update, fileName string, file io.Reader) error {
	if b.BucketName == "" {
		return errors.New("BucketName not set")
	}
	s3Client, err := aws.GetS3Client()
	if err != nil {
		return err
	}
	key := b.prefixedKey(fmt.Sprintf("%s/%s/%s/%s/%s", update.AppId, update.Branch, update.RuntimeVersion, update.UpdateId, fileName))
	input := &s3.PutObjectInput{
		Bucket: awssdk.String(b.BucketName),
		Key:    awssdk.String(key),
		Body:   file,
	}
	_, err = s3Client.PutObject(context.TODO(), input)
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

func (b *S3Bucket) CreateUpdateFrom(previousUpdate *types.Update, newUpdateId string) (*types.Update, error) {
	if b.BucketName == "" {
		return nil, errors.New("BucketName not set")
	}
	if previousUpdate == nil {
		return nil, errors.New("previousUpdate is nil")
	}
	if previousUpdate.UpdateId == "" {
		return nil, errors.New("previousUpdate.UpdateId is empty")
	}
	if newUpdateId == "" {
		return nil, errors.New("newUpdateId is empty")
	}

	s3Client, err := aws.GetS3Client()
	if err != nil {
		return nil, fmt.Errorf("error getting S3 client: %w", err)
	}

	sourcePrefix := b.prefixedKey(fmt.Sprintf("%s/%s/%s/%s/", previousUpdate.AppId, previousUpdate.Branch, previousUpdate.RuntimeVersion, previousUpdate.UpdateId))
	targetPrefix := b.prefixedKey(fmt.Sprintf("%s/%s/%s/%s/", previousUpdate.AppId, previousUpdate.Branch, previousUpdate.RuntimeVersion, newUpdateId))

	paginator := s3.NewListObjectsV2Paginator(s3Client, &s3.ListObjectsV2Input{
		Bucket: awssdk.String(b.BucketName),
		Prefix: awssdk.String(sourcePrefix),
	})

	g, gctx := errgroup.WithContext(context.TODO())
	g.SetLimit(runtime.NumCPU())

	var listErr error
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(gctx)
		if err != nil {
			listErr = fmt.Errorf("failed to list objects: %w", err)
			break
		}

		for _, object := range page.Contents {
			key := *object.Key
			relPath := strings.TrimPrefix(key, sourcePrefix)

			if relPath == "update-metadata.json" || relPath == ".check" || strings.HasSuffix(relPath, "/") {
				continue
			}

			srcKey := key
			dstKey := targetPrefix + relPath

			g.Go(func() error {
				getObjOutput, err := s3Client.GetObject(gctx, &s3.GetObjectInput{
					Bucket: awssdk.String(b.BucketName),
					Key:    awssdk.String(srcKey),
				})
				if err != nil {
					return fmt.Errorf("error getting object %s: %w", srcKey, err)
				}
				defer getObjOutput.Body.Close()

				_, err = s3Client.PutObject(gctx, &s3.PutObjectInput{
					Bucket:        awssdk.String(b.BucketName),
					Key:           awssdk.String(dstKey),
					Body:          getObjOutput.Body,
					ContentLength: getObjOutput.ContentLength,
				})
				if err != nil {
					return fmt.Errorf("error putting object %s: %w", dstKey, err)
				}
				return nil
			})
		}
	}

	// A worker error cancels gctx, which also aborts the listing above; the
	// worker error is the interesting one, so report it first.
	if err := g.Wait(); err != nil {
		return nil, err
	}
	if listErr != nil {
		return nil, listErr
	}

	updateId, err := strconv.ParseInt(newUpdateId, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("error parsing update ID: %w", err)
	}
	return &types.Update{
		AppId:          previousUpdate.AppId,
		Branch:         previousUpdate.Branch,
		RuntimeVersion: previousUpdate.RuntimeVersion,
		UpdateId:       newUpdateId,
		CreatedAt:      helpers.NormalizeTimestampToDuration(updateId),
	}, nil
}

func (b *S3Bucket) RetrieveMigrationHistory() ([]string, error) {
	if b.BucketName == "" {
		return nil, errors.New("BucketName not set")
	}
	s3Client, errS3 := aws.GetS3Client()
	if errS3 != nil {
		return nil, errS3
	}
	input := &s3.GetObjectInput{
		Bucket: awssdk.String(b.BucketName),
		Key:    awssdk.String(b.prefixedKey(".migrationhistory")),
	}
	resp, err := s3Client.GetObject(context.TODO(), input)
	if err != nil {
		var noSuchKey *s3types.NoSuchKey
		if errors.As(err, &noSuchKey) {
			// handle empty migration history if file doesn't exist (first time setup)
			return nil, nil
		}
		return nil, fmt.Errorf("GetObject error: %w", err)
	}
	defer resp.Body.Close()
	var migrationHistory []string
	for {
		var line string
		_, err := fmt.Fscanln(resp.Body, &line)
		if err != nil {
			break
		}
		migrationHistory = append(migrationHistory, line)
	}
	return migrationHistory, nil
}

func (b *S3Bucket) ApplyMigration(migrationId string) error {
	if b.BucketName == "" {
		return errors.New("BucketName not set")
	}

	migrationHistory, err := b.RetrieveMigrationHistory()
	if err != nil {
		return fmt.Errorf("RetrieveMigrationHistory error: %w", err)
	}
	isAlreadyApplied := false
	for _, id := range migrationHistory {
		if id == migrationId {
			isAlreadyApplied = true
			break
		}
	}
	if isAlreadyApplied {
		return nil
	}

	s3Client, errS3 := aws.GetS3Client()
	if errS3 != nil {
		return errS3
	}

	var currentContent []byte
	obj, err := s3Client.GetObject(context.TODO(), &s3.GetObjectInput{
		Bucket: awssdk.String(b.BucketName),
		Key:    awssdk.String(b.prefixedKey(".migrationhistory")),
	})
	if err == nil {
		defer obj.Body.Close()
		currentContent, _ = io.ReadAll(obj.Body)
	}

	newContent := append(currentContent, []byte(migrationId+"\n")...)

	_, err = s3Client.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket: awssdk.String(b.BucketName),
		Key:    awssdk.String(b.prefixedKey(".migrationhistory")),
		Body:   bytes.NewReader(newContent),
	})
	if err != nil {
		return fmt.Errorf("PutObject error: %w", err)
	}

	return nil
}

func (b *S3Bucket) RemoveMigrationFromHistory(migrationId string) error {
	if b.BucketName == "" {
		return errors.New("BucketName not set")
	}

	migrationHistory, err := b.RetrieveMigrationHistory()
	if err != nil {
		return fmt.Errorf("RetrieveMigrationHistory error: %w", err)
	}

	hasMigration := false
	for _, id := range migrationHistory {
		if id == migrationId {
			hasMigration = true
			break
		}
	}
	if !hasMigration {
		return nil
	}

	var newContent []byte
	for _, id := range migrationHistory {
		if id != migrationId {
			newContent = append(newContent, []byte(id+"\n")...)
		}
	}

	s3Client, errS3 := aws.GetS3Client()
	if errS3 != nil {
		return errS3
	}

	_, err = s3Client.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket: awssdk.String(b.BucketName),
		Key:    awssdk.String(b.prefixedKey(".migrationhistory")),
		Body:   bytes.NewReader(newContent),
	})
	if err != nil {
		return fmt.Errorf("PutObject error: %w", err)
	}

	return nil
}
