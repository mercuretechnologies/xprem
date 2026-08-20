// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package licensing

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLicenseResponsesNeverExposeKeyOrSecret(t *testing.T) {
	repo := repoWithAcme()
	fake := &fakeLicenseServer{check: checkAnswersValidAcme}
	service := newTestService(t, repo, fake.client(t))
	handler := NewLicenseHandler(service)

	get := httptest.NewRecorder()
	handler.GetLicenseHandler(get, httptest.NewRequest(http.MethodGet, "/license", nil))
	require.Equal(t, http.StatusOK, get.Code)

	check := httptest.NewRecorder()
	checkRequest := httptest.NewRequest(http.MethodPost, "/license/check", strings.NewReader(`{"key":"XPREM-KEY"}`))
	handler.CheckLicenseHandler(check, checkRequest)
	require.Equal(t, http.StatusOK, check.Code)

	for name, body := range map[string]string{"get": get.Body.String(), "check": check.Body.String()} {
		assert.NotContains(t, body, repo.stored.Key, name)
		assert.NotContains(t, body, repo.secret, name)
	}
}
