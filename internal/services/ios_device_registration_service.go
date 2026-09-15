package services

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
	"xprem/internal/appstoreconnect"
	"xprem/internal/auditlog"
	"xprem/internal/ios"
	"xprem/internal/store"
	"xprem/internal/types"
	"xprem/internal/validation"

	"github.com/google/uuid"
)

const (
	defaultIosDeviceInvitationHours = 168
	maxIosDeviceInvitationLabel     = 100
	maxAppleDeviceName              = 50
)

var (
	iosDeviceInvitationToken = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	appleDeviceIdPattern     = regexp.MustCompile(`^[A-Za-z0-9]{1,64}$`)
)

// ErrIosDeviceInvitationUsed reports a registration link that already registered its iPhone.
var ErrIosDeviceInvitationUsed = errors.New("ios device invitation already used")

// IosDeviceInvitationStatus is the state of a registration link as shown in the dashboard.
type IosDeviceInvitationStatus string

const (
	IosDeviceInvitationPending IosDeviceInvitationStatus = "pending"
	IosDeviceInvitationUsed    IosDeviceInvitationStatus = "used"
	IosDeviceInvitationExpired IosDeviceInvitationStatus = "expired"
	IosDeviceInvitationRevoked IosDeviceInvitationStatus = "revoked"
)

// IosDeviceInvitation is an iPhone registration link as shown in the dashboard; Device is the iPhone a used link registered.
type IosDeviceInvitation struct {
	Id        string                     `json:"id"`
	Label     string                     `json:"label"`
	Status    IosDeviceInvitationStatus  `json:"status"`
	Device    *IosDeviceInvitationDevice `json:"device"`
	ExpiresAt string                     `json:"expiresAt"`
	RevokedAt *string                    `json:"revokedAt"`
	CreatedAt string                     `json:"createdAt"`
	CreatedBy string                     `json:"createdBy"`
}

type IosDeviceInvitationDevice struct {
	Name    string `json:"name"`
	Product string `json:"product"`
}

// PublicIosDeviceInvitation is what the public registration page may know about a link.
type PublicIosDeviceInvitation struct {
	AppName   string `json:"appName"`
	Label     string `json:"label"`
	ExpiresAt string `json:"expiresAt"`
}

// PublicIosDeviceRegistration is the outcome shown to the person who registered an iPhone; it never carries the UDID.
type PublicIosDeviceRegistration struct {
	Status     types.IosDeviceRegistrationStatus `json:"status"`
	DeviceName string                            `json:"deviceName"`
	Product    string                            `json:"product"`
	Error      *string                           `json:"error"`
}

// AppleDevice is a non-Mac device of the Apple team; Product, OSVersion and RegisteredVia come from
// the latest registration of its UDID through the app's links.
type AppleDevice struct {
	Id            string                  `json:"id"`
	Name          string                  `json:"name"`
	UDID          string                  `json:"udid"`
	DeviceClass   string                  `json:"deviceClass"`
	Model         string                  `json:"model"`
	Product       string                  `json:"product"`
	OSVersion     string                  `json:"osVersion"`
	Status        string                  `json:"status"`
	AddedAt       string                  `json:"addedAt"`
	RegisteredVia *IosDeviceRegisteredVia `json:"registeredVia"`
}

type IosDeviceRegisteredVia struct {
	InvitationId string `json:"invitationId"`
	Label        string `json:"label"`
	RegisteredAt string `json:"registeredAt"`
}

// iosDeviceTokenHash hashes a bearer token so its plaintext is never needed in the database.
func iosDeviceTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// randomURLSafeString generates 32 cryptographically random bytes encoded for use in links.
func randomURLSafeString() (string, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(secret), nil
}

