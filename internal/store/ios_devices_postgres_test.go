package store_test

import (
	"bytes"
	"context"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
	"xprem/internal/appstoreconnect"
	"xprem/internal/appstoreconnect/appstoreconnecttest"
	"xprem/internal/auditlog"
	"xprem/internal/database"
	"xprem/internal/database/postgres/pgdb"
	dashhandlers "xprem/internal/handlers/dashboard"
	"xprem/internal/ios"
	"xprem/internal/ios/iostest"
	"xprem/internal/services"
	"xprem/internal/store"
	"xprem/internal/types"
	"xprem/internal/validation"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"howett.net/plist"
)

const testBaseURL = "https://ota.example.com"

type deviceRegistrationFixture struct {
	*appStoreConnectFixture
	router   *mux.Router
	identity iostest.Identity
}

func newDeviceRegistrationFixture(t *testing.T) *deviceRegistrationFixture {
	t.Helper()
	t.Setenv("BASE_URL", testBaseURL)
	f := &deviceRegistrationFixture{appStoreConnectFixture: newAppStoreConnectFixture(t), router: mux.NewRouter()}
	authority := iostest.NewDeviceAuthority()
	f.identity = iostest.NewIssuedIdentity(&authority, &x509.Certificate{
		Subject:   pkix.Name{CommonName: "Test iPhone"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature,
	})
	f.service.SetDeviceResponseVerifier(ios.NewDeviceResponseVerifier(authority.Certificate))
	handler := dashhandlers.NewIosCredentialsHandler(f.service)
	f.router.HandleFunc("/device-registrations/{TOKEN}", handler.PublicIosDeviceInvitationHandler).Methods(http.MethodGet)
	f.router.HandleFunc("/device-registrations/{TOKEN}/profile", handler.IosDeviceRegistrationProfileHandler).Methods(http.MethodGet)
	f.router.HandleFunc("/device-registrations/{TOKEN}/enroll", handler.EnrollIosDeviceHandler).Methods(http.MethodPost)
	f.router.HandleFunc("/device-registrations/{TOKEN}/registrations/{REGISTRATION_ID}", handler.PublicIosDeviceRegistrationHandler).Methods(http.MethodGet)
	return f
}

func (f *deviceRegistrationFixture) do(method string, target string, body []byte) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	f.router.ServeHTTP(recorder, httptest.NewRequest(method, target, bytes.NewReader(body)))
	return recorder
}

func (f *deviceRegistrationFixture) createInvitation(t *testing.T, label string) string {
	t.Helper()
	_, token, err := f.service.CreateIosDeviceInvitation(context.Background(), f.appId, label, 24)
	require.NoError(t, err)
	return token
}

// challenge downloads the link's .mobileconfig and reads the challenge iOS would send back.
func (f *deviceRegistrationFixture) challenge(t *testing.T, token string) string {
	t.Helper()
	response := f.do(http.MethodGet, "/device-registrations/"+token+"/profile", nil)
	require.Equal(t, http.StatusOK, response.Code)
	var profile struct {
		PayloadContent struct {
			Challenge string `plist:"Challenge"`
		} `plist:"PayloadContent"`
	}
	_, err := plist.Unmarshal(response.Body.Bytes(), &profile)
	require.NoError(t, err)
	return profile.PayloadContent.Challenge
}

func (f *deviceRegistrationFixture) deviceResponseBody(t *testing.T, attributes map[string]string) []byte {
	t.Helper()
	content, err := plist.Marshal(attributes, plist.XMLFormat)
	require.NoError(t, err)
	return f.identity.SignResponse(content, true)
}

// enroll posts a device response and returns the redirect target's query.
func (f *deviceRegistrationFixture) enroll(t *testing.T, token string, body []byte) url.Values {
	t.Helper()
	response := f.do(http.MethodPost, "/device-registrations/"+token+"/enroll", body)
	require.Equal(t, http.StatusMovedPermanently, response.Code)
	location, err := url.Parse(response.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, testBaseURL+"/dashboard/register-device/"+token, location.Scheme+"://"+location.Host+location.Path)
	return location.Query()
}

