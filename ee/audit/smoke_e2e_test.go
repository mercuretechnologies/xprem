// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package audit

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"xprem/internal/auditlog"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestEndToEndArchiveSmoke runs the whole real chain at once: real Postgres
// store, real StartArchiveFromEnv (destination resolution, advisory lock,
// cursor CAS, boot catch-up goroutine), real NDJSON files on a real local
// destination. The layer-by-layer tests prove each mechanism; this one proves
// they are actually wired to each other.
func TestEndToEndArchiveSmoke(t *testing.T) {
	auditStore, pool := setupAuditStore(t)
	archiveDir := t.TempDir()
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_AUDIT_LOGS_BASE_PATH", archiveDir)
	t.Setenv("ARCHIVE_AUDIT_LOGS", "true")
	t.Setenv("AUDIT_LOGS_EXPORT_INTERVAL_SECONDS", "60")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	service := NewAuditService(auditStore)
	service.licenseValid = func() bool { return true }
	// Unique like every other DB test here: the table and the export cursor
	// are shared across runs, a fixed actor would collide with itself.
	actorID := "smoke-" + uuid.NewString()
	var lastID int64
	for range 3 {
		inserted, err := auditStore.Insert(ctx, Event{
			ActorType: auditlog.ActorUser, ActorID: actorID, ActorDisplay: "smoke@example.com",
			Action: auditlog.ActionUserLogin, TargetType: "user", TargetID: actorID,
			Outcome: auditlog.OutcomeSuccess,
		})
		require.NoError(t, err)
		lastID = inserted.ID
	}
	// Age the rows past the 30s visibility lag, like a real deployment where
	// the exporter only ever sees settled rows.
	_, err := pool.Exec(ctx,
		"UPDATE audit_log_events SET occurred_at = now() - interval '1 minute' WHERE actor_id = $1",
		actorID)
	require.NoError(t, err)

	require.NoError(t, service.StartArchiveFromEnv(ctx))

	// The boot catch-up export runs in its goroutine: wait for the cursor to
	// pass our rows, then check the bytes actually on disk.
	require.Eventually(t, func() bool {
		cursor, cursorErr := auditStore.ExportCursor(ctx)
		return cursorErr == nil && cursor >= lastID
	}, 15*time.Second, 100*time.Millisecond, "the exporter never caught up past our rows")

	var archived strings.Builder
	require.NoError(t, filepath.WalkDir(archiveDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		require.True(t, strings.HasSuffix(path, ".ndjson"), "unexpected file %s", path)
		content, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		archived.Write(content)
		return nil
	}))
	occurrences := strings.Count(archived.String(), actorID)
	// 3 events, each naming the actor as actorId and targetId: 6 mentions.
	require.Equal(t, 6, occurrences, "the 3 events must land exactly once each in the NDJSON archive")
	require.Contains(t, archived.String(), `"action":"user.login"`)
}
