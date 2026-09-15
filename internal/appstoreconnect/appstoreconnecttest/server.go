// Package appstoreconnecttest runs an in-memory App Store Connect API that
// verifies request tokens, for tests.
package appstoreconnecttest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	KeyID    = "ABC123DEFG"
	IssuerID = "57246542-96fe-1a63-e053-0824d011072a"
	TeamID   = "ABCDE12345"
)

// Server is a fake App Store Connect team reachable at BaseURL.
type Server struct {
	BaseURL       string
	PrivateKeyPEM string

	mu sync.Mutex
	// Status, when set, answers every authenticated request with that status.
	Status int
	// DeviceLimitReached makes device registration answer 409.
	DeviceLimitReached bool
	// DeviceConflictDetail injects a registration conflict while lookups still return no device.
	DeviceConflictDetail string
	Devices              []Device
	requests             []string
	certificates         map[string][]byte
	certificatesCreated  int
	publicKey            *ecdsa.PublicKey
}

// Device is a registered device with the attributes Apple lists.
type Device struct {
	ID, Name, UDID, Model, Platform, DeviceClass, Status, AddedDate string
}

func New(t testing.TB) *Server {
	t.Helper()
	apiKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(apiKey)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{
		PrivateKeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})),
		certificates:  map[string][]byte{},
		publicKey:     &apiKey.PublicKey,
	}
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)
	server.BaseURL = httpServer.URL + "/v1"
	return server
}

// RegisterCertificate adds an existing certificate to the team's distribution certificates.
func (s *Server) RegisterCertificate(der []byte) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addCertificate(der)
}

// RequestCount counts the requests matching "METHOD /path".
func (s *Server) RequestCount(request string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, received := range s.requests {
		if received == request {
			count++
		}
	}
	return count
}

func (s *Server) addCertificate(der []byte) string {
	s.certificatesCreated++
	id := fmt.Sprintf("CERT%d", s.certificatesCreated)
	s.certificates[id] = der
	return id
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, r.Method+" "+r.URL.Path)
	if err := s.authorize(r); err != nil {
		writeError(w, http.StatusUnauthorized, "NOT_AUTHORIZED", err.Error())
		return
	}
	if s.Status != 0 {
		writeError(w, s.Status, "FAILURE", "injected failure")
		return
	}
	switch r.Method + " " + r.URL.Path {
	case "GET /v1/bundleIds":
		writeJSON(w, http.StatusOK, map[string]any{"data": []any{}})
	case "GET /v1/certificates":
		s.listCertificates(w, r)
	case "GET /v1/devices":
		s.listDevices(w, r)
	case "POST /v1/devices":
		s.createDevice(w, r)
	default:
		if id, ok := strings.CutPrefix(r.URL.Path, "/v1/devices/"); ok && r.Method == http.MethodPatch {
			s.updateDevice(w, r, id)
			return
		}
		writeError(w, http.StatusNotFound, "NOT_FOUND", "unknown resource")
	}
}

func (s *Server) authorize(r *http.Request) error {
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return fmt.Errorf("missing bearer token")
	}
	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(raw, claims, func(token *jwt.Token) (any, error) {
		return s.publicKey, nil
	}, jwt.WithValidMethods([]string{"ES256"}), jwt.WithAudience("appstoreconnect-v1"), jwt.WithIssuer(IssuerID), jwt.WithIssuedAt(), jwt.WithExpirationRequired())
	if err != nil {
		return err
	}
	if token.Header["kid"] != KeyID || token.Header["typ"] != "JWT" {
		return fmt.Errorf("unexpected token header %v", token.Header)
	}
	issuedAt, _ := claims.GetIssuedAt()
	expiresAt, _ := claims.GetExpirationTime()
	if issuedAt == nil || expiresAt.Sub(issuedAt.Time) > 20*time.Minute {
		return fmt.Errorf("token lifetime exceeds 20 minutes")
	}
	return nil
}

// RegisteredDevices returns the devices of the fake team.
func (s *Server) RegisteredDevices() []Device {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Device(nil), s.Devices...)
}