func (f *deviceRegistrationFixture) publicRegistration(t *testing.T, token string, registrationId string) map[string]any {
	t.Helper()
	response := f.do(http.MethodGet, "/device-registrations/"+token+"/registrations/"+registrationId, nil)
	require.Equal(t, http.StatusOK, response.Code)
	var registration map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &registration))
	return registration
}

func TestIosDeviceInvitationLifecycle(t *testing.T) {
	f := newDeviceRegistrationFixture(t)
	ctx := context.Background()

	_, _, err := f.service.CreateIosDeviceInvitation(ctx, f.appId, "QA team", 24)
	requireValidationMessage(t, err, "Add an App Store Connect API key first")
	f.saveKey(t, f.appId)
	var valErr *validation.Error
	for _, hours := range []int{-1, 721} {
		_, _, err = f.service.CreateIosDeviceInvitation(ctx, f.appId, "", hours)
		assert.ErrorAs(t, err, &valErr, hours)
	}

	invitation, token, err := f.service.CreateIosDeviceInvitation(ctx, f.appId, "  QA team ", 0)
	require.NoError(t, err)
	assert.Regexp(t, `^[A-Za-z0-9_-]{43}$`, token)
	assert.Equal(t, "QA team", invitation.Label)
	var tokenHash string
	require.NoError(t, f.pool.QueryRow(ctx, "SELECT token_hash FROM ios_device_invitations WHERE id = $1", invitation.Id).Scan(&tokenHash))
	assert.NotContains(t, tokenHash, token, "only the hash of the token is stored")

	response := f.do(http.MethodGet, "/device-registrations/"+token, nil)
	require.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	var public map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &public))
	assert.Equal(t, "QA team", public["label"])
	assert.Equal(t, invitation.ExpiresAt, public["expiresAt"])
	assert.Contains(t, public, "appName")

	profile := f.do(http.MethodGet, "/device-registrations/"+token+"/profile", nil)
	require.Equal(t, http.StatusOK, profile.Code)
	assert.Equal(t, "application/x-apple-aspen-config", profile.Header().Get("Content-Type"))
	assert.Equal(t, `attachment; filename="register-iphone.mobileconfig"`, profile.Header().Get("Content-Disposition"))
	assert.Equal(t, "no-store", profile.Header().Get("Cache-Control"))
	var payload struct {
		PayloadUUID    string `plist:"PayloadUUID"`
		PayloadContent struct {
			URL       string `plist:"URL"`
			Challenge string `plist:"Challenge"`
		} `plist:"PayloadContent"`
	}
	_, err = plist.Unmarshal(profile.Body.Bytes(), &payload)
	require.NoError(t, err)
	assert.Equal(t, testBaseURL+"/device-registrations/"+token+"/enroll", payload.PayloadContent.URL)
	assert.Equal(t, strings.ToUpper(invitation.Id), payload.PayloadUUID)
	var challenge string
	require.NoError(t, f.pool.QueryRow(ctx, "SELECT challenge FROM ios_device_invitations WHERE id = $1", invitation.Id).Scan(&challenge))
	assert.Equal(t, challenge, payload.PayloadContent.Challenge)

	expiredToken := f.createInvitation(t, "")
	_, err = f.pool.Exec(ctx, "UPDATE ios_device_invitations SET expires_at = now() - interval '1 minute' WHERE token_hash = encode(sha256($1::bytea), 'hex')", expiredToken)
	require.NoError(t, err)
	revokedToken := f.createInvitation(t, "")
	invitations, err := f.service.ListIosDeviceInvitations(ctx, f.appId)
	require.NoError(t, err)
	require.Len(t, invitations, 3)
	require.NoError(t, f.service.RevokeIosDeviceInvitation(ctx, f.appId, invitations[0].Id))
	notFoundErr := (*store.ErrResourceNotFound)(nil)
	assert.ErrorAs(t, f.service.RevokeIosDeviceInvitation(ctx, insertBareApp(t, f.pool), invitations[0].Id), &notFoundErr, "a link of another app")

	for name, invalid := range map[string]string{"unknown": strings.Repeat("A", 43), "expired": expiredToken, "revoked": revokedToken, "malformed": "not-a-token"} {
		for _, path := range []string{"", "/profile"} {
			response := f.do(http.MethodGet, "/device-registrations/"+invalid+path, nil)
			assert.Equal(t, http.StatusNotFound, response.Code, name+path)
			assert.JSONEq(t, `{"error":"invalid-link"}`, response.Body.String(), name+path)
		}
		assert.Equal(t, "invalid-link", f.enroll(t, invalid, f.deviceResponseBody(t, map[string]string{"UDID": "UDID-1"})).Get("error"), name)
	}

	invitations, err = f.service.ListIosDeviceInvitations(ctx, f.appId)
	require.NoError(t, err)
	assert.NotNil(t, invitations[0].RevokedAt)
	assert.Nil(t, invitations[2].RevokedAt)
	assert.Equal(t, invitation.Id, invitations[2].Id, "newest first")
	statuses := []services.IosDeviceInvitationStatus{}
	for _, listed := range invitations {
		statuses = append(statuses, listed.Status)
		assert.Nil(t, listed.Device)
	}
	assert.Equal(t, []services.IosDeviceInvitationStatus{services.IosDeviceInvitationRevoked, services.IosDeviceInvitationExpired, services.IosDeviceInvitationPending}, statuses)
	assert.Equal(t, []auditlog.Action{
		auditlog.ActionAppStoreConnectApiKeySaved,
		auditlog.ActionIosDeviceInvitationCreated,
		auditlog.ActionIosDeviceInvitationCreated,
		auditlog.ActionIosDeviceInvitationCreated,
		auditlog.ActionIosDeviceInvitationRevoked,
	}, f.actions)
}

