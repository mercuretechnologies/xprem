package test

import (
	"context"
	"encoding/json"
	"github.com/jarcoal/httpmock"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
	"xprem/config"
	"xprem/internal/bucket"
	cache2 "xprem/internal/cache"
	"xprem/internal/cdn"
	"xprem/internal/crypto"
	"xprem/internal/handlers"
	"xprem/internal/metrics"
	infrastructure "xprem/internal/router"
	"xprem/internal/services"
	"xprem/internal/store"
	"xprem/internal/types"
)

func setup(t *testing.T) func() {
	GlobalBeforeEach()
	httpmock.Activate()
	SetValidConfiguration()
	metrics.InitMetrics()
	return func() {
		GlobalAfterEach(t)
		defer httpmock.DeactivateAndReset()
	}
}

// testContainer builds a DI container from the current env, bucket and app
// config. Request handlers were package-level funcs before the control-plane
// refactor; they are now methods on the container's handler structs, so tests
// resolve them through here (e.g. testContainer().ExpoProtocolHandler.
// HandleManifest). Built fresh per call so a test that mutates the bucket path
// or app registry before invoking a handler sees its change, the bucket is a
// per-test singleton, so repeated calls reuse the same backend.
func testContainer() *infrastructure.AppContainer {
	container, _ := infrastructure.InitDependencies(context.Background())
	return container
}

// testUpdateService builds an UpdateService over the current bucket, wired the
// same way as the stateless branch of wire.go. Needed by tests that call
// package-level service helpers (e.g. services.PreWarmManifestCache) directly
// rather than through a handler.
func testUpdateService() *services.UpdateService {
	resolvedBucket := bucket.GetBucket()
	return services.NewUpdateService(store.NewBucketUpdateStore(resolvedBucket), resolvedBucket)
}

// testLatestUpdate reads the newest update straight from the bucket store, the
// same one wire.go uses in stateless mode. Assertions want this rather than
// testUpdateService().GetLatestUpdate: the service reads through the lastUpdate
// cache, which would let an assertion pass on a value a previous step cached.
func testLatestUpdate(appId, branch, runtimeVersion string, platform types.Platform) (*types.Update, error) {
	return store.NewBucketUpdateStore(bucket.GetBucket()).
		GetLatestUpdate(context.Background(), appId, branch, runtimeVersion, platform)
}

// testUpdate and testUpdateType read an update and its type from the bucket
// store. That state is bucket-backed but belongs to the control plane, so it
// lives on the store rather than in internal/update, which assertions would
// otherwise reach for.
func testUpdate(appId, branch, runtimeVersion, updateId string) (*types.Update, error) {
	return store.NewBucketUpdateStore(bucket.GetBucket()).
		GetUpdate(context.Background(), appId, branch, runtimeVersion, updateId)
}

func testUpdateType(update types.Update) (types.UpdateType, error) {
	return store.NewBucketUpdateStore(bucket.GetBucket()).
		GetUpdateType(context.Background(), update)
}

func GlobalBeforeEach() {
	metrics.CleanupMetrics()
	cache := cache2.GetCache()
	_ = cache.Clear()
	newTime := time.Date(1990, time.January, 1, 0, 0, 0, 0, time.UTC)

	ChangeModTimeRecursively(os.Getenv("LOCAL_BUCKET_BASE_PATH"), newTime)
}

