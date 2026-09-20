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
	bucket.ResetBucketInstance()
	t.Cleanup(bucket.ResetBucketInstance)
	handler := NewBuildCacheHandler(services.NewBuildCacheService(pendingCacheUploadRepo{}, bucket.GetBucket()))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/cache/uploads/"+registryBuild, strings.NewReader("bytes"))
	req = mux.SetURLVars(req, map[string]string{"APP_ID": registryApp, "UPLOAD_ID": registryBuild})
	req = req.WithContext(services.WithBuildIdentifier(req.Context(), registryIdentifier))
	response := httptest.NewRecorder()
	handler.UploadLocal(response, req)
	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Contains(t, response.Body.String(), "signed bucket URL")
}