func TestIosDeviceEnrollment(t *testing.T) {
	f := newDeviceRegistrationFixture(t)
	ctx := context.Background()
	f.saveKey(t, f.appId)
	f.apple.Devices = []appstoreconnecttest.Device{
		{ID: "EXISTING", Name: "Old iPhone", UDID: "UDID-EXISTING", Model: "iPhone 13", Platform: "IOS", DeviceClass: "IPHONE", Status: "ENABLED", AddedDate: "2026-01-02T03:04:05.000+0000"},
		{ID: "OUTSIDE", Name: "Old iPad", UDID: "UDID-OUTSIDE", Platform: "IOS", DeviceClass: "IPAD", Status: "DISABLED", AddedDate: "2026-03-02T03:04:05.000+0000"},
		{ID: "MAC", Name: "Mac", UDID: "UDID-MAC", Platform: "MAC_OS", DeviceClass: "MAC", Status: "ENABLED"},
	}
	token := f.createInvitation(t, "QA team")
	challenge := f.challenge(t, token)

	assert.Equal(t, "invalid-link", f.enroll(t, token, f.deviceResponseBody(t, map[string]string{"UDID": "UDID-NEW", "CHALLENGE": "wrong"})).Get("error"), "wrong challenge")
	assert.Equal(t, "invalid-link", f.enroll(t, token, []byte("not a device response")).Get("error"))
	assert.Equal(t, "invalid-link", f.enroll(t, token, bytes.Repeat([]byte("x"), 64<<10+1)).Get("error"), "oversized body")

	f.apple.DeviceLimitReached = true
	limited := f.enroll(t, token, f.deviceResponseBody(t, map[string]string{"UDID": "UDID-LIMIT", "PRODUCT": "iPhone16,1", "CHALLENGE": challenge}))
	failed := f.publicRegistration(t, token, limited.Get("registration"))
	assert.Equal(t, "failed", failed["status"])
	assert.Equal(t, "Apple refused this iPhone. Contact the app administrator.", failed["error"])
	assert.Equal(t, http.StatusOK, f.do(http.MethodGet, "/device-registrations/"+token, nil).Code, "a failed registration does not use the link")
	f.apple.DeviceLimitReached = false

	created := f.enroll(t, token, f.deviceResponseBody(t, map[string]string{
		"UDID": "UDID-NEW", "PRODUCT": "iPhone15,2", "VERSION": "22A3354", "CHALLENGE": challenge,
	}))
	require.NotEmpty(t, created.Get("registration"))
	assert.Empty(t, created.Get("error"))
	assert.Equal(t, map[string]any{"status": "registered", "deviceName": "QA team (iPhone15,2)", "product": "iPhone15,2", "error": nil}, f.publicRegistration(t, token, created.Get("registration")))
	registeredAtApple := f.apple.RegisteredDevices()
	require.Len(t, registeredAtApple, 4)
	assert.Equal(t, "UDID-NEW", registeredAtApple[3].UDID)
	assert.Equal(t, "QA team (iPhone15,2)", registeredAtApple[3].Name)

	for _, path := range []string{"", "/profile"} {
		response := f.do(http.MethodGet, "/device-registrations/"+token+path, nil)
		assert.Equal(t, http.StatusGone, response.Code, path)
		assert.JSONEq(t, `{"error":"used"}`, response.Body.String(), path)
	}
	assert.Equal(t, "used", f.enroll(t, token, f.deviceResponseBody(t, map[string]string{"UDID": "UDID-OTHER", "CHALLENGE": challenge})).Get("error"))
	assert.Equal(t, "registered", f.publicRegistration(t, token, created.Get("registration"))["status"], "the outcome stays readable once the link is used")
	for _, registrationId := range []string{created.Get("registration"), limited.Get("registration")} {
		response := f.do(http.MethodGet, "/device-registrations/"+token+"/registrations/"+registrationId, nil)
		assert.NotContains(t, response.Body.String(), "UDID", "the public status never exposes the UDID")
	}

	janeToken := f.createInvitation(t, "Jane")
	already := f.enroll(t, janeToken, f.deviceResponseBody(t, map[string]string{
		"UDID": "UDID-EXISTING", "PRODUCT": "iPhone14,5", "VERSION": "23A341", "DEVICE_NAME": "Jane's iPhone", "CHALLENGE": f.challenge(t, janeToken),
	}))
	assert.Equal(t, "registered", f.publicRegistration(t, janeToken, already.Get("registration"))["status"])
	assert.Len(t, f.apple.RegisteredDevices(), 4, "a device Apple already lists is not created again")
	assert.Equal(t, 2, f.apple.RequestCount("POST /v1/devices"), "the refused one and UDID-NEW")
	response := f.do(http.MethodGet, "/device-registrations/"+janeToken+"/registrations/"+created.Get("registration"), nil)
	assert.Equal(t, http.StatusNotFound, response.Code, "a registration of another link")

	invitations, err := f.service.ListIosDeviceInvitations(ctx, f.appId)
	require.NoError(t, err)
	require.Len(t, invitations, 2)
	assert.Equal(t, services.IosDeviceInvitationUsed, invitations[1].Status)
	assert.Equal(t, &services.IosDeviceInvitationDevice{Name: "QA team (iPhone15,2)", Product: "iPhone15,2"}, invitations[1].Device)

	devices, err := f.service.ListAppleDevices(ctx, f.appId)
	require.NoError(t, err)
	require.Len(t, devices, 3, "Macs are left out")
	assert.Equal(t, []string{"UDID-NEW", "UDID-EXISTING", "UDID-OUTSIDE"}, []string{devices[0].UDID, devices[1].UDID, devices[2].UDID}, "enabled first, then the most recently added")
	assert.Equal(t, "QA team", devices[0].RegisteredVia.Label)
	assert.Equal(t, invitations[1].Id, devices[0].RegisteredVia.InvitationId)
	assert.Equal(t, "22A3354", devices[0].OSVersion)
	assert.Equal(t, services.AppleDevice{
		Id: "EXISTING", Name: "Old iPhone", UDID: "UDID-EXISTING", DeviceClass: "IPHONE", Model: "iPhone 13",
		Product: "iPhone14,5", OSVersion: "23A341", Status: "ENABLED", AddedAt: "2026-01-02T03:04:05Z",
		RegisteredVia: &services.IosDeviceRegisteredVia{InvitationId: invitations[0].Id, Label: "Jane", RegisteredAt: devices[1].RegisteredVia.RegisteredAt},
	}, devices[1])
	assert.Equal(t, services.AppleDevice{
		Id: "OUTSIDE", Name: "Old iPad", UDID: "UDID-OUTSIDE", DeviceClass: "IPAD", Status: "DISABLED", AddedAt: "2026-03-02T03:04:05Z",
	}, devices[2], "a device added outside xprem has no product, version or link")
	assert.False(t, f.hasRegistration(t, "UDID-LIMIT"), "a failed registration is not a registered device")

	registered := 0
	for _, action := range f.actions {
		if action == auditlog.ActionIosDeviceRegistered {
			registered++
		}
	}
	assert.Equal(t, 2, registered)
}

