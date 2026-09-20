package bucket

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
)

func (b *GCSBucket) RetrieveMigrationHistory() ([]string, error) {
	history, _, err := b.readMigrationHistory()
	return history, err
}

func (b *GCSBucket) readMigrationHistory() ([]string, int64, error) {
	ctx := context.Background()
	bh, err := b.bucketHandle(ctx)
	if err != nil {
		return nil, 0, err
	}
	obj := bh.Object(b.prefixedKey(".migrationhistory"))
	r, err := obj.NewReader(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotExist) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	defer r.Close()
	content, err := io.ReadAll(r)
	if err != nil {
		return nil, 0, err
	}
	if r.Attrs.Generation == 0 {
		return nil, 0, errors.New("migration history response has no generation")
	}
	var migrations []string
	for _, line := range strings.Split(strings.TrimSpace(string(content)), "\n") {
		if line != "" {
			migrations = append(migrations, line)
		}
	}
	return migrations, r.Attrs.Generation, nil
}

func (b *GCSBucket) writeMigrationHistory(history []string, generation int64) error {
	ctx := context.Background()
	bh, err := b.bucketHandle(ctx)
	if err != nil {
		return err
	}
	conditions := storage.Conditions{GenerationMatch: generation}
	if generation == 0 {
		conditions.DoesNotExist = true
	}
	w := bh.Object(b.prefixedKey(".migrationhistory")).If(conditions).NewWriter(ctx)
	if _, err := io.WriteString(w, migrationHistoryContent(history)); err != nil {
		_ = w.Close()
		return gcsMigrationHistoryWriteError(err)
	}
	return gcsMigrationHistoryWriteError(w.Close())
}

func gcsMigrationHistoryWriteError(err error) error {
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) && apiErr.Code == 412 {
		return fmt.Errorf("%w: %w", errMigrationHistoryConflict, err)
	}
	return err
}

func (b *GCSBucket) ApplyMigration(migrationId string) error {
	return updateMigrationHistory(migrationId, false, b.readMigrationHistory, b.writeMigrationHistory)
}

func (b *GCSBucket) RemoveMigrationFromHistory(migrationId string) error {
	return updateMigrationHistory(migrationId, true, b.readMigrationHistory, b.writeMigrationHistory)
}
