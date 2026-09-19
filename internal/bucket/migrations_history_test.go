package bucket

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpdateMigrationHistoryStopsOnReadOrWriteFailure(t *testing.T) {
	failure := errors.New("storage unavailable")
	writes := 0
	err := updateMigrationHistory("target", false, func() ([]string, int, error) {
		return []string{"partial"}, 1, failure
	}, func([]string, int) error {
		writes++
		return nil
	})
	require.ErrorIs(t, err, failure)
	require.Zero(t, writes)

	err = updateMigrationHistory("target", false, func() ([]string, int, error) {
		return []string{"existing"}, 1, nil
	}, func([]string, int) error {
		writes++
		return failure
	})
	require.ErrorIs(t, err, failure)
	require.Equal(t, 1, writes)
}

func TestUpdateMigrationHistoryBoundsConflicts(t *testing.T) {
	writes := 0
	err := updateMigrationHistory("target", false, func() ([]string, int, error) {
		return nil, 0, nil
	}, func([]string, int) error {
		writes++
		return errMigrationHistoryConflict
	})
	require.ErrorIs(t, err, errMigrationHistoryConflict)
	require.Equal(t, migrationHistoryWriteAttempts, writes)
}

func TestUpdateMigrationHistorySkipsUnchangedHistory(t *testing.T) {
	for _, remove := range []bool{false, true} {
		id := "existing"
		if remove {
			id = "absent"
		}
		err := updateMigrationHistory(id, remove, func() ([]string, int, error) {
			return []string{"existing"}, 1, nil
		}, func([]string, int) error {
			t.Fatal("unchanged history must not be written")
			return nil
		})
		require.NoError(t, err)
	}
}

func TestReadS3MigrationHistoryIncludesFinalLine(t *testing.T) {
	history, err := readS3MigrationHistory(strings.NewReader("first\nsecond"))
	require.NoError(t, err)
	require.Equal(t, []string{"first", "second"}, history)
}