// CreateIosDeviceInvitation creates a registration link and returns its token, which is not stored.
func (s *IosCredentialsService) CreateIosDeviceInvitation(ctx context.Context, appId string, label string, expiresInHours int) (*IosDeviceInvitation, string, error) {
	appId, err := s.canonicalAppID(appId)
	if err != nil {
		return nil, "", err
	}
	label = strings.TrimSpace(label)
	if utf8.RuneCountInString(label) > maxIosDeviceInvitationLabel {
		return nil, "", validation.Errorf("label", "label must be at most %d characters", maxIosDeviceInvitationLabel)
	}
	if expiresInHours == 0 {
		expiresInHours = defaultIosDeviceInvitationHours
	}
	if expiresInHours < 1 || expiresInHours > 720 {
		return nil, "", validation.Errorf("expiresInHours", "must be between 1 and 720")
	}
	key, err := s.repo.GetAppStoreConnectApiKey(ctx, appId)
	if err != nil {
		return nil, "", err
	}
	if key == nil {
		return nil, "", validation.Errorf("", "Add an App Store Connect API key first")
	}
	token, err := randomURLSafeString()
	if err != nil {
		return nil, "", err
	}
	challenge, err := randomURLSafeString()
	if err != nil {
		return nil, "", err
	}
	actorType, actorId, actorDisplay := auditActorFromContext(ctx)
	invitation := store.NewIosDeviceInvitation{
		Id:           uuid.NewString(),
		AppId:        appId,
		TokenHash:    iosDeviceTokenHash(token),
		Challenge:    challenge,
		Label:        label,
		ExpiresAt:    time.Now().Add(time.Duration(expiresInHours) * time.Hour),
		ActorType:    actorType,
		ActorId:      actorId,
		ActorDisplay: actorDisplay,
	}
	createdAt, err := s.repo.InsertIosDeviceInvitation(ctx, invitation)
	if err != nil {
		return nil, "", err
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionIosDeviceInvitationCreated,
		TargetType:    "ios_device_invitation",
		TargetID:      invitation.Id,
		TargetDisplay: label,
		AppID:         appId,
		Metadata:      map[string]any{"label": label, "expires_at": invitation.ExpiresAt.UTC().Format(time.RFC3339)},
	})
	return &IosDeviceInvitation{
		Id:        invitation.Id,
		Label:     label,
		Status:    IosDeviceInvitationPending,
		ExpiresAt: invitation.ExpiresAt.UTC().Format(time.RFC3339),
		CreatedAt: createdAt.UTC().Format(time.RFC3339),
		CreatedBy: actorDisplay,
	}, token, nil
}

// ListIosDeviceInvitations returns every registration link of the app, newest first, whatever its status.
func (s *IosCredentialsService) ListIosDeviceInvitations(ctx context.Context, appId string) ([]IosDeviceInvitation, error) {
	appId, err := s.canonicalAppID(appId)
	if err != nil {
		return nil, err
	}
	rows, err := s.repo.ListIosDeviceInvitations(ctx, appId)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	invitations := make([]IosDeviceInvitation, len(rows))
	for i, row := range rows {
		invitations[i] = IosDeviceInvitation{
			Id:        row.Id,
			Label:     row.Label,
			Status:    IosDeviceInvitationPending,
			ExpiresAt: row.ExpiresAt.UTC().Format(time.RFC3339),
			CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339),
			CreatedBy: row.CreatedBy,
		}
		if row.RevokedAt != nil {
			revokedAt := row.RevokedAt.UTC().Format(time.RFC3339)
			invitations[i].RevokedAt = &revokedAt
		}
		switch {
		case row.ConsumedAt != nil:
			invitations[i].Status = IosDeviceInvitationUsed
			invitations[i].Device = &IosDeviceInvitationDevice{Name: row.DeviceName, Product: row.DeviceProduct}
		case row.RevokedAt != nil:
			invitations[i].Status = IosDeviceInvitationRevoked
		case !now.Before(row.ExpiresAt):
			invitations[i].Status = IosDeviceInvitationExpired
		}
	}
	return invitations, nil
}

// RevokeIosDeviceInvitation revokes an app invitation and records the management event.
func (s *IosCredentialsService) RevokeIosDeviceInvitation(ctx context.Context, appId string, invitationId string) error {
	appId, err := s.canonicalAppID(appId)
	if err != nil {
		return err
	}
	label, err := s.repo.RevokeIosDeviceInvitation(ctx, appId, invitationId)
	if err != nil {
		return err
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionIosDeviceInvitationRevoked,
		TargetType:    "ios_device_invitation",
		TargetID:      invitationId,
		TargetDisplay: label,
		AppID:         appId,
	})
	return nil
}

