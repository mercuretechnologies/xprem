package helpers

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetExpoAuthPrefersAccessTokenHeader(t *testing.T) {
	req := httptestRequest(http.Header{
		"Authorization":       []string{"Bearer dashboard-jwt"},
		"X-Expo-Access-Token": []string{"expo-token"},
		"expo-session":        []string{"session-secret"},
	})

	auth := GetExpoAuth(req)
	require.NotNil(t, auth.Token)
	assert.Equal(t, "expo-token", *auth.Token)
	assert.Nil(t, auth.SessionSecret)
}

func TestGetExpoAuthFallsBackToExpoSession(t *testing.T) {
	req := httptestRequest(http.Header{
		"Authorization": []string{"Bearer dashboard-jwt"},
		"expo-session":  []string{"session-secret"},
	})

	auth := GetExpoAuth(req)
	require.NotNil(t, auth.SessionSecret)
	assert.Equal(t, "session-secret", *auth.SessionSecret)
	assert.Nil(t, auth.Token)
}

func TestGetExpoAuthIgnoresDashboardAuthorization(t *testing.T) {
	req := httptestRequest(http.Header{
		"Authorization": []string{"Bearer dashboard-jwt"},
	})

	auth := GetExpoAuth(req)
	assert.Nil(t, auth.Token)
	assert.Nil(t, auth.SessionSecret)
}

func httptestRequest(headers http.Header) *http.Request {
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	for key, values := range headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	return req
}