func TestIosDeviceInvitationIsSingleUse(t *testing.T) {
	f := newDeviceRegistrationFixture(t)
	ctx := context.Background()
	f.saveKey(t, f.appId)
	token := f.createInvitation(t, "")
	challenge := f.challenge(t, token)

	credentials := store.NewPostgresIosCredentialsStore(&database.Engine{Queries: pgdb.New(f.pool), DB: f.pool})
	invitations, err := f.service.ListIosDeviceInvitations(ctx, f.appId)
	require.NoError(t, err)
	invitationId := invitations[0].Id
	claimToken, err := credentials.ClaimIosDeviceInvitation(ctx, invitationId)
	require.NoError(t, err)
	assert.NotEmpty(t, claimToken)
	otherClaim, err := credentials.ClaimIosDeviceInvitation(ctx, invitationId)
	require.NoError(t, err)
	assert.Empty(t, otherClaim, "a claimed link cannot be claimed again")
	assert.Equal(t, "used", f.enroll(t, token, f.deviceResponseBody(t, map[string]string{"UDID": "UDID-WAITING", "CHALLENGE": challenge})).Get("error"), "an enrollment in progress holds the link")
	require.NoError(t, credentials.ReleaseIosDeviceInvitation(ctx, invitationId, claimToken))
	_, err = f.pool.Exec(ctx, "UPDATE ios_device_invitations SET claimed_at = now() - interval '6 minutes' WHERE id = $1", invitationId)
	require.NoError(t, err)
	claimToken, err = credentials.ClaimIosDeviceInvitation(ctx, invitationId)
	require.NoError(t, err)
	assert.NotEmpty(t, claimToken, "an abandoned claim expires")
	require.NoError(t, credentials.ReleaseIosDeviceInvitation(ctx, invitationId, claimToken))

	const enrollments = 8
	locations := make(chan string, enrollments)
	var group sync.WaitGroup
	for i := range enrollments {
		body := f.deviceResponseBody(t, map[string]string{"UDID": fmt.Sprintf("UDID-CONCURRENT-%d", i), "CHALLENGE": challenge})
		group.Add(1)
		go func() {
			defer group.Done()
			locations <- f.do(http.MethodPost, "/device-registrations/"+token+"/enroll", body).Header().Get("Location")
		}()
	}
	group.Wait()
	close(locations)
	outcomes := map[string]int{}
	for location := range locations {
		parsed, err := url.Parse(location)
		require.NoError(t, err)
		if parsed.Query().Get("registration") != "" {
			outcomes["registered"]++
		} else {
			outcomes[parsed.Query().Get("error")]++
		}
	}
	assert.Equal(t, map[string]int{"registered": 1, "used": enrollments - 1}, outcomes)
	var registrations int
	require.NoError(t, f.pool.QueryRow(ctx, "SELECT COUNT(*) FROM ios_device_registrations WHERE invitation_id = $1 AND status = 'registered'", invitationId).Scan(&registrations))
	assert.Equal(t, 1, registrations)
	assert.Equal(t, 1, f.apple.RequestCount("POST /v1/devices"))
}

