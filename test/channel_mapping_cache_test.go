package test

import (
	cache2 "expo-open-ota/internal/cache"
	"expo-open-ota/internal/providers/expo"
	infrastructure "expo-open-ota/internal/router"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jarcoal/httpmock"
	"github.com/stretchr/testify/assert"
)

func TestChannelMappingIsCached(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockWorkingExpoResponse("staging")

	// First call, hits the Expo GraphQL API
	mapping1, err := expo.FetchChannelMapping("test-app-id", "staging")
	assert.NoError(t, err)
	assert.NotNil(t, mapping1)
	assert.Equal(t, "branch-1", mapping1.BranchName)

	// Reset mock, if cache works, next call won't need the API
	httpmock.Reset()

	// Second call, mock is gone, so this must use cache
	mapping2, err := expo.FetchChannelMapping("test-app-id", "staging")
	assert.NoError(t, err)
	assert.NotNil(t, mapping2)
	assert.Equal(t, mapping1.BranchName, mapping2.BranchName)
	assert.Equal(t, mapping1.Id, mapping2.Id)
}

func TestUpdateChannelBranchMappingInvalidatesChannelMappingCache(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockWorkingExpoResponse("staging")

	// Populate the channel mapping cache
	mapping1, err := expo.FetchChannelMapping("test-app-id", "staging")
	assert.NoError(t, err)
	assert.NotNil(t, mapping1)
	assert.Equal(t, "branch-1", mapping1.BranchName)

	// Verify cache is populated (reset mock, cached call should still work)
	httpmock.Reset()
	cachedMapping, err := expo.FetchChannelMapping("test-app-id", "staging")
	assert.NoError(t, err)
	assert.Equal(t, "branch-1", cachedMapping.BranchName)

	// Register mocks for the update handler + the subsequent fetch with a different mapping
	httpmock.RegisterResponder("POST", "https://api.expo.dev/graphql",
		func(req *http.Request) (*http.Response, error) {
			if req.Header.Get("operationName") == "UpdateChannelBranchMapping" {
				return httpmock.NewJsonResponse(http.StatusOK, map[string]interface{}{
					"data": map[string]interface{}{
						"updateChannel": map[string]interface{}{
							"editUpdateChannel": map[string]interface{}{
								"id": "staging-id",
							},
						},
					},
				})
			}
			if req.Header.Get("operationName") == "FetchExpoChannelMapping" {
				return MockExpoChannelMapping(
					[]map[string]interface{}{
						{"id": "branch-1-id", "name": "branch-1"},
						{"id": "branch-2-id", "name": "branch-2"},
					},
					map[string]interface{}{
						"id":   "staging-id",
						"name": "staging",
						"branchMapping": StringifyBranchMapping(map[string]interface{}{
							"version": 0,
							"data": []map[string]interface{}{
								{
									"branchId":           "branch-2-id",
									"branchMappingLogic": "true",
								},
							},
						}),
					},
				)
			}
			return httpmock.NewStringResponse(404, "Unknown operation"), nil
		})

	// Call UpdateChannelBranchMappingHandler via the router, this should invalidate the cache.
	// The channel's id and name differ on purpose, mirroring what the dashboard
	// sends: the remap is keyed by id while the mapping cache is keyed by name,
	// so invalidating with the id would silently leave the stale entry behind.
	router := infrastructure.NewRouter(testContainer())
	body := `{"releaseChannelId":"staging-id","releaseChannelName":"staging"}`
	req, _ := http.NewRequest("POST", "/api/apps/test-app-id/branch/branch-2-id/updateChannelBranchMapping", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+login().Token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNoContent, w.Code)

	// Verify the channel mapping cache key was deleted
	cache := cache2.GetCache()
	cacheKey := expo.ComputeChannelMappingCacheKey("test-app-id", "staging")
	assert.Equal(t, "", cache.Get(cacheKey), "Channel mapping cache should be invalidated after handler call")

	// FetchExpoChannelMapping should now hit the API and return the updated mapping
	mapping2, err := expo.FetchChannelMapping("test-app-id", "staging")
	assert.NoError(t, err)
	assert.NotNil(t, mapping2)
	assert.Equal(t, "branch-2", mapping2.BranchName, "After UpdateChannelBranchMappingHandler, should fetch new mapping from API")
}