func (s *Server) listDevices(w http.ResponseWriter, r *http.Request) {
	status, udid := r.URL.Query().Get("filter[status]"), r.URL.Query().Get("filter[udid]")
	data := []map[string]any{}
	for _, device := range s.Devices {
		if (status == "" || device.Status == status) && (udid == "" || device.UDID == udid) {
			data = append(data, map[string]any{"type": "devices", "id": device.ID, "attributes": map[string]string{
				"name":        device.Name,
				"udid":        device.UDID,
				"model":       device.Model,
				"platform":    device.Platform,
				"deviceClass": device.DeviceClass,
				"status":      device.Status,
				"addedDate":   device.AddedDate,
			}})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

func (s *Server) createDevice(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Data struct {
			Attributes struct {
				Name     string `json:"name"`
				Platform string `json:"platform"`
				UDID     string `json:"udid"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "PARAMETER_ERROR", err.Error())
		return
	}
	attributes := body.Data.Attributes
	if s.DeviceConflictDetail != "" {
		writeError(w, http.StatusConflict, "ENTITY_ERROR.ATTRIBUTE.INVALID", s.DeviceConflictDetail)
		return
	}
	if attributes.Name == "" || len([]rune(attributes.Name)) > 50 || attributes.Platform != "IOS" || attributes.UDID == "" {
		writeError(w, http.StatusConflict, "ENTITY_ERROR.ATTRIBUTE.INVALID", "invalid device attributes")
		return
	}
	for _, device := range s.Devices {
		if device.UDID == attributes.UDID {
			writeError(w, http.StatusConflict, "ENTITY_ERROR.ATTRIBUTE.INVALID", "A device with number '"+attributes.UDID+"' already exists on this team.")
			return
		}
	}
	if s.DeviceLimitReached {
		writeError(w, http.StatusConflict, "ENTITY_ERROR.ATTRIBUTE.INVALID", "There are no current ios devices on this team matching the provided device IDs. You have reached the maximum number of registered iPhone devices.")
		return
	}
	device := Device{
		ID:          fmt.Sprintf("DEVICE%d", len(s.Devices)+1),
		Name:        attributes.Name,
		UDID:        attributes.UDID,
		Platform:    "IOS",
		DeviceClass: "IPHONE",
		Status:      "ENABLED",
		AddedDate:   time.Now().UTC().Format("2006-01-02T15:04:05.000-0700"),
	}
	s.Devices = append(s.Devices, device)
	writeJSON(w, http.StatusCreated, map[string]any{"data": map[string]any{"type": "devices", "id": device.ID, "attributes": attributes}})
}

func (s *Server) updateDevice(w http.ResponseWriter, r *http.Request, id string) {
	var body struct {
		Data struct {
			Type       string `json:"type"`
			ID         string `json:"id"`
			Attributes struct {
				Status string `json:"status"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Data.Type != "devices" || body.Data.ID != id {
		writeError(w, http.StatusConflict, "ENTITY_ERROR", "invalid device update")
		return
	}
	status := body.Data.Attributes.Status
	if status != "ENABLED" && status != "DISABLED" {
		writeError(w, http.StatusConflict, "ENTITY_ERROR.ATTRIBUTE.INVALID", "invalid status")
		return
	}
	for i, device := range s.Devices {
		if device.ID == id {
			s.Devices[i].Status = status
			writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"type": "devices", "id": id, "attributes": map[string]string{
				"name": device.Name, "udid": device.UDID, "platform": device.Platform, "deviceClass": device.DeviceClass, "status": status,
			}}})
			return
		}
	}
	writeError(w, http.StatusNotFound, "NOT_FOUND", "There is no resource of type 'devices' with id '"+id+"'")
}

func (s *Server) listCertificates(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("filter[certificateType]") != "DISTRIBUTION,IOS_DISTRIBUTION" {
		writeError(w, http.StatusBadRequest, "PARAMETER_ERROR", "unexpected certificate filter")
		return
	}
	data := []map[string]any{}
	for id, der := range s.certificates {
		data = append(data, map[string]any{"type": "certificates", "id": id, "attributes": map[string]any{"certificateContent": der}})
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, code string, detail string) {
	writeJSON(w, status, map[string]any{"errors": []map[string]string{{"status": fmt.Sprint(status), "code": code, "title": "Request failed", "detail": detail}}})
}