// TestIosDeviceInvitationRejectsStaleOwners covers workers resuming after a claim is replaced.
func TestIosDeviceInvitationRejectsStaleOwners(t *testing.T) {
	for _, status := range []types.IosDeviceRegistrationStatus{types.IosDeviceRegistered, types.IosDeviceRegistrationFailed} {
		t.Run(string(status), func(t *testing.T) {
			f := newDeviceRegistrationFixture(t)
			ctx := context.Background()
			f.saveKey(t, f.appId)
			f.createInvitation(t, "")
			invitations, err := f.service.ListIosDeviceInvitations(ctx, f.appId)
			require.NoError(t, err)
			invitationID := invitations[0].Id
			credentials := store.NewPostgresIosCredentialsStore(&database.Engine{Queries: pgdb.New(f.pool), DB: f.pool})
			first, err := credentials.ClaimIosDeviceInvitation(ctx, invitationID)
			require.NoError(t, err)
			require.NotEmpty(t, first)
			_, err = f.pool.Exec(ctx, "UPDATE ios_device_invitations SET claimed_at = now() - interval '6 minutes' WHERE id = $1", invitationID)
			require.NoError(t, err)
			second, err := credentials.ClaimIosDeviceInvitation(ctx, invitationID)
			require.NoError(t, err)
			require.NotEmpty(t, second)
			require.NotEqual(t, first, second)
			registration := store.IosDeviceRegistration{InvitationId: invitationID, UDID: uuid.NewString(), Status: status}
			_, err = credentials.FinishIosDeviceRegistration(ctx, registration, first)
			require.ErrorIs(t, err, store.ErrIosDeviceInvitationClaimLost)
			require.NoError(t, credentials.ReleaseIosDeviceInvitation(ctx, invitationID, first))
			var activeToken string
			require.NoError(t, f.pool.QueryRow(ctx, "SELECT claim_token::text FROM ios_device_invitations WHERE id = $1 AND consumed_at IS NULL AND claimed_at IS NOT NULL", invitationID).Scan(&activeToken))
			assert.Equal(t, second, activeToken)
			var count int
			require.NoError(t, f.pool.QueryRow(ctx, "SELECT count(*) FROM ios_device_registrations WHERE invitation_id = $1", invitationID).Scan(&count))
			assert.Zero(t, count, "stale completions must roll back the registration")
			registration.Status = types.IosDeviceRegistered
			_, err = credentials.FinishIosDeviceRegistration(ctx, registration, second)
			require.NoError(t, err)
			_, err = credentials.FinishIosDeviceRegistration(ctx, registration, second)
			require.ErrorIs(t, err, store.ErrIosDeviceInvitationClaimLost, "a completed claim cannot be reused")
		})
	}
}

