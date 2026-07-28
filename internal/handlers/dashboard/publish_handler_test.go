package handlers

import (
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
