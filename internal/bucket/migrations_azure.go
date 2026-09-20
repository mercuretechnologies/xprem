package bucket

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blockblob"
)

func (b *AzureBucket) RetrieveMigrationHistory() ([]string, error) {
	history, _, err := b.readMigrationHistory()
	return history, err
}

func (b *AzureBucket) readMigrationHistory() ([]string, *azcore.ETag, error) {
	ctx := context.Background()
	cc, err := b.containerClient()
	if err != nil {
		return nil, nil, err
	}
	resp, err := cc.NewBlobClient(b.prefixedKey(".migrationhistory")).DownloadStream(ctx, nil)
	if err != nil {
		if bloberror.HasCode(err, bloberror.BlobNotFound) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	defer resp.Body.Close()
	content, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	if resp.ETag == nil || *resp.ETag == "" {
		return nil, nil, errors.New("migration history response has no ETag")
	}
	var migrations []string
	for _, line := range strings.Split(strings.TrimSpace(string(content)), "\n") {
		if line != "" {
			migrations = append(migrations, line)
		}
	}
	return migrations, resp.ETag, nil
}

func (b *AzureBucket) writeMigrationHistory(history []string, etag *azcore.ETag) error {
	ctx := context.Background()
	cc, err := b.containerClient()
	if err != nil {
		return err
	}
	conditions := &blob.ModifiedAccessConditions{IfMatch: etag}
	if etag == nil {
		anyETag := azcore.ETagAny
		conditions.IfNoneMatch = &anyETag
	}
	_, err = cc.NewBlockBlobClient(b.prefixedKey(".migrationhistory")).UploadBuffer(ctx, []byte(migrationHistoryContent(history)), &blockblob.UploadBufferOptions{
		AccessConditions: &blob.AccessConditions{ModifiedAccessConditions: conditions},
	})
	if bloberror.HasCode(err, bloberror.ConditionNotMet, bloberror.BlobAlreadyExists) {
		return fmt.Errorf("%w: %w", errMigrationHistoryConflict, err)
	}
	return err
}

func (b *AzureBucket) ApplyMigration(migrationId string) error {
	return updateMigrationHistory(migrationId, false, b.readMigrationHistory, b.writeMigrationHistory)
}

func (b *AzureBucket) RemoveMigrationFromHistory(migrationId string) error {
	return updateMigrationHistory(migrationId, true, b.readMigrationHistory, b.writeMigrationHistory)
}