// TestIosDeviceConflictDoesNotExposeUDID covers the conflict fallback when re-finding fails.
func TestIosDeviceConflictDoesNotExposeUDID(t *testing.T) {
	f := newDeviceRegistrationFixture(t)
	f.saveKey(t, f.appId)
	f.apple.DeviceConflictDetail = "A device with number 'UDID-PRIVATE' already exists on this team."
	token := f.createInvitation(t, "")
	redirect := f.enroll(t, token, f.deviceResponseBody(t, map[string]string{"UDID": "UDID-PRIVATE", "CHALLENGE": f.challenge(t, token)}))
	registration := f.publicRegistration(t, token, redirect.Get("registration"))
	assert.Equal(t, "failed", registration["status"])
	assert.NotContains(t, registration["error"], "UDID-PRIVATE")
	assert.Equal(t, "Apple refused this iPhone. Contact the app administrator.", registration["error"])
}

// TestIosEnrollmentRejectsUnverifiedDevice proves unsigned responses cannot reach Apple or consume a link.
func TestIosEnrollmentRejectsUnverifiedDevice(t *testing.T) {
	f := newDeviceRegistrationFixture(t)
	f.saveKey(t, f.appId)
	token := f.createInvitation(t, "")
	udid := uuid.NewString()
	attributes := map[string]string{"UDID": udid, "CHALLENGE": f.challenge(t, token)}
	content, err := plist.Marshal(attributes, plist.XMLFormat)
	require.NoError(t, err)
	unsigned := iostest.SignedData(content, true)
	assert.Equal(t, "invalid-link", f.enroll(t, token, unsigned).Get("error"))
	assert.Zero(t, f.apple.RequestCount("POST /v1/devices"))
	assert.False(t, f.hasRegistration(t, udid))
	assert.Equal(t, http.StatusOK, f.do(http.MethodGet, "/device-registrations/"+token, nil).Code)
	valid := f.enroll(t, token, f.deviceResponseBody(t, attributes))
	assert.NotEmpty(t, valid.Get("registration"), "rejecting an unsigned request leaves the invitation usable")
}

