package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"xprem/internal/bucket"
	"xprem/internal/services"
	"xprem/internal/types"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
)

type pendingCacheUploadRepo struct{ services.BuildCacheRepository }

func (pendingCacheUploadRepo) Get(_ context.Context, appID, identifierID, id string) (*types.BuildCacheObject, error) {
	return &types.BuildCacheObject{ID: id, AppID: appID, AppIdentifierID: identifierID, Namespace: types.BuildCacheGradle, Size: 5}, nil
}

func TestBuildCacheLocalUploadRejectsCloudStorage(t *testing.T) {
	t.Setenv("STORAGE_MODE", "s3")
	t.Setenv("S3_BUCKET_NAME", "updates")
	t.Setenv("S3_BUCKET_BUILDS_NAME", "")
	artifactStore, err := bucket.OpenBuildArtifactStore()
	require.NoError(t, err)
	handler := NewBuildCacheHandler(services.NewBuildCacheService(pendingCacheUploadRepo{}, artifactStore.CacheStore()))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/cache/uploads/"+registryBuild, strings.NewReader("bytes"))
	req = mux.SetURLVars(req, map[string]string{"APP_ID": registryApp, "UPLOAD_ID": registryBuild})
	req = req.WithContext(services.WithBuildIdentifier(req.Context(), registryIdentifier))
	response := httptest.NewRecorder()
	handler.UploadLocal(response, req)
	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Contains(t, response.Body.String(), "signed bucket URL")
}

func TestBuildCacheRefusesWhileBuildsAreOff(t *testing.T) {
	handler := NewBuildCacheHandler(services.NewBuildCacheService(pendingCacheUploadRepo{}, nil))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/cache/uploads/"+registryBuild, strings.NewReader("bytes"))
	req = mux.SetURLVars(req, map[string]string{"APP_ID": registryApp, "UPLOAD_ID": registryBuild})
	req = req.WithContext(services.WithBuildIdentifier(req.Context(), registryIdentifier))
	response := httptest.NewRecorder()
	handler.UploadLocal(response, req)
	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Contains(t, response.Body.String(), "builds are turned off")
}
