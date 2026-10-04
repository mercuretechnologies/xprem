package repository_test

import (
	"context"
	"sync"
	"testing"
	"xprem/internal/repository"
	"xprem/internal/types"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestBuildLogsOrderedIdempotentAndScoped(t *testing.T) {
	f := setupBuildStore(t)
	ctx := context.Background()
	id := uuid.NewString()
	_, _, err := f.builds.Create(ctx, f.record(id, types.BuildStatusBuilding))
	require.NoError(t, err)
	firstContent := `{"buildStepId":"general","buildStepDisplayName":"Build","time":"2026-09-09T10:00:00Z","level":30,"msg":"héllo"}` + "\n"
	secondContent := `{"buildStepId":"general","buildStepDisplayName":"Build","time":"2026-09-09T10:00:01Z","level":30,"msg":"done"}` + "\n"
	secondOffset := int32(len(firstContent))
	thirdOffset := secondOffset + int32(len(secondContent))
	// An uncertain response can be retried concurrently without duplicating output.
	var wg sync.WaitGroup
	failures := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			failures <- f.builds.AppendLogs(ctx, f.app, id, 0, firstContent)
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	require.ErrorIs(t, f.builds.AppendLogs(ctx, f.app, id, 0, "different"), repository.ErrBuildLogOffset)
	require.ErrorIs(t, f.builds.AppendLogs(ctx, f.app, id, secondOffset+1, secondContent), repository.ErrBuildLogOffset)
	require.ErrorIs(t, f.builds.AppendLogs(ctx, f.app, id, 1, firstContent), repository.ErrBuildLogOffset)
	require.NoError(t, f.builds.AppendLogs(ctx, f.app, id, secondOffset, secondContent))
	logs, err := f.builds.ListLogs(ctx, f.app, id, 0)
	require.NoError(t, err)
	require.Len(t, logs, 2)
	require.Equal(t, firstContent, logs[0].Content)
	require.Equal(t, secondOffset, logs[1].Offset)
	logs, err = f.builds.ListLogs(ctx, f.app, id, secondOffset)
	require.NoError(t, err)
	require.Len(t, logs, 1)
	content := `{"buildStepId":"general","buildStepDisplayName":"Build","time":"2026-09-09T10:00:00Z","level":30,"msg":"structured"}` + "\n"
	require.NoError(t, f.builds.AppendLogs(ctx, f.app, id, thirdOffset, content))
	require.NoError(t, f.builds.AppendLogs(ctx, f.app, id, thirdOffset, content))
	require.ErrorIs(t, f.builds.AppendLogs(ctx, f.app, id, thirdOffset, secondContent), repository.ErrBuildLogOffset)
	logs, err = f.builds.ListLogs(ctx, f.app, id, thirdOffset)
	require.NoError(t, err)
	require.Len(t, logs, 1)
	require.Equal(t, content, logs[0].Content)
	require.Error(t, f.builds.AppendLogs(ctx, uuid.NewString(), id, thirdOffset, "other app"))
	logs, err = f.builds.ListLogs(ctx, uuid.NewString(), id, 0)
	require.NoError(t, err)
	require.Empty(t, logs)
	_, err = f.pool.Exec(ctx, "DELETE FROM builds WHERE id=$1", id)
	require.NoError(t, err)
	var count int
	require.NoError(t, f.pool.QueryRow(ctx, "SELECT count(*) FROM build_log_chunks WHERE build_id=$1", id).Scan(&count))
	require.Zero(t, count)
}
