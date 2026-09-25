package test

import (
	"testing"
	"time"
	"xprem/internal/bucket"
	"xprem/internal/bucketmigration"
	"xprem/internal/objectstore"

	"github.com/stretchr/testify/require"
)

// recordingMigration records the direction it ran in, for a bucket kept in a
// temporary directory.
func recordingMigration(id string, day int, ran *[]string) bucketmigration.BaseMigration {
	return bucketmigration.BaseMigration{
		Id:   id,
		Time: time.Date(2025, 4, day, 0, 0, 0, 0, time.UTC),
		UpFunc: func(*bucket.Bucket) error {
			*ran = append(*ran, id+" up")
			return nil
		},
		DownFunc: func(*bucket.Bucket) error {
			*ran = append(*ran, id+" down")
			return nil
		},
	}
}

func TestShouldNotRunAppliedMigrations(t *testing.T) {
	b := bucket.Open(objectstore.ModeLocal, t.TempDir(), "")
	history := bucketmigration.NewHistory(b.ObjectStore)
	require.NoError(t, history.Record("20250415_fake_migrationA"))

	var ran []string
	bucketmigration.ClearRegisteredMigrations()
	bucketmigration.Register(recordingMigration("20250415_fake_migrationA", 15, &ran))

	require.NoError(t, bucketmigration.RunMigrations(b))

	applied, err := history.Applied()
	require.NoError(t, err)
	require.Equal(t, []string{"20250415_fake_migrationA"}, applied)
	require.Empty(t, ran)
}

func TestShouldRunMultipleMigrationsAsc(t *testing.T) {
	b := bucket.Open(objectstore.ModeLocal, t.TempDir(), "")
	history := bucketmigration.NewHistory(b.ObjectStore)

	var ran []string
	bucketmigration.ClearRegisteredMigrations()
	bucketmigration.Register(recordingMigration("20250416_fake_migrationB", 16, &ran))
	bucketmigration.Register(recordingMigration("20250415_fake_migrationA", 15, &ran))

	require.NoError(t, bucketmigration.RunMigrations(b))
	applied, err := history.Applied()
	require.NoError(t, err)
	require.Equal(t, []string{"20250415_fake_migrationA", "20250416_fake_migrationB"}, applied)
	require.Equal(t, []string{"20250415_fake_migrationA up", "20250416_fake_migrationB up"}, ran)

	require.NoError(t, bucketmigration.RollbackLastMigration(b))
	applied, err = history.Applied()
	require.NoError(t, err)
	require.Equal(t, []string{"20250415_fake_migrationA"}, applied)
	require.Equal(t, "20250416_fake_migrationB down", ran[2])

	require.NoError(t, bucketmigration.RollbackLastMigration(b))
	applied, err = history.Applied()
	require.NoError(t, err)
	require.Empty(t, applied)
	require.Equal(t, "20250415_fake_migrationA down", ran[3])
}
