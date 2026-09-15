// Package appstoreconnect is a minimal App Store Connect API client for
// signing certificates and devices.
package appstoreconnect

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	DefaultBaseURL   = "https://api.appstoreconnect.apple.com/v1"
	tokenLifetime    = 10 * time.Minute
	maxResponseBytes = 10 << 20
)

// ErrUnavailable reports that Apple could not be reached or failed on its side.
var ErrUnavailable = errors.New("App Store Connect is unavailable")

// APIError is a client error answered by App Store Connect.
type APIError struct {
	Status int
	Detail string
}

// Error includes the Apple status and detail for server-side diagnostics.
func (e *APIError) Error() string {
	return fmt.Sprintf("App Store Connect answered %d: %s", e.Status, e.Detail)
}

// Client calls App Store Connect with a team API key.
type Client struct {
	baseURL    string
	httpClient *http.Client
	keyID      string
	issuerID   string
	privateKey *ecdsa.PrivateKey
}

// Certificate is a signing certificate registered in the team.
type Certificate struct {
	ID  string
	DER []byte
}

// Device is a device registered in the team; AddedDate is Apple's raw timestamp.
type Device struct {
	ID          string
	Name        string
	UDID        string
	Model       string
	DeviceClass string
	Status      string
	AddedDate   string
}

// NewClient creates a team API client with a one-minute request timeout.
func NewClient(baseURL, keyID, issuerID string, privateKey *ecdsa.PrivateKey) *Client {
	return &Client{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: time.Minute},
		keyID:      keyID,
		issuerID:   issuerID,
		privateKey: privateKey,
	}
}

// ParsePrivateKey reads the PEM PKCS#8 P-256 key of a .p8 file.
func ParsePrivateKey(pemText string) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemText))
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, errors.New("not a PEM private key")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	ecKey, ok := key.(*ecdsa.PrivateKey)
	if !ok || ecKey.Curve != elliptic.P256() {
		return nil, errors.New("not an EC P-256 private key")
	}
	return ecKey, nil
}

type resource[T any] struct {
	ID         string `json:"id"`
	Attributes T      `json:"attributes"`
}

type listDocument[T any] struct {
	Data  []resource[T] `json:"data"`
	Links struct {
		Next string `json:"next"`
	} `json:"links"`
}

type document[T any] struct {
	Data resource[T] `json:"data"`
}

type certificateAttributes struct {
	CertificateContent []byte `json:"certificateContent"`
}

type deviceAttributes struct {
	Name        string `json:"name"`
	UDID        string `json:"udid"`
	Model       string `json:"model"`
	Platform    string `json:"platform"`
	DeviceClass string `json:"deviceClass"`
	Status      string `json:"status"`
	AddedDate   string `json:"addedDate"`
}

// device combines the Apple resource ID with its device attributes.
func (a deviceAttributes) device(id string) Device {
	return Device{ID: id, Name: a.Name, UDID: a.UDID, Model: a.Model, DeviceClass: a.DeviceClass, Status: a.Status, AddedDate: a.AddedDate}
}

// VerifyAccess makes the cheapest authenticated call.
func (c *Client) VerifyAccess(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/bundleIds?limit=1", nil, nil)
}

// ListDistributionCertificates downloads every page of the team's distribution certificates.
func (c *Client) ListDistributionCertificates(ctx context.Context) ([]Certificate, error) {
	query := url.Values{"filter[certificateType]": {"DISTRIBUTION,IOS_DISTRIBUTION"}, "limit": {"200"}}
	resources, err := listResources[certificateAttributes](ctx, c, "/certificates?"+query.Encode())
	if err != nil {
		return nil, err
	}
	certificates := make([]Certificate, len(resources))
	for i, certificate := range resources {
		certificates[i] = Certificate{ID: certificate.ID, DER: certificate.Attributes.CertificateContent}
	}
	return certificates, nil
}

// ListIOSDevices returns the team's devices that are not Macs, enabled or not.
func (c *Client) ListIOSDevices(ctx context.Context) ([]Device, error) {
	query := url.Values{"limit": {"200"}}
	resources, err := listResources[deviceAttributes](ctx, c, "/devices?"+query.Encode())
	if err != nil {
		return nil, err
	}
	devices := []Device{}
	for _, device := range resources {
		if device.Attributes.Platform != "MAC_OS" && device.Attributes.DeviceClass != "MAC" {
			devices = append(devices, device.Attributes.device(device.ID))
		}
	}
	return devices, nil
}

