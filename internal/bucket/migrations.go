package bucket

import (
	"errors"
	"fmt"
	"strings"
)

type MigrationStorage interface {
	RetrieveMigrationHistory() ([]string, error)
	ApplyMigration(migrationId string) error
	RemoveMigrationFromHistory(migrationId string) error
}

var errMigrationHistoryConflict = errors.New("migration history changed concurrently")

const migrationHistoryWriteAttempts = 10

// Each retry reads the latest history and writes only if that version still
// exists. This also protects callers that do not hold the migration runner lock.
func updateMigrationHistory[Version any](migrationID string, remove bool, read func() ([]string, Version, error), write func([]string, Version) error) error {
	for attempt := 0; attempt < migrationHistoryWriteAttempts; attempt++ {
		history, version, err := read()
		if err != nil {
			return fmt.Errorf("RetrieveMigrationHistory error: %w", err)
		}
		found := false
		updated := make([]string, 0, len(history)+1)
		for _, id := range history {
			if id == migrationID {
				found = true
				if remove {
					continue
				}
			}
			updated = append(updated, id)
		}
		if (remove && !found) || (!remove && found) {
			return nil
		}
		if !remove {
			updated = append(updated, migrationID)
		}
		if err := write(updated, version); !errors.Is(err, errMigrationHistoryConflict) {
			return err
		}
	}
	return fmt.Errorf("updating migration history after %d attempts: %w", migrationHistoryWriteAttempts, errMigrationHistoryConflict)
}

func migrationHistoryContent(history []string) string {
	if len(history) == 0 {
		return ""
	}
	return strings.Join(history, "\n") + "\n"
}