func TestAppleDeviceStatus(t *testing.T) {
	f := newDeviceRegistrationFixture(t)
	ctx := context.Background()
	f.apple.Devices = []appstoreconnecttest.Device{{ID: "DEVICE1", Name: "Jane's iPhone", UDID: "UDID-1", Platform: "IOS", DeviceClass: "IPHONE", Status: "ENABLED"}}

	requireValidationMessage(t, f.service.SetAppleDeviceEnabled(ctx, f.appId, "DEVICE1", false), "add an API key")
	f.saveKey(t, f.appId)
	require.NoError(t, f.service.SetAppleDeviceEnabled(ctx, f.appId, "DEVICE1", false))
	assert.Equal(t, "DISABLED", f.apple.RegisteredDevices()[0].Status)
	require.NoError(t, f.service.SetAppleDeviceEnabled(ctx, f.appId, "DEVICE1", true))
	assert.Equal(t, "ENABLED", f.apple.RegisteredDevices()[0].Status)
	assert.Equal(t, 2, f.apple.RequestCount("PATCH /v1/devices/DEVICE1"))

	notFoundErr := (*store.ErrResourceNotFound)(nil)
	assert.ErrorAs(t, f.service.SetAppleDeviceEnabled(ctx, f.appId, "UNKNOWN", false), &notFoundErr)
	assert.ErrorAs(t, f.service.SetAppleDeviceEnabled(ctx, f.appId, "../certificates", false), &notFoundErr)
	f.apple.Status = http.StatusServiceUnavailable
	assert.ErrorIs(t, f.service.SetAppleDeviceEnabled(ctx, f.appId, "DEVICE1", false), appstoreconnect.ErrUnavailable)

	assert.Equal(t, []auditlog.Action{
		auditlog.ActionAppStoreConnectApiKeySaved,
		auditlog.ActionIosDeviceDisabled,
		auditlog.ActionIosDeviceEnabled,
	}, f.actions)
}

func (f *deviceRegistrationFixture) hasRegistration(t *testing.T, udid string) bool {
	t.Helper()
	var count int
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT COUNT(*) FROM ios_device_registrations WHERE udid = $1 AND status = $2", udid, types.IosDeviceRegistered).Scan(&count))
	return count > 0
}