// ListAppleDevices returns the team's non-Mac devices, enabled ones first, then the most recently added.
func (s *IosCredentialsService) ListAppleDevices(ctx context.Context, appId string) ([]AppleDevice, error) {
	appId, err := s.canonicalAppID(appId)
	if err != nil {
		return nil, err
	}
	client, err := s.appStoreConnectClient(ctx, appId)
	if err != nil {
		return nil, err
	}
	appleDevices, err := client.ListIOSDevices(ctx)
	if err != nil {
		return nil, appStoreConnectError(err)
	}
	registered, err := s.repo.ListRegisteredIosDevices(ctx, appId)
	if err != nil {
		return nil, err
	}
	registrations := map[string]store.RegisteredIosDevice{}
	for _, registration := range registered {
		registrations[registration.UDID] = registration
	}
	devices := make([]AppleDevice, len(appleDevices))
	for i, device := range appleDevices {
		devices[i] = AppleDevice{
			Id:          device.ID,
			Name:        device.Name,
			UDID:        device.UDID,
			DeviceClass: device.DeviceClass,
			Model:       device.Model,
			Status:      device.Status,
			AddedAt:     appleTimestamp(device.AddedDate),
		}
		if registration, ok := registrations[strings.ToUpper(device.UDID)]; ok {
			devices[i].Product = registration.Product
			devices[i].OSVersion = registration.OSVersion
			devices[i].RegisteredVia = &IosDeviceRegisteredVia{
				InvitationId: registration.InvitationId,
				Label:        registration.Label,
				RegisteredAt: registration.RegisteredAt.UTC().Format(time.RFC3339),
			}
		}
	}
	// RFC3339 UTC timestamps sort chronologically as strings.
	slices.SortStableFunc(devices, func(a, b AppleDevice) int {
		if enabledA, enabledB := a.Status == appleDeviceEnabled, b.Status == appleDeviceEnabled; enabledA != enabledB {
			if enabledA {
				return -1
			}
			return 1
		}
		return strings.Compare(b.AddedAt, a.AddedAt)
	})
	return devices, nil
}

const (
	appleDeviceEnabled  = "ENABLED"
	appleDeviceDisabled = "DISABLED"
)

// SetAppleDeviceEnabled enables or disables a device of the app's Apple team.
func (s *IosCredentialsService) SetAppleDeviceEnabled(ctx context.Context, appId string, deviceId string, enabled bool) error {
	appId, err := s.canonicalAppID(appId)
	if err != nil {
		return err
	}
	if !appleDeviceIdPattern.MatchString(deviceId) {
		return &store.ErrResourceNotFound{Resource: "apple device", Identifier: deviceId}
	}
	client, err := s.appStoreConnectClient(ctx, appId)
	if err != nil {
		return err
	}
	status, action := appleDeviceDisabled, auditlog.ActionIosDeviceDisabled
	if enabled {
		status, action = appleDeviceEnabled, auditlog.ActionIosDeviceEnabled
	}
	device, err := client.UpdateDeviceStatus(ctx, deviceId, status)
	var apiErr *appstoreconnect.APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
		return &store.ErrResourceNotFound{Resource: "apple device", Identifier: deviceId}
	}
	if err != nil {
		return appStoreConnectError(err)
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        action,
		TargetType:    "ios_device",
		TargetID:      deviceId,
		TargetDisplay: device.Name,
		AppID:         appId,
		Metadata:      map[string]any{"device_id": deviceId, "name": device.Name},
	})
	return nil
}

// appleTimestamp converts Apple's timestamps, such as 2026-09-15T08:00:00.000+0000, to RFC3339.
func appleTimestamp(value string) string {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000-0700"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC().Format(time.RFC3339)
		}
	}
	return value
}

