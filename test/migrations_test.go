package test

import (
	"context"
	"io"
	"testing"
	"time"
	"xprem/internal/bucket"
	"xprem/internal/bucketmigration"
	"xprem/internal/types"
)

type dummyMigrationsBucket struct {
	migrationsHistory []string
	actionsRecorded   []string
}

func (b *dummyMigrationsBucket) DeleteUpdateFolder(_, _, _, _ string) error {
	b.actionsRecorded = append(b.actionsRecorded, "DeleteUpdateFolder")
	return nil
}
func (b *dummyMigrationsBucket) RequestUploadUrlForFileUpdate(_, _, _, _, _ string) (string, error) {
	b.actionsRecorded = append(b.actionsRecorded, "RequestUploadUrlForFileUpdate")
	return "", nil
}
func (b *dummyMigrationsBucket) GetUpdates(_, _, _ string) ([]types.Update, error) {
	b.actionsRecorded = append(b.actionsRecorded, "GetUpdates")
	return nil, nil
}
func (b *dummyMigrationsBucket) GetFile(_ types.Update, _ string) (*types.BucketFile, error) {
	b.actionsRecorded = append(b.actionsRecorded, "GetFile")
	return nil, nil
}
func (b *dummyMigrationsBucket) GetBranches(_ string) ([]string, error) {
	b.actionsRecorded = append(b.actionsRecorded, "GetBranches")
	return nil, nil
}
func (b *dummyMigrationsBucket) GetRuntimeVersions(_, _ string) ([]types.RuntimeVersionWithStats, error) {
	b.actionsRecorded = append(b.actionsRecorded, "GetRuntimeVersions")
	return nil, nil
}
func (b *dummyMigrationsBucket) UploadFileIntoUpdate(_ types.Update, _ string, _ io.Reader) error {
	b.actionsRecorded = append(b.actionsRecorded, "UploadFileIntoUpdate")
	return nil
}
func (b *dummyMigrationsBucket) CopyFileIntoUpdate(_ types.Update, _ types.Update, _ string) error {
	b.actionsRecorded = append(b.actionsRecorded, "CopyFileIntoUpdate")
	return nil
}
func (b *dummyMigrationsBucket) CreateUpdateFrom(_ *types.Update, _ string) (*types.Update, error) {
	b.actionsRecorded = append(b.actionsRecorded, "CreateUpdateFrom")
	return nil, nil
}
func (b *dummyMigrationsBucket) GetInstanceID() (string, error) { return "", nil }
func (b *dummyMigrationsBucket) PersistInstanceID(_ string) error {
	return nil
}
func (b *dummyMigrationsBucket) RetrieveMigrationHistory() ([]string, error) {
	return b.migrationsHistory, nil
}
func (b *dummyMigrationsBucket) ApplyMigration(migrationId string) error {
	b.migrationsHistory = append(b.migrationsHistory, migrationId)
	return nil
}
func (b *dummyMigrationsBucket) RemoveMigrationFromHistory(migrationId string) error {
	for i, id := range b.migrationsHistory {
		if id == migrationId {
			b.migrationsHistory = append(b.migrationsHistory[:i], b.migrationsHistory[i+1:]...)
			break
		}
	}
	return nil
}
func (b *dummyMigrationsBucket) BlobExists(context.Context, string, string) (bool, error) {
	b.actionsRecorded = append(b.actionsRecorded, "BlobExists")
	return false, nil
}
func (b *dummyMigrationsBucket) GetBlob(context.Context, string, string) (*types.BucketFile, error) {
	b.actionsRecorded = append(b.actionsRecorded, "GetBlob")
	return nil, nil
}
func (b *dummyMigrationsBucket) PutBlob(context.Context, string, string, io.Reader) error {
	b.actionsRecorded = append(b.actionsRecorded, "PutBlob")
	return nil
}
func (b *dummyMigrationsBucket) BSDiffExists(context.Context, string, string, string) (bool, error) {
	b.actionsRecorded = append(b.actionsRecorded, "BSDiffExists")
	return false, nil
}
func (b *dummyMigrationsBucket) GetBSDiff(context.Context, string, string, string) (*types.BucketFile, error) {
	b.actionsRecorded = append(b.actionsRecorded, "GetBSDiff")
	return nil, nil
}
func (b *dummyMigrationsBucket) PutBSDiff(context.Context, string, string, string, io.Reader) error {
	b.actionsRecorded = append(b.actionsRecorded, "PutBSDiff")
	return nil
}
func (b *dummyMigrationsBucket) RequestBlobUploadURL(_, _, _ string) (string, error) {
	b.actionsRecorded = append(b.actionsRecorded, "RequestBlobUploadURL")
	return "", nil
}

