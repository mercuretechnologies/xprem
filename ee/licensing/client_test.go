// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package licensing

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const acmeInformationsJSON = `{
	"org": {"name": "Acme Corp"},
	"planCode": "enterprise",
	"subscription": {
		"startAt": "2026-01-01T00:00:00.000Z",
		"endAt": null,
		"renewalAt": "2027-01-01T00:00:00.000Z"
	}
}`

// fakeLicenseServer stubs the three license server endpoints; nil handlers
// answer 404, which the client reports as unreachable.
type fakeLicenseServer struct {
	check    http.HandlerFunc
	attach   http.HandlerFunc
	validate http.HandlerFunc
}

func (f *fakeLicenseServer) client(t *testing.T) *Client {
	t.Helper()
	router := http.NewServeMux()
	if f.check != nil {
		router.HandleFunc("/v1/licenses/check", f.check)
	}
	if f.attach != nil {
		router.HandleFunc("/v1/licenses/attach", f.attach)
	}
	if f.validate != nil {
		router.HandleFunc("/v1/licenses/validate", f.validate)
	}
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return NewClient(server.URL)
}

func writeJSONBody(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write([]byte(body))
}

func testKeyParams(key string) KeyParams {
	return KeyParams{
		InstanceId: "instance-1",
		LicenseKey: key,
		BaseUrl:    "https://updates.example.com",
		Version:    "1.2.3",
	}
}

func TestClientCheckValidKey(t *testing.T) {
	fake := &fakeLicenseServer{
		check: func(w http.ResponseWriter, r *http.Request) {
			var body map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			assert.Equal(t, "instance-1", body["instanceId"])
			assert.Equal(t, "XPREM-KEY", body["licenseKey"])
			assert.Equal(t, "https://updates.example.com", body["baseUrl"])
			assert.Equal(t, "1.2.3", body["version"])
			writeJSONBody(w, http.StatusOK, `{"valid": true, "licenseInformations": `+acmeInformationsJSON+`}`)
		},
	}
	result, err := fake.client(t).Check(context.Background(), testKeyParams("XPREM-KEY"))
	require.NoError(t, err)
	assert.True(t, result.Valid)
	require.NotNil(t, result.License)
	assert.Equal(t, "Acme Corp", result.License.OrgName)
	assert.Equal(t, "enterprise", result.License.PlanCode)
	assert.Nil(t, result.License.SubscriptionEndAt)
	require.NotNil(t, result.License.SubscriptionRenewalAt)
}

func TestClientCheckInvalidKeyIsNotAnError(t *testing.T) {
	fake := &fakeLicenseServer{
		check: func(w http.ResponseWriter, r *http.Request) {
			writeJSONBody(w, http.StatusOK, `{"valid": false, "errorCode": "LICENSE_KEY_NOT_FOUND"}`)
		},
	}
	result, err := fake.client(t).Check(context.Background(), testKeyParams("nope"))
	require.NoError(t, err)
	assert.False(t, result.Valid)
	assert.Equal(t, CodeLicenseKeyNotFound, result.ErrorCode)
	assert.Nil(t, result.License)
}

func TestClientAttachSuccess(t *testing.T) {
	fake := &fakeLicenseServer{
		attach: func(w http.ResponseWriter, r *http.Request) {
			writeJSONBody(w, http.StatusOK, `{"isActive": true, "activationSecret": "secret-42", "licenseInformations": `+acmeInformationsJSON+`}`)
		},
	}
	activation, err := fake.client(t).Attach(context.Background(), testKeyParams("XPREM-KEY"))
	require.NoError(t, err)
	assert.Equal(t, "secret-42", activation.ActivationSecret)
	assert.Equal(t, "Acme Corp", activation.License.OrgName)
}

func TestClientAttachRefusalIsADecision(t *testing.T) {
	fake := &fakeLicenseServer{
		attach: func(w http.ResponseWriter, r *http.Request) {
			writeJSONBody(w, http.StatusBadRequest, `{"valid": false, "errorCode": "LICENSE_KEY_ALREADY_USED"}`)
		},
	}
	_, err := fake.client(t).Attach(context.Background(), testKeyParams("XPREM-KEY"))
	var refusal *DecisionError
	require.ErrorAs(t, err, &refusal)
	assert.Equal(t, CodeLicenseKeyAlreadyUsed, refusal.Code)
}

func TestClientAttachMalformedBodyRejectionIsNotADecision(t *testing.T) {
	fake := &fakeLicenseServer{
		attach: func(w http.ResponseWriter, r *http.Request) {
			writeJSONBody(w, http.StatusBadRequest, `{"statusCode": 400, "error": "Bad Request", "message": "Request validation failed"}`)
		},
	}
	_, err := fake.client(t).Attach(context.Background(), testKeyParams("XPREM-KEY"))
	require.ErrorIs(t, err, ErrServerRejected)
	var refusal *DecisionError
	assert.False(t, errors.As(err, &refusal))
}

func TestClientTrimsTrailingSlashFromBaseURL(t *testing.T) {
	router := http.NewServeMux()
	router.HandleFunc("/v1/licenses/check", func(w http.ResponseWriter, r *http.Request) {
		writeJSONBody(w, http.StatusOK, `{"valid": true, "licenseInformations": `+acmeInformationsJSON+`}`)
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	result, err := NewClient(server.URL+"/").Check(context.Background(), testKeyParams("XPREM-KEY"))
	require.NoError(t, err)
	assert.True(t, result.Valid)
}

func TestClientValidateSuccess(t *testing.T) {
	fake := &fakeLicenseServer{
		validate: func(w http.ResponseWriter, r *http.Request) {
			var body map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			assert.Equal(t, "secret-42", body["activationSecret"])
			assert.NotContains(t, body, "licenseKey")
			writeJSONBody(w, http.StatusOK, `{"isActive": true, "licenseInformations": `+acmeInformationsJSON+`}`)
		},
	}
	license, err := fake.client(t).Validate(context.Background(), ValidateParams{
		InstanceId:       "instance-1",
		ActivationSecret: "secret-42",
		BaseUrl:          "https://updates.example.com",
	})
	require.NoError(t, err)
	assert.Equal(t, "Acme Corp", license.OrgName)
}

func TestClientValidateRefusalIsADecision(t *testing.T) {
	fake := &fakeLicenseServer{
		validate: func(w http.ResponseWriter, r *http.Request) {
			writeJSONBody(w, http.StatusBadRequest, `{"valid": false, "errorCode": "INVALID_ACTIVATION"}`)
		},
	}
	_, err := fake.client(t).Validate(context.Background(), ValidateParams{})
	var refusal *DecisionError
	require.ErrorAs(t, err, &refusal)
	assert.Equal(t, CodeInvalidActivation, refusal.Code)
}

func TestClientUnexpectedStatusIsUnreachable(t *testing.T) {
	fake := &fakeLicenseServer{
		validate: func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		},
	}
	_, err := fake.client(t).Validate(context.Background(), ValidateParams{})
	require.ErrorIs(t, err, ErrServerUnreachable)
}

func TestClientConnectionFailureIsUnreachable(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	server.Close()
	_, err := NewClient(server.URL).Check(context.Background(), testKeyParams("XPREM-KEY"))
	require.ErrorIs(t, err, ErrServerUnreachable)
}