// listResources follows Apple's next links and fails instead of returning a partial list.
func listResources[T any](ctx context.Context, client *Client, path string) ([]resource[T], error) {
	pageURL, err := url.Parse(client.baseURL + path)
	if err != nil {
		return nil, err
	}
	var resources []resource[T]
	seen := map[string]bool{}
	for {
		if seen[pageURL.String()] {
			return nil, fmt.Errorf("%w: repeated pagination link", ErrUnavailable)
		}
		seen[pageURL.String()] = true
		var page listDocument[T]
		if err := client.do(ctx, http.MethodGet, pageURL.String(), nil, &page); err != nil {
			return nil, err
		}
		resources = append(resources, page.Data...)
		if page.Links.Next == "" {
			return resources, nil
		}
		next, err := url.Parse(page.Links.Next)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid pagination link", ErrUnavailable)
		}
		pageURL = pageURL.ResolveReference(next)
	}
}

// FindDevice returns the Apple id of the device with this UDID, or "" when it is not registered.
func (c *Client) FindDevice(ctx context.Context, udid string) (string, error) {
	query := url.Values{"filter[udid]": {udid}, "limit": {"200"}}
	var list listDocument[deviceAttributes]
	if err := c.do(ctx, http.MethodGet, "/devices?"+query.Encode(), nil, &list); err != nil {
		return "", err
	}
	for _, device := range list.Data {
		if strings.EqualFold(device.Attributes.UDID, udid) {
			return device.ID, nil
		}
	}
	return "", nil
}

// RegisterIOSDevice adds an iOS device to the team and returns its Apple id.
func (c *Client) RegisterIOSDevice(ctx context.Context, name, udid string) (string, error) {
	body := map[string]any{"data": map[string]any{
		"type":       "devices",
		"attributes": map[string]string{"name": name, "platform": "IOS", "udid": udid},
	}}
	var created document[deviceAttributes]
	if err := c.do(ctx, http.MethodPost, "/devices", body, &created); err != nil {
		return "", err
	}
	return created.Data.ID, nil
}

// UpdateDeviceStatus enables or disables a device of the team and returns it.
func (c *Client) UpdateDeviceStatus(ctx context.Context, id string, status string) (Device, error) {
	body := map[string]any{"data": map[string]any{
		"type":       "devices",
		"id":         id,
		"attributes": map[string]string{"status": status},
	}}
	var updated document[deviceAttributes]
	if err := c.do(ctx, http.MethodPatch, "/devices/"+url.PathEscape(id), body, &updated); err != nil {
		return Device{}, err
	}
	return updated.Data.Attributes.device(updated.Data.ID), nil
}

// token signs a short-lived ES256 bearer token for App Store Connect.
func (c *Client) token() (string, error) {
	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iss": c.issuerID,
		"iat": now.Unix(),
		"exp": now.Add(tokenLifetime).Unix(),
		"aud": "appstoreconnect-v1",
	})
	token.Header["kid"] = c.keyID
	return token.SignedString(c.privateKey)
}

// do authenticates one request within the configured API and decodes its response.
func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	base, err := url.Parse(c.baseURL)
	if err != nil {
		return err
	}
	requestURL, err := url.Parse(path)
	if err != nil {
		return err
	}
	if !requestURL.IsAbs() {
		requestURL, err = url.Parse(c.baseURL + path)
		if err != nil {
			return err
		}
	}
	// A next link must never send the team's bearer token to another origin.
	if requestURL.Scheme != base.Scheme || requestURL.Host != base.Host || requestURL.User != nil ||
		!strings.HasPrefix(requestURL.Path, strings.TrimRight(base.Path, "/")+"/") {
		return fmt.Errorf("%w: pagination link outside the API", ErrUnavailable)
	}
	token, err := c.token()
	if err != nil {
		return fmt.Errorf("sign app store connect token: %w", err)
	}
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, requestURL.String(), payload)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if response.StatusCode >= 500 || response.StatusCode == http.StatusTooManyRequests {
		return fmt.Errorf("%w: status %d", ErrUnavailable, response.StatusCode)
	}
	if response.StatusCode >= 400 {
		return apiError(response.StatusCode, data)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%w: unexpected response to %s %s", ErrUnavailable, method, request.URL.Path)
	}
	return nil
}

// apiError extracts the first Apple error detail, falling back to the HTTP status.
func apiError(status int, data []byte) *APIError {
	var answer struct {
		Errors []struct {
			Title  string `json:"title"`
			Detail string `json:"detail"`
		} `json:"errors"`
	}
	apiErr := &APIError{Status: status, Detail: http.StatusText(status)}
	if json.Unmarshal(data, &answer) == nil && len(answer.Errors) > 0 {
		first := answer.Errors[0]
		if first.Detail != "" {
			apiErr.Detail = first.Detail
		} else if first.Title != "" {
			apiErr.Detail = first.Title
		}
	}
	return apiErr
}