// activeIosDeviceInvitation resolves a token; unknown, expired and revoked links are all not found, a
// consumed link is ErrIosDeviceInvitationUsed.
func (s *IosCredentialsService) activeIosDeviceInvitation(ctx context.Context, token string) (*store.ActiveIosDeviceInvitation, error) {
	if s.repo == nil {
		return nil, store.ErrNotSupportedInStatelessMode
	}
	notFound := &store.ErrResourceNotFound{Resource: "ios device invitation", Identifier: "link"}
	if !iosDeviceInvitationToken.MatchString(token) {
		return nil, notFound
	}
	invitation, err := s.repo.ResolveIosDeviceInvitation(ctx, iosDeviceTokenHash(token))
	if err != nil {
		return nil, err
	}
	if invitation == nil {
		return nil, notFound
	}
	if invitation.Consumed {
		return nil, ErrIosDeviceInvitationUsed
	}
	return invitation, nil
}

// GetPublicIosDeviceInvitation returns safe display metadata for an active, unused registration link.
func (s *IosCredentialsService) GetPublicIosDeviceInvitation(ctx context.Context, token string) (*PublicIosDeviceInvitation, error) {
	invitation, err := s.activeIosDeviceInvitation(ctx, token)
	if err != nil {
		return nil, err
	}
	return &PublicIosDeviceInvitation{
		AppName:   invitation.AppName,
		Label:     invitation.Label,
		ExpiresAt: invitation.ExpiresAt.UTC().Format(time.RFC3339),
	}, nil
}

// GetPublicIosDeviceRegistration answers for a registration of the link, even once the link expired or was revoked.
func (s *IosCredentialsService) GetPublicIosDeviceRegistration(ctx context.Context, token string, registrationId string) (*PublicIosDeviceRegistration, error) {
	if s.repo == nil {
		return nil, store.ErrNotSupportedInStatelessMode
	}
	notFound := &store.ErrResourceNotFound{Resource: "ios device registration", Identifier: "link"}
	if _, err := uuid.Parse(registrationId); err != nil || !iosDeviceInvitationToken.MatchString(token) {
		return nil, notFound
	}
	registration, err := s.repo.GetIosDeviceRegistration(ctx, iosDeviceTokenHash(token), registrationId)
	if err != nil {
		return nil, err
	}
	if registration == nil {
		return nil, notFound
	}
	return &PublicIosDeviceRegistration{
		Status:     registration.Status,
		DeviceName: registration.DeviceName,
		Product:    registration.Product,
		Error:      registration.Error,
	}, nil
}

// IosDeviceRegistrationProfile renders the .mobileconfig of a link; enrollURL is where iOS posts its attributes.
func (s *IosCredentialsService) IosDeviceRegistrationProfile(ctx context.Context, token string, enrollURL string) ([]byte, error) {
	invitation, err := s.activeIosDeviceInvitation(ctx, token)
	if err != nil {
		return nil, err
	}
	return ios.RegistrationProfile(ios.RegistrationProfileInput{
		PayloadUUID: strings.ToUpper(invitation.Id),
		AppName:     invitation.AppName,
		EnrollURL:   enrollURL,
		Challenge:   invitation.Challenge,
	})
}

