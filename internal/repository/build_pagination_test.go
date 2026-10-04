package repository_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
	"xprem/internal/handlers"
	"xprem/internal/services"
	"xprem/internal/types"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
)

func TestBuildPaginationContinuesAfterConcurrentChanges(t *testing.T) {
	f := setupBuildStore(t)
	ctx := context.Background()
	handler := handlers.NewBuildRegistryHandler(services.NewBuildService(f.builds, nil, nil))
	createdAt := time.Date(2026, 9, 13, 12, 0, 0, 123456000, time.UTC)
	ids := make([]string, 6)
	for i := range ids {
		ids[i] = fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)
		_, _, err := f.builds.Create(ctx, f.record(ids[i], types.BuildStatusBuilding))
		require.NoError(t, err)
		// All rows tie on creation time; continuation must also compare IDs.
		_, err = f.pool.Exec(ctx, "UPDATE builds SET created_at=$1 WHERE id=$2", createdAt, ids[i])
		require.NoError(t, err)
	}
	type page struct {
		Builds     []types.BuildRecord `json:"builds"`
		Count      int64               `json:"count"`
		NextCursor string              `json:"nextCursor"`
	}
	fetch := func(appID, cursor string) page {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/builds?limit=2&cursor="+url.QueryEscape(cursor), nil)
		req = mux.SetURLVars(req, map[string]string{"APP_ID": appID})
		response := httptest.NewRecorder()
		handler.List(response, req)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		var fetched page
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &fetched))
		return fetched
	}
	first := fetch(f.app, "")
	require.Len(t, first.Builds, 2)
	require.Equal(t, int64(6), first.Count)
	require.NotEmpty(t, first.NextCursor)
	seen := []string{first.Builds[0].ID, first.Builds[1].ID}
	require.Equal(t, []string{ids[5], ids[4]}, seen)

	// Newer builds must not shift the continuation or stop it early.
	for range 2 {
		_, _, err := f.builds.Create(ctx, f.record(uuid.NewString(), types.BuildStatusBuilding))
		require.NoError(t, err)
	}
	// A cursor must remain usable after the row it points to is deleted.
	_, err := f.pool.Exec(ctx, "DELETE FROM builds WHERE id=$1", ids[4])
	require.NoError(t, err)
	second := fetch(f.app, first.NextCursor)
	require.Len(t, second.Builds, 2)
	require.Equal(t, int64(7), second.Count)
	require.NotEmpty(t, second.NextCursor)
	third := fetch(f.app, second.NextCursor)
	require.Len(t, third.Builds, 2)
	require.Empty(t, third.NextCursor, "a full final page must terminate")
	for _, build := range append(second.Builds, third.Builds...) {
		seen = append(seen, build.ID)
	}
	require.Equal(t, []string{ids[5], ids[4], ids[3], ids[2], ids[1], ids[0]}, seen)
	refreshed := fetch(f.app, "")
	require.Len(t, refreshed.Builds, 2)
	for _, build := range refreshed.Builds {
		require.NotContains(t, seen, build.ID, "refresh includes the new builds")
	}
	empty := fetch(uuid.NewString(), first.NextCursor)
	require.NotNil(t, empty.Builds)
	require.Empty(t, empty.Builds)
	require.Zero(t, empty.Count)
	require.Empty(t, empty.NextCursor)
}
