package handlers

import (
	"errors"
	"expo-open-ota/internal/handlers"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
)

// The two publish routes reject a malformed request before they reach any
// service, so a handler with none wired is enough to pin the gates down: every
// case below must be refused, and a leak would panic on the nil service rather
// than pass silently.
func publishRequest(t *testing.T, route string, handler http.HandlerFunc, branch, runtimeVersion, body string) *httptest.ResponseRecorder {
	t.Helper()
	router := mux.NewRouter()
	router.HandleFunc("/apps/{APP_ID}/branch/{BRANCH}/runtimeVersion/{RUNTIME_VERSION}/"+route, handler)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(
		http.MethodPost,
		"/apps/app-1/branch/"+branch+"/runtimeVersion/"+runtimeVersion+"/"+route,
		strings.NewReader(body),
	))
	return recorder
}

// The protected-branch guard runs before the body is even read, so a refusal
// costs nothing and cannot be worked around by the payload. Both publish
// routes must consult it: a route that forgot to is a branch anyone with
// update:publish can rewrite.
func TestPublishRoutesHonorTheProtectedBranchGuard(t *testing.T) {
	routes := map[string]func(*UpdateHandler) http.HandlerFunc{
		"rollback":  func(h *UpdateHandler) http.HandlerFunc { return h.CreateRollbackHandler },
		"republish": func(h *UpdateHandler) http.HandlerFunc { return h.RepublishUpdateHandler },
	}
	for route, handlerOf := range routes {
		t.Run(route+" denied", func(t *testing.T) {
			handler := NewUpdateHandler(nil, nil)
			var guardedBranch string
			handler.SetProtectedBranchGuard(func(_ *http.Request, _ string, branchName string) error {
				guardedBranch = branchName
				return fmt.Errorf("%w: production is a protected branch", handlers.ErrAccessDenied)
			})
			recorder := publishRequest(t, route, handlerOf(handler), "production", "1", `{"message":"why","updateId":"1"}`)
			require.Equal(t, http.StatusForbidden, recorder.Code)
			require.Contains(t, recorder.Body.String(), "protected branch")
			require.Equal(t, "production", guardedBranch)
		})
		t.Run(route+" undecidable", func(t *testing.T) {
			// A guard that could not decide is a 500, never a pass: the branch
			// it failed to classify may be the protected one.
			handler := NewUpdateHandler(nil, nil)
			handler.SetProtectedBranchGuard(func(_ *http.Request, _ string, _ string) error {
				return errors.New("database is down")
			})
			recorder := publishRequest(t, route, handlerOf(handler), "production", "1", `{"message":"why","updateId":"1"}`)
			require.Equal(t, http.StatusInternalServerError, recorder.Code)
		})
	}
}

func TestCreateRollbackHandlerRejectsBadRequests(t *testing.T) {
	handler := NewUpdateHandler(nil, nil)
	cases := map[string]struct{ branch, runtimeVersion, body string }{
		"control char in branch":  {"ma%01in", "1", `{"message":"why"}`},
		"overlong branch":         {strings.Repeat("a", 129), "1", `{"message":"why"}`},
		"control char in runtime": {"main", "1%01x", `{"message":"why"}`},
		"malformed json":          {"main", "1", `{`},
		"unknown platform":        {"main", "1", `{"platform":"web","message":"why"}`},
		"no message":              {"main", "1", `{"platform":"ios"}`},
		"blank message":           {"main", "1", `{"platform":"ios","message":"   "}`},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			recorder := publishRequest(t, "rollback", handler.CreateRollbackHandler, testCase.branch, testCase.runtimeVersion, testCase.body)
			require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
		})
	}
}

func TestRepublishUpdateHandlerRejectsBadRequests(t *testing.T) {
	handler := NewUpdateHandler(nil, nil)
	cases := map[string]struct{ branch, runtimeVersion, body string }{
		"control char in branch": {"ma%01in", "1", `{"updateId":"1"}`},
		"malformed json":         {"main", "1", `{`},
		"no target":              {"main", "1", `{}`},
		"both targets":           {"main", "1", `{"updateId":"1","publishGroup":"3f2b0f5e-0f24-4b1e-9c53-2a3b1f0f9d11"}`},
		"group is not a uid":     {"main", "1", `{"publishGroup":"not-a-uuid"}`},
		"update id is a name":    {"main", "1", `{"updateId":"latest"}`},
		"negative update id":     {"main", "1", `{"updateId":"-1"}`},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			recorder := publishRequest(t, "republish", handler.RepublishUpdateHandler, testCase.branch, testCase.runtimeVersion, testCase.body)
			require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
		})
	}
}
