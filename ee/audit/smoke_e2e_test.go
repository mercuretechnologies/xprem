// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package audit

import (
	"context"
	"expo-open-ota/internal/auditlog"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestEndToEndArchiveSmoke exercises the real Postgres store, real archive export, and
// real NDJSON files on a local destination together, end to end.
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
	// Unique per run since the table and export cursor are shared across test runs.
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
	// Age the rows past the 30s visibility lag so the exporter sees them as settled.
	_, err := pool.Exec(ctx,
		"UPDATE audit_log_events SET occurred_at = now() - interval '1 minute' WHERE actor_id = $1",
		actorID)
	require.NoError(t, err)

	require.NoError(t, service.StartArchiveFromEnv(ctx))

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