// EnrollIosDevice registers at Apple the iPhone that posted its attributes through a link and
// returns the id of the recorded registration, successful or failed. Only a successful registration
// consumes the link.
func (s *IosCredentialsService) EnrollIosDevice(ctx context.Context, token string, body []byte) (string, error) {
	invitation, err := s.activeIosDeviceInvitation(ctx, token)
	if err != nil {
		return "", err
	}
	parseResponse := ios.ParseDeviceResponse
	if s.deviceResponseVerifier != nil {
		parseResponse = s.deviceResponseVerifier.Parse
	}
	attributes, err := parseResponse(body, invitation.Challenge)
	if err != nil {
		return "", validation.Errorf("", "invalid device response")
	}
	claimToken, err := s.repo.ClaimIosDeviceInvitation(ctx, invitation.Id)
	if err != nil {
		return "", err
	}
	if claimToken == "" {
		return "", ErrIosDeviceInvitationUsed
	}
	registration := store.IosDeviceRegistration{
		InvitationId: invitation.Id,
		UDID:         attributes.UDID,
		DeviceName:   appleDeviceName(attributes, invitation.Label),
		Product:      attributes.Product,
		OSVersion:    attributes.Version,
		Status:       types.IosDeviceRegistered,
	}
	appleDeviceId, err := s.registerAppleDevice(ctx, invitation.AppId, registration.DeviceName, attributes.UDID)
	if err != nil {
		registration.Status = types.IosDeviceRegistrationFailed
		message := registrationErrorMessage(err)
		registration.Error = &message
	} else {
		registration.AppleDeviceId = &appleDeviceId
	}
	registrationId, err := s.repo.FinishIosDeviceRegistration(ctx, registration, claimToken)
	if err != nil {
		if releaseErr := s.repo.ReleaseIosDeviceInvitation(context.WithoutCancel(ctx), invitation.Id, claimToken); releaseErr != nil {
			log.Printf("ios device invitation release failed: %v", releaseErr)
		}
		if errors.Is(err, store.ErrIosDeviceInvitationClaimLost) {
			return "", ErrIosDeviceInvitationUsed
		}
		return "", err
	}
	if registration.Status == types.IosDeviceRegistered && s.onAuditEvent != nil {
		s.onAuditEvent(ctx, auditlog.Event{
			ActorType:     auditlog.ActorSystem,
			ActorID:       "device-registration",
			ActorDisplay:  "iPhone registration link",
			Action:        auditlog.ActionIosDeviceRegistered,
			TargetType:    "ios_device",
			TargetID:      appleDeviceId,
			TargetDisplay: registration.DeviceName,
			AppID:         invitation.AppId,
			Outcome:       auditlog.OutcomeSuccess,
			Metadata:      map[string]any{"invitation_id": invitation.Id, "product": registration.Product},
		})
	}
	return registrationId, nil
}

// registerAppleDevice returns the Apple id of the device, registering it when the team does not have it yet.
func (s *IosCredentialsService) registerAppleDevice(ctx context.Context, appId string, name string, udid string) (string, error) {
	client, err := s.appStoreConnectClient(ctx, appId)
	if err != nil {
		return "", err
	}
	appleDeviceId, err := client.FindDevice(ctx, udid)
	if err != nil {
		return "", appStoreConnectError(err)
	}
	if appleDeviceId != "" {
		return appleDeviceId, nil
	}
	appleDeviceId, err = client.RegisterIOSDevice(ctx, name, udid)
	var apiErr *appstoreconnect.APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusConflict {
		if existing, findErr := client.FindDevice(ctx, udid); findErr == nil && existing != "" {
			return existing, nil
		}
		log.Printf("ios device registration conflict: %s", apiErr.Detail)
		return "", validation.Errorf("", "Apple refused this iPhone. Contact the app administrator.")
	}
	if err != nil {
		return "", appStoreConnectError(err)
	}
	return appleDeviceId, nil
}

// registrationErrorMessage returns a public-safe enrollment error and logs unexpected failures.
func registrationErrorMessage(err error) string {
	var valErr *validation.Error
	switch {
	case errors.As(err, &valErr):
		return valErr.Error()
	case errors.Is(err, appstoreconnect.ErrUnavailable):
		return "App Store Connect could not be reached. Try again in a few minutes."
	}
	log.Printf("ios device registration failed: %v", err)
	return "The server could not register this iPhone."
}

// appleDeviceName is the iPhone's own name, else "<label or iPhone> (<model>)", cut to Apple's 50 characters.
func appleDeviceName(attributes *ios.DeviceAttributes, label string) string {
	name := strings.TrimSpace(attributes.DeviceName)
	if name == "" {
		name = label
		if name == "" {
			name = "iPhone"
		}
		if attributes.Product != "" {
			name += " (" + attributes.Product + ")"
		}
	}
	if runes := []rune(name); len(runes) > maxAppleDeviceName {
		name = string(runes[:maxAppleDeviceName])
	}
	return name
}