func TestShouldNotRunAppliedMigrations(t *testing.T) {
	migrationA := bucketmigration.BaseMigration{
		Id:   "20250415_fake_migrationA",
		Time: time.Date(2025, 4, 15, 0, 0, 0, 0, time.UTC),
		UpFunc: func(b bucket.Bucket) error {
			b.DeleteUpdateFolder("", "", "", "")
			return nil
		},
		DownFunc: func(b bucket.Bucket) error {
			b.GetBranches("")
			return nil
		},
	}
	bucketmigration.ClearRegisteredMigrations()
	b := &dummyMigrationsBucket{
		migrationsHistory: []string{"20250415_fake_migrationA"},
		actionsRecorded:   []string{},
	}
	bucketmigration.Register(migrationA)
	err := bucketmigration.RunMigrations(b)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if len(b.migrationsHistory) != 1 {
		t.Fatalf("Expected 1 migration in history, got %d", len(b.migrationsHistory))
	}
	if b.migrationsHistory[0] != "20250415_fake_migrationA" {
		t.Fatalf("Expected migration ID '20250415_fake_migrationA', got '%s'", b.migrationsHistory[0])
	}
	if len(b.actionsRecorded) != 0 {
		t.Fatalf("Expected no actions recorded, got %d", len(b.actionsRecorded))
	}
}

func TestShouldRunMultipleMigrationsAsc(t *testing.T) {
	b := &dummyMigrationsBucket{
		migrationsHistory: []string{},
		actionsRecorded:   []string{},
	}
	bucketmigration.ClearRegisteredMigrations()
	migrationA := bucketmigration.BaseMigration{
		Id:   "20250415_fake_migrationA",
		Time: time.Date(2025, 4, 15, 0, 0, 0, 0, time.UTC),
		UpFunc: func(b bucket.Bucket) error {
			b.DeleteUpdateFolder("", "", "", "")
			return nil
		},
		DownFunc: func(b bucket.Bucket) error {
			b.GetBranches("")
			return nil
		},
	}
	migrationB := bucketmigration.BaseMigration{
		Id:   "20250416_fake_migrationB",
		Time: time.Date(2025, 4, 16, 0, 0, 0, 0, time.UTC),
		UpFunc: func(b bucket.Bucket) error {
			b.GetFile(types.Update{}, "")
			return nil
		},
		DownFunc: func(b bucket.Bucket) error {
			b.GetRuntimeVersions("", "")
			return nil
		},
	}
	bucketmigration.Register(migrationB)
	bucketmigration.Register(migrationA)

	err := bucketmigration.RunMigrations(b)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if len(b.migrationsHistory) != 2 {
		t.Fatalf("Expected 2 migrations in history, got %d", len(b.migrationsHistory))
	}
	if b.migrationsHistory[0] != "20250415_fake_migrationA" || b.migrationsHistory[1] != "20250416_fake_migrationB" {
		t.Fatalf("Expected migration IDs '20250415_fake_migrationA' and '20250416_fake_migrationB', got '%s' and '%s'", b.migrationsHistory[0], b.migrationsHistory[1])
	}
	if len(b.actionsRecorded) != 2 {
		t.Fatalf("Expected 2 actions recorded, got %d", len(b.actionsRecorded))
	}
	if b.actionsRecorded[0] != "DeleteUpdateFolder" || b.actionsRecorded[1] != "GetFile" {
		t.Fatalf("Expected actions 'DeleteUpdateFolder', 'GetBranches', 'GetFile', and 'GetRuntimeVersions', got '%s'", b.actionsRecorded)
	}

	err = bucketmigration.RollbackLastMigration(b)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	if len(b.migrationsHistory) != 1 {
		t.Fatalf("Expected 1 migration in history, got %d", len(b.migrationsHistory))
	}
	if b.migrationsHistory[0] != "20250415_fake_migrationA" {
		t.Fatalf("Expected migration ID '20250415_fake_migrationA', got '%s'", b.migrationsHistory[0])
	}
	if len(b.actionsRecorded) != 3 {
		t.Fatalf("Expected 3 actions recorded, got %d", len(b.actionsRecorded))
	}
	if b.actionsRecorded[0] != "DeleteUpdateFolder" || b.actionsRecorded[1] != "GetFile" || b.actionsRecorded[2] != "GetRuntimeVersions" {
		t.Fatalf("Expected action 'GetRuntimeVersions', got '%s'", b.actionsRecorded[2])
	}
	err = bucketmigration.RollbackLastMigration(b)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	if len(b.migrationsHistory) != 0 {
		t.Fatalf("Expected 0 migrations in history, got %d", len(b.migrationsHistory))
	}
	if len(b.actionsRecorded) != 4 {
		t.Fatalf("Expected 4 actions recorded, got %d", len(b.actionsRecorded))
	}
	if b.actionsRecorded[3] != "GetBranches" {
		t.Fatalf("Expected action 'GetBranches', got '%s'", b.actionsRecorded[3])
	}
}
