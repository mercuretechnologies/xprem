// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package licensing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultAPIBaseURL is the production license server.
const DefaultAPIBaseURL = "https://api.xprem.dev"

// Error codes answered by the license server on check, attach and validate.
const (
	CodeLicenseKeyNotFound    = "LICENSE_KEY_NOT_FOUND"
	CodeLicenseKeyAlreadyUsed = "LICENSE_KEY_ALREADY_USED"
	CodeLicenseKeyExpired     = "LICENSE_KEY_EXPIRED"
	CodeInvalidInstanceURL    = "INVALID_INSTANCE_URL"
	CodeSubscriptionInactive  = "SUBSCRIPTION_INACTIVE"
	CodeLicenseExpired        = "LICENSE_EXPIRED"
	CodeInvalidActivation     = "INVALID_ACTIVATION"

	// Minted locally, never sent by the server.
	CodeServerUnreachable = "LICENSE_SERVER_UNREACHABLE"
	CodeServerRejected    = "LICENSE_SERVER_REJECTED"
	CodePlanNotSupported  = "PLAN_NOT_SUPPORTED"
)

// ErrServerUnreachable wraps every transport-level failure, as opposed to a
// license decision.
var ErrServerUnreachable = errors.New("licensing: could not reach the license server")

// ErrServerRejected is a request the server refused to process (schema
// validation, rate limit), as opposed to a license decision.
var ErrServerRejected = errors.New("licensing: the license server rejected the request")

// DecisionError is a refusal decided by the license server.
type DecisionError struct {
	Code string
}

func (e *DecisionError) Error() string {
	return "licensing: the license server refused: " + e.Code
}

// Client calls the license server.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient falls back to DefaultAPIBaseURL when baseURL is empty.
func NewClient(baseURL string) *Client {
	if baseURL == "" {
		baseURL = DefaultAPIBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	return &Client{baseURL: baseURL, httpClient: &http.Client{Timeout: 15 * time.Second}}
}

// KeyParams is the request body of check and attach.
type KeyParams struct {
	InstanceId string `json:"instanceId"`
	LicenseKey string `json:"licenseKey"`
	BaseUrl    string `json:"baseUrl"`
	Version    string `json:"version"`
}

// ValidateParams is the request body of validate.
type ValidateParams struct {
	InstanceId       string `json:"instanceId"`
	ActivationSecret string `json:"activationSecret"`
	BaseUrl          string `json:"baseUrl"`
}

// CheckResult is the outcome of a no-side-effect key check.
type CheckResult struct {
	Valid     bool
	ErrorCode string
	License   *License
}

// Activation is a successful attach.
type Activation struct {
	ActivationSecret string
	License          License
}

type licenseInformationsPayload struct {
	Org struct {
		Name string `json:"name"`
	} `json:"org"`
	PlanCode     string `json:"planCode"`
	Subscription struct {
		StartAt   time.Time  `json:"startAt"`
		EndAt     *time.Time `json:"endAt"`
		RenewalAt *time.Time `json:"renewalAt"`
	} `json:"subscription"`
}

func (p *licenseInformationsPayload) toLicense() License {
	return License{
		OrgName:               p.Org.Name,
		PlanCode:              p.PlanCode,
		SubscriptionStartAt:   p.Subscription.StartAt,
		SubscriptionEndAt:     p.Subscription.EndAt,
		SubscriptionRenewalAt: p.Subscription.RenewalAt,
	}
}

// serverResponse is the union of the three endpoints' bodies.
type serverResponse struct {
	Valid               *bool                       `json:"valid"`
	IsActive            *bool                       `json:"isActive"`
	ErrorCode           string                      `json:"errorCode"`
	ActivationSecret    string                      `json:"activationSecret"`
	LicenseInformations *licenseInformationsPayload `json:"licenseInformations"`
}

func (c *Client) post(ctx context.Context, path string, payload any) (*serverResponse, int, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal license request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("build license request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %w", ErrServerUnreachable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusBadRequest {
		// Drained so the keep-alive connection can be reused.
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, resp.StatusCode, fmt.Errorf("%w: unexpected status %s", ErrServerUnreachable, resp.Status)
	}
	var decoded serverResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("%w: undecodable response: %v", ErrServerUnreachable, err)
	}
	return &decoded, resp.StatusCode, nil
}

func decision(resp *serverResponse) error {
	if resp.ErrorCode != "" {
		return &DecisionError{Code: resp.ErrorCode}
	}
	return fmt.Errorf("%w: no error code in the response", ErrServerRejected)
}

// Check asks whether a key is usable, without consuming it.
func (c *Client) Check(ctx context.Context, params KeyParams) (CheckResult, error) {
	resp, status, err := c.post(ctx, "/v1/licenses/check", params)
	if err != nil {
		return CheckResult{}, err
	}
	if status != http.StatusOK {
		return CheckResult{}, decision(resp)
	}
	if resp.Valid != nil && *resp.Valid && resp.LicenseInformations != nil {
		license := resp.LicenseInformations.toLicense()
		return CheckResult{Valid: true, License: &license}, nil
	}
	if resp.ErrorCode == "" {
		return CheckResult{}, fmt.Errorf("%w: unintelligible check response", ErrServerUnreachable)
	}
	return CheckResult{ErrorCode: resp.ErrorCode}, nil
}

// Attach consumes the key and binds it to this instance.
func (c *Client) Attach(ctx context.Context, params KeyParams) (*Activation, error) {
	resp, status, err := c.post(ctx, "/v1/licenses/attach", params)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, decision(resp)
	}
	if resp.IsActive == nil || !*resp.IsActive || resp.ActivationSecret == "" || resp.LicenseInformations == nil {
		return nil, fmt.Errorf("%w: unintelligible attach response", ErrServerUnreachable)
	}
	return &Activation{
		ActivationSecret: resp.ActivationSecret,
		License:          resp.LicenseInformations.toLicense(),
	}, nil
}

// Validate re-checks an activation and returns the fresh license descriptor.
func (c *Client) Validate(ctx context.Context, params ValidateParams) (*License, error) {
	resp, status, err := c.post(ctx, "/v1/licenses/validate", params)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, decision(resp)
	}
	if resp.IsActive == nil || !*resp.IsActive || resp.LicenseInformations == nil {
		return nil, fmt.Errorf("%w: unintelligible validate response", ErrServerUnreachable)
	}
	license := resp.LicenseInformations.toLicense()
	return &license, nil
}
