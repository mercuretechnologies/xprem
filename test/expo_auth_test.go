package test

import (
	"net/http"
	"path/filepath"
	"testing"

	"xprem/internal/types"

	"github.com/jarcoal/httpmock"
	"github.com/stretchr/testify/assert"
)

// mockExpoMe answers the Expo `me` lookup per bearer token; any token missing
// from the table gets `me: null`, which is how Expo answers an unknown token.
func mockExpoMe(byBearer map[string]map[string]interface{}) {
	httpmock.RegisterResponder("POST", "https://api.expo.dev/graphql",
		func(req *http.Request) (*http.Response, error) {
			if req.Header.Get("operationName") != "FetchExpoUserAccountInformations" {
				return httpmock.NewStringResponse(http.StatusNotFound, "Unknown operation"), nil
			}
			if me, ok := byBearer[req.Header.Get("Authorization")]; ok {
				return MockExpoAccountResponse(me)
			}
			return httpmock.NewJsonResponse(http.StatusOK, map[string]interface{}{
				"data": map[string]interface{}{"me": nil},
			})
		})
}

func requestUploadUrlWithBearer(t *testing.T, bearer string) int {
	t.Helper()
	projectRoot, err := findProjectRoot()
	if err != nil {
		t.Fatalf("Error finding project root: %v", err)
	}
	sampleUpdatePath := filepath.Join(projectRoot, "/test/sample-exports/bundles-layout")
	w, _, _, r := createUploadRequest(t, projectRoot, "DO_NOT_USE", "1", sampleUpdatePath, "Authorization", bearer, types.PlatformIOS)
	serveThroughRouter(w, r)
	return w.Code
}

// When the server's own token cannot be resolved to a user, its username is "".
// A caller whose token Expo also answers with `me: null` must not be read as
// "the same user": two empty usernames are not an identity.
func TestRequestUploadUrlRejectsNullUserWhenServerTokenResolvesToNoUser(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockExpoMe(map[string]map[string]interface{}{})

	assert.Equal(t, http.StatusUnauthorized, requestUploadUrlWithBearer(t, "Bearer anything_at_all"))
}

// A server token that resolves to a user must still refuse a caller token Expo
// does not recognize.
func TestRequestUploadUrlRejectsNullUserForUnknownCallerToken(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockExpoMe(map[string]map[string]interface{}{
		"Bearer EXPO_ACCESS_TOKEN": {"id": "123", "username": "test_username"},
	})

	assert.Equal(t, http.StatusUnauthorized, requestUploadUrlWithBearer(t, "Bearer unknown_token"))
}

// An account with no username must not pass even when the server's lookup
// failed, which also yields "".
func TestRequestUploadUrlRejectsEmptyUsername(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockExpoMe(map[string]map[string]interface{}{
		"Bearer empty_user_token": {"id": "9", "username": ""},
	})

	assert.Equal(t, http.StatusUnauthorized, requestUploadUrlWithBearer(t, "Bearer empty_user_token"))
}

// GraphQL-level errors carried on an HTTP 200 are a refusal, not an account.
func TestRequestUploadUrlRejectsGraphQLErrors(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	httpmock.RegisterResponder("POST", "https://api.expo.dev/graphql",
		func(req *http.Request) (*http.Response, error) {
			return httpmock.NewJsonResponse(http.StatusOK, map[string]interface{}{
				"data":   map[string]interface{}{"me": nil},
				"errors": []map[string]interface{}{{"message": "Authentication failed"}},
			})
		})

	assert.Equal(t, http.StatusUnauthorized, requestUploadUrlWithBearer(t, "Bearer anything_at_all"))
}

// A token of a different Expo user is refused when the server token resolves.
func TestRequestUploadUrlRejectsDifferentExpoUser(t *testing.T) {
	teardown := setup(t)
	defer teardown()
	mockExpoMe(map[string]map[string]interface{}{
		"Bearer EXPO_ACCESS_TOKEN": {"id": "123", "username": "test_username"},
		"Bearer other_org_token":   {"id": "456", "username": "someone_else"},
	})

	assert.Equal(t, http.StatusUnauthorized, requestUploadUrlWithBearer(t, "Bearer other_org_token"))
}