func GlobalAfterEach(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		bucket.ResetBucketInstance()
		cdn.ResetCDNInstance()
		projectRoot, err := findProjectRoot()
		if err != nil {
			t.Errorf("Error finding project root: %v", err)
		}
		// Clean both legacy path (./updates/DO_NOT_USE) and v2 multi-app path
		// (./updates/test-app-id/DO_NOT_USE), tests mix both depending on how
		// they set LOCAL_BUCKET_BASE_PATH.
		// The cas folders hold the blobs a presign run wrote: left behind, the
		// next run sees them as already uploaded and skips the upload requests.
		for _, casPath := range []string{
			filepath.Join(projectRoot, "./updates/cas"),
			filepath.Join(projectRoot, "./updates/test-app-id/cas"),
			filepath.Join(projectRoot, "./test/test-updates/test-app-id/cas"),
		} {
			if err := os.RemoveAll(casPath); err != nil {
				t.Errorf("Error removing cas directory: %v", err)
			}
		}
		for _, updatesPath := range []string{
			filepath.Join(projectRoot, "./updates/DO_NOT_USE"),
			filepath.Join(projectRoot, "./updates/test-app-id/DO_NOT_USE"),
		} {
			updates, err := os.ReadDir(updatesPath)
			if err != nil {
				continue
			}
			for _, update := range updates {
				if update.IsDir() {
					err = os.RemoveAll(filepath.Join(updatesPath, update.Name()))
					if err != nil {
						t.Errorf("Error removing update directory: %v", err)
					}
				}
			}
		}
		// Also remove all folders > 1674170951 in ./test/test-updates/test-app-id/branch-1/1
		fixturePath := filepath.Join(projectRoot, "./test/test-updates/test-app-id/branch-1/1")
		fixtureUpdates, err := os.ReadDir(fixturePath)
		if err != nil {
			t.Errorf("Error reading updates directory: %v", err)
		}
		for _, update := range fixtureUpdates {
			if update.IsDir() {
				updateTime, err := strconv.Atoi(update.Name())
				if err != nil {
					continue
				}
				if updateTime > 1674170951 {
					err = os.RemoveAll(filepath.Join(fixturePath, update.Name()))
					if err != nil {
						t.Errorf("Error removing update directory: %v", err)
					}
				}
			}
		}
	})

}

func findProjectRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(cwd, "go.mod")); err == nil {
			return cwd, nil
		}
		parent := filepath.Dir(cwd)
		if parent == cwd {
			break
		}
		cwd = parent
	}

	return "", os.ErrNotExist
}

func MockExpoChannelMapping(updateBranches []map[string]interface{}, updateChannelByName map[string]interface{}) (*http.Response, error) {
	return httpmock.NewJsonResponse(http.StatusOK, map[string]interface{}{
		"data": map[string]interface{}{
			"app": map[string]interface{}{
				"byId": map[string]interface{}{
					"id":                  "EXPO_APP_ID",
					"updateBranches":      updateBranches,
					"updateChannelByName": updateChannelByName,
				},
			},
		},
	})
}

func MockExpoBranchesMappingResponse(updateBranches []map[string]interface{}, updateChannelByName []map[string]interface{}) (*http.Response, error) {
	return httpmock.NewJsonResponse(http.StatusOK, map[string]interface{}{
		"data": map[string]interface{}{
			"app": map[string]interface{}{
				"byId": map[string]interface{}{
					"id":             "EXPO_APP_ID",
					"updateBranches": updateBranches,
					"updateChannels": updateChannelByName,
				},
			},
		},
	})
}

func MockExpoBranchesResponse(updateBranches []map[string]interface{}) (*http.Response, error) {
	return httpmock.NewJsonResponse(http.StatusOK, map[string]interface{}{
		"data": map[string]interface{}{
			"app": map[string]interface{}{
				"byId": map[string]interface{}{
					"id":             "EXPO_APP_ID",
					"updateBranches": updateBranches,
				},
			},
		},
	})
}

func MockExpoAccountResponse(me map[string]interface{}) (*http.Response, error) {
	return httpmock.NewJsonResponse(http.StatusOK, map[string]interface{}{
		"data": map[string]interface{}{
			"me": me,
		},
	})
}

func StringifyBranchMapping(branchMapping map[string]interface{}) string {
	branchMappingString, err := json.Marshal(branchMapping)
	if err != nil {
		panic(err)
	}
	return string(branchMappingString)
}

func mockWorkingExpoResponse(channelName string) {
	httpmock.RegisterResponder("POST", "https://api.expo.dev/graphql",
		func(req *http.Request) (*http.Response, error) {
			isFetchSelfExpoUsername := req.Header.Get("operationName") == "FetchExpoUserAccountInformations"
			isFetchExpoChannelMapping := req.Header.Get("operationName") == "FetchExpoChannelMapping"
			isFetchBranches := req.Header.Get("operationName") == "FetchExpoBranches"
			isCreateBranch := req.Header.Get("operationName") == "CreateBranch"
			if isFetchBranches {
				return MockExpoBranchesResponse([]map[string]interface{}{
					{
						"id":   "branch-1-id",
						"name": "branch-1",
					},
					{
						"id":   "branch-2-id",
						"name": "branch-2",
					},
				})
			}
			if isCreateBranch {
				return httpmock.NewJsonResponse(http.StatusOK, map[string]interface{}{
					"data": map[string]interface{}{
						"updateBranch": map[string]interface{}{
							"createUpdateBranchForApp": map[string]interface{}{
								"id":   "created-branch-id",
								"name": "created-branch",
							},
						},
					},
				})
			}
			if isFetchSelfExpoUsername {
				return MockExpoAccountResponse(map[string]interface{}{
					"id":       "test_id",
					"username": "test_username",
					"email":    "test_email",
				})
			}
			if isFetchExpoChannelMapping {
				return MockExpoChannelMapping(
					[]map[string]interface{}{
						{
							"id":   "branch-1-id",
							"name": "branch-1",
						},
						{
							"id":   "branch-2-id",
							"name": "branch-2",
						},
					},
					map[string]interface{}{
						"id":   channelName + "-id",
						"name": channelName,
						"branchMapping": StringifyBranchMapping(map[string]interface{}{
							"version": 0,
							"data": []map[string]interface{}{
								{
									"branchId":           "branch-1-id",
									"branchMappingLogic": "true",
								},
								{
									"branchId":           "branch-2-id",
									"branchMappingLogic": "false",
								},
							},
						}),
					},
				)
			}

			return httpmock.NewStringResponse(404, "Unknown operation"), nil
		})
}

