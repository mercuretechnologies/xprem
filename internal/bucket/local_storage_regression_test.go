package bucket

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLocalBucketMetadataOnFreshRoot(t *testing.T) {
	for _, prefix := range []string{"", "nested/prefix/"} {
		t.Run(prefix, func(t *testing.T) {
			t.Run("instance ID", func(t *testing.T) {
				b := &LocalBucket{BasePath: filepath.Join(t.TempDir(), "bucket"), KeyPrefix: prefix}
				require.NoError(t, b.PersistInstanceID(t.Context(), "instance-1"))
				id, err := b.GetInstanceID(t.Context())
				require.NoError(t, err)
				require.Equal(t, "instance-1", id)
			})
			t.Run("migration", func(t *testing.T) {
				b := &LocalBucket{BasePath: filepath.Join(t.TempDir(), "bucket"), KeyPrefix: prefix}
				require.NoError(t, b.ApplyMigration("migration-1"))
				require.NoError(t, b.ApplyMigration("migration-1"))
				history, err := b.RetrieveMigrationHistory()
				require.NoError(t, err)
				require.Equal(t, []string{"migration-1"}, history)
			})
		})
	}
}

func TestLocalBucketMigrationHistoryReplacement(t *testing.T) {
	b := &LocalBucket{BasePath: t.TempDir()}
	require.NoError(t, b.ApplyMigration("migration-1"))
	require.NoError(t, b.ApplyMigration("migration-2"))
	historyPath := filepath.Join(b.rootPath(), ".migrationhistory")
	previous, err := os.Open(historyPath)
	require.NoError(t, err)
	defer previous.Close()

	require.NoError(t, b.RemoveMigrationFromHistory("migration-1"))
	history, err := b.RetrieveMigrationHistory()
	require.NoError(t, err)
	require.Equal(t, []string{"migration-2"}, history)
	// A reader of the old history must keep a complete snapshot while the
	// replacement is published, rather than observing an in-place truncation.
	oldContents, err := io.ReadAll(previous)
	require.NoError(t, err)
	require.Equal(t, "migration-1\nmigration-2\n", string(oldContents))

	require.NoError(t, b.RemoveMigrationFromHistory("migration-2"))
	contents, err := os.ReadFile(historyPath)
	require.NoError(t, err)
	require.Empty(t, contents)
	requireNoLeftovers(t, b.rootPath())
}

func TestLocalBucketFilesystemReadErrors(t *testing.T) {
	b := &LocalBucket{BasePath: filepath.Join(t.TempDir(), "bucket")}
	history, err := b.RetrieveMigrationHistory()
	require.NoError(t, err)
	require.Empty(t, history)
	updates, err := b.GetUpdates("app-1", "main", "1")
	require.NoError(t, err)
	require.Empty(t, updates)

	// A file where the root directory belongs is an I/O failure, not an
	// empty bucket. This remains deterministic even when tests run as root.
	require.NoError(t, os.WriteFile(b.BasePath, []byte("not a directory"), 0o600))
	_, err = b.RetrieveMigrationHistory()
	require.Error(t, err)
	_, err = b.GetUpdates("app-1", "main", "1")
	require.Error(t, err)
}

func TestLocalBucketCreatesPrivateDirectories(t *testing.T) {
	t.Setenv("DB_URL", "postgres://localhost/xprem")
	t.Setenv("JWT_SECRET", "test_jwt_secret")
	t.Setenv("BASE_URL", "http://localhost:3000")
	b := &LocalBucket{BasePath: filepath.Join(t.TempDir(), "bucket")}
	require.NoError(t, b.PutBlob(context.Background(), "app-1", testBlobHash, strings.NewReader("hello")))
	_, err := b.RequestUploadUrlForFileUpdate("app-1", "main", "1.0", "123", "bundle.js")
	require.NoError(t, err)
	require.NoError(t, b.UploadFileIntoUpdate(validUpdate(), "assets/icon.png", strings.NewReader("image")))
	update := validUpdate()
	_, err = b.CreateUpdateFrom(&update, "456")
	require.NoError(t, err)

	for _, relative := range []string{".", "app-1", "app-1/cas", "app-1/main/1.0/123", "app-1/main/1.0/123/assets", "app-1/main/1.0/456", "app-1/main/1.0/456/assets"} {
		info, err := os.Stat(filepath.Join(b.rootPath(), relative))
		require.NoError(t, err)
		require.Zero(t, info.Mode().Perm()&0o077, "directory %s must be private", relative)
	}
}

func TestValidateRelativePathRejectsAliasedNames(t *testing.T) {
	for _, name := range []string{".", "./file", "assets/./file", "assets/.", "assets//file", "assets/"} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, validateRelativePath("file name", name))
		})
	}
	for _, name := range []string{".check", "assets/.hidden", "assets/..hidden", "assets/icon.png"} {
		require.NoError(t, validateRelativePath("file name", name))
	}
}