func mockExpoForRequestUploadUrlTest(channelName string) {
	httpmock.RegisterResponder("POST", "https://api.expo.dev/graphql",
		func(req *http.Request) (*http.Response, error) {
			isFetchSelfExpoUsername := req.Header.Get("operationName") == "FetchExpoUserAccountInformations"
			isFetchExpoChannelMapping := req.Header.Get("operationName") == "FetchExpoChannelMapping"
			isFetchBranches := req.Header.Get("operationName") == "FetchExpoBranches"
			isCreateBranch := req.Header.Get("operationName") == "CreateBranch"
			if isFetchBranches {
				return MockExpoBranchesResponse([]map[string]interface{}{
					{
						"id":   "branch-1-id",
						"name": "branch-1",
					},
					{
						"id":   "branch-2-id",
						"name": "branch-2",
					},
					{
						"id":   "do-not-use",
						"name": "DO_NOT_USE",
					},
				})
			}
			if isCreateBranch {
				return httpmock.NewJsonResponse(http.StatusOK, map[string]interface{}{
					"data": map[string]interface{}{
						"updateBranch": map[string]interface{}{
							"createUpdateBranchForApp": map[string]interface{}{
								"id":   "created-branch-id",
								"name": "created-branch",
							},
						},
					},
				})
			}
			if isFetchSelfExpoUsername {
				authHeader := req.Header.Get("Authorization")
				if authHeader != "" {
					if authHeader == "Bearer expo_test_token" || authHeader == "Bearer EXPO_ACCESS_TOKEN" {
						return MockExpoAccountResponse(map[string]interface{}{
							"id":       "123",
							"username": "test_username",
							"email":    "test@example.com",
						})
					}
					if authHeader == "Bearer expo_alternative_token" {
						return MockExpoAccountResponse(map[string]interface{}{
							"id":       "1234",
							"username": "test_alternative_username",
							"email":    "test_alternative@example.com",
						})
					}
					if authHeader != "Bearer expo_test_token" {
						return httpmock.NewStringResponse(http.StatusUnauthorized, `{"error": "Unauthorized"}`), nil
					}
				}
				expoSession := req.Header.Get("expo-session")
				if expoSession != "" {
					if expoSession == "expo_test_session" {
						return MockExpoAccountResponse(map[string]interface{}{
							"id":       "123",
							"username": "test_username",
							"email":    "text@example.com",
						})
					}
					return httpmock.NewStringResponse(http.StatusUnauthorized, `{"error": "Unauthorized"}`), nil
				}
				return MockExpoAccountResponse(map[string]interface{}{
					"id":       "123",
					"username": "test_username",
					"email":    "test@example.com",
				})
			}

			if isFetchExpoChannelMapping {
				return MockExpoChannelMapping(
					[]map[string]interface{}{
						{
							"id":   "branch-1-id",
							"name": "branch-1",
						},
						{
							"id":   "branch-2-id",
							"name": "branch-2",
						},
						{
							"id":   "do-not-use",
							"name": "DO_NOT_USE",
						},
					},
					map[string]interface{}{
						"id":   channelName + "-id",
						"name": channelName,
						"branchMapping": StringifyBranchMapping(map[string]interface{}{
							"version": 0,
							"data": []map[string]interface{}{
								{
									"branchId":           "do-not-use",
									"branchMappingLogic": "true",
								},
							},
						}),
					},
				)
			}

			return httpmock.NewStringResponse(404, "Unknown operation"), nil
		})
}

// ComputeUploadRequestsInput mirrors what eoas posts for one platform: that
// platform's launch asset and assets, plus the config files, each stamped with
// its role.
func ComputeUploadRequestsInput(dirPath string, platform types.Platform) handlers.RequestUploadURLsRequest {
	metadataFile, err := os.Open(filepath.Join(dirPath, "metadata.json"))
	if err != nil {
		panic(err)
	}
	defer metadataFile.Close()
	var metadataObject types.MetadataObject
	if err := json.NewDecoder(metadataFile).Decode(&metadataObject); err != nil {
		panic(err)
	}
	item := func(relativePath, ext string, role services.FileRole) services.FileUploadItem {
		data, err := os.ReadFile(filepath.Join(dirPath, relativePath))
		if err != nil {
			panic(err)
		}
		sum, err := crypto.CreateHash(data, "sha256", "base64")
		if err != nil {
			panic(err)
		}
		file := services.FileUploadItem{
			Path: relativePath,
			Hash: crypto.GetBase64URLEncoding(sum),
			Ext:  ext,
			Role: role,
		}
		if role != services.FileRoleConfig {
			key, err := crypto.CreateHash(data, "md5", "hex")
			if err != nil {
				panic(err)
			}
			file.Key = key
		}
		return file
	}
	files := []services.FileUploadItem{
		item("metadata.json", "json", services.FileRoleConfig),
		item("expoConfig.json", "json", services.FileRoleConfig),
	}
	platformMetadata, err := metadataObject.FileMetadata.PlatformMetadata(platform)
	if err == nil && platformMetadata.Bundle != "" {
		files = append(files, item(platformMetadata.Bundle, "hbc", services.FileRoleLaunch))
		for _, asset := range platformMetadata.Assets {
			files = append(files, item(asset.Path, asset.Ext, services.FileRoleAsset))
		}
	}
	return handlers.RequestUploadURLsRequest{Files: files}
}

func ChangeModTime(filePath string, newTime time.Time) error {
	// Ouvre le fichier
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	err = os.Chtimes(filePath, newTime, newTime)
	if err != nil {
		return err
	}

	return nil
}

func ChangeModTimeRecursively(dir string, newTime time.Time) error {
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			err := ChangeModTime(path, newTime)
			if err != nil {
				return err
			}
		}
		return nil
	})

	return err
}

func SetValidConfiguration() {
	projectRoot, err := findProjectRoot()
	if err != nil {
		panic(err)
	}
	os.Setenv("BASE_URL", "http://localhost:3000")
	os.Setenv("LOCAL_BUCKET_BASE_PATH", filepath.Join(projectRoot, "/test/test-updates"))
	os.Setenv("JWT_SECRET", "test_jwt_secret")
	os.Setenv("PRIVATE_CLOUDFRONT_KEY_PATH", "")
	os.Setenv("CLOUDFRONT_DOMAIN", "")
	os.Setenv("CLOUDFRONT_KEY_PAIR_ID", "")
	os.Setenv("USE_DASHBOARD", "true")
	os.Setenv("ADMIN_EMAIL", "admin@xprem.dev")
	os.Setenv("ADMIN_PASSWORD", "admin")

	// v2 single-app flat-env config: a test-app-id entry pointing at the
	// existing test keys, reproducing the legacy local-file key storage
	// behavior the old env vars provided.
	os.Setenv("EXPO_APP_ID", "test-app-id")
	os.Setenv("EXPO_ACCESS_TOKEN", "EXPO_ACCESS_TOKEN")
	os.Setenv("KEYS_STORAGE_TYPE", "local")
	os.Setenv("PUBLIC_LOCAL_EXPO_KEY_PATH", filepath.Join(projectRoot, "/test/keys/public-key-test.pem"))
	os.Setenv("PRIVATE_LOCAL_EXPO_KEY_PATH", filepath.Join(projectRoot, "/test/keys/private-key-test.pem"))
	config.ResetAppsForTest()
	if err := config.LoadAppsFromFlatEnv(); err != nil {
		panic(err)
	}
}

// serveThroughRouter runs a CLI publish request through the real router. It is
// what these tests must use rather than calling a handler directly:
// authentication and the per-key access decision live in the routing table
// now (internal/router/routes_publish.go), so a handler called on its own
// would answer as if every credential were valid.
func serveThroughRouter(w *httptest.ResponseRecorder, r *http.Request) {
	infrastructure.NewRouter(testContainer()).ServeHTTP(w, r)
}
