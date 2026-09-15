package services

import (
	"context"
	"time"
	"xprem/internal/appstoreconnect"
	"xprem/internal/auditlog"
	"xprem/internal/ios"
	"xprem/internal/store"
	"xprem/internal/types"
	"xprem/internal/validation"

	"github.com/google/uuid"
)

type IosCredentialsRepository interface {
	SaveIosCertificate(ctx context.Context, certificate store.IosCertificate, seal store.SealIosCertificateFunc) (string, error)
	GetIosCertificate(ctx context.Context, certificateId string) (*store.IosCertificate, error)
	ListIosCertificates(ctx context.Context) ([]store.IosCertificate, error)
	GetIosSigningSetting(ctx context.Context, identifierId string) (*store.IosSigningSetting, error)
	UpsertIosSigningSetting(ctx context.Context, identifierId string, setting store.IosSigningSetting) error
	UpsertAppStoreConnectApiKey(ctx context.Context, appId string, key store.SealedAppStoreConnectApiKey) error
	GetAppStoreConnectApiKey(ctx context.Context, appId string) (*store.SealedAppStoreConnectApiKey, error)
	DeleteAppStoreConnectApiKey(ctx context.Context, appId string) error
	InsertIosDeviceInvitation(ctx context.Context, invitation store.NewIosDeviceInvitation) (time.Time, error)
	ListIosDeviceInvitations(ctx context.Context, appId string) ([]store.IosDeviceInvitation, error)
	RevokeIosDeviceInvitation(ctx context.Context, appId string, invitationId string) (string, error)
	ResolveIosDeviceInvitation(ctx context.Context, tokenHash string) (*store.ActiveIosDeviceInvitation, error)
	ClaimIosDeviceInvitation(ctx context.Context, invitationId string) (string, error)
	FinishIosDeviceRegistration(ctx context.Context, registration store.IosDeviceRegistration, claimToken string) (string, error)
	ReleaseIosDeviceInvitation(ctx context.Context, invitationId string, claimToken string) error
	GetIosDeviceRegistration(ctx context.Context, tokenHash string, registrationId string) (*store.IosDeviceRegistration, error)
	ListRegisteredIosDevices(ctx context.Context, appId string) ([]store.RegisteredIosDevice, error)
}

// IosCredentialsMetadata is the signing state of an iOS identifier, shared by App Store, TestFlight and Ad Hoc builds.
type IosCredentialsMetadata struct {
	Identifier string                `json:"identifier"`
	Signing    IosSigningSettingView `json:"signing"`
}

// IosSigningSettingView is the signing choice of an identifier; CertificateMissing reports a
// selected certificate that was deleted from the pool.
type IosSigningSettingView struct {
	Mode               types.IosSigningMode    `json:"mode"`
	Certificate        *IosCertificateMetadata `json:"certificate"`
	CertificateMissing bool                    `json:"certificateMissing"`
}

type IosCertificateMetadata struct {
	Id           string                     `json:"id"`
	CommonName   string                     `json:"commonName"`
	SerialNumber string                     `json:"serialNumber"`
	Type         types.IosCertificateType   `json:"type"`
	TeamID       string                     `json:"teamId"`
	ExpiresAt    string                     `json:"expiresAt"`
	Source       types.IosCertificateSource `json:"source"`
}

// IosSigningSettingInput is a signing choice submitted for an identifier.
type IosSigningSettingInput struct {
	Mode          types.IosSigningMode
	CertificateId string
}

type IosCredentialsService struct {
	repo        IosCredentialsRepository
	identifiers AppIdentifierRepository
	// onAuditEvent is the audit emission seam; nil (community) means
	// credential changes leave no events.
	onAuditEvent           auditlog.RecordFunc
	appStoreConnectBaseURL string
	deviceResponseVerifier *ios.DeviceResponseVerifier
}

// NewIosCredentialsService builds the service; nil repos (stateless mode) make
// every method answer ErrNotSupportedInStatelessMode.
func NewIosCredentialsService(repo IosCredentialsRepository, identifiers AppIdentifierRepository) *IosCredentialsService {
	return &IosCredentialsService{
		repo:                   repo,
		identifiers:            identifiers,
		appStoreConnectBaseURL: appstoreconnect.DefaultBaseURL,
	}
}

// SetOnAuditEvent plugs the audit emission seam. Nil-safe.
func (s *IosCredentialsService) SetOnAuditEvent(record auditlog.RecordFunc) {
	s.onAuditEvent = record
}

// SetAppStoreConnectBaseURL points the service at another App Store Connect API.
func (s *IosCredentialsService) SetAppStoreConnectBaseURL(baseURL string) {
	s.appStoreConnectBaseURL = baseURL
}

// SetDeviceResponseVerifier selects the device trust anchor for integration tests; nil uses Apple's CA.
func (s *IosCredentialsService) SetDeviceResponseVerifier(verifier *ios.DeviceResponseVerifier) {
	s.deviceResponseVerifier = verifier
}

// canonicalAppID binds sealed app blobs to one spelling of the app uuid.
func (s *IosCredentialsService) canonicalAppID(appId string) (string, error) {
	if s.repo == nil {
		return "", store.ErrNotSupportedInStatelessMode
	}
	parsed, err := uuid.Parse(appId)
	if err != nil {
		return "", &store.ErrResourceNotFound{Resource: "app", Identifier: appId}
	}
	return parsed.String(), nil
}

// resolveIosIdentifier maps (app, identifier id) to the identifier row,
// refusing unknown ids and non-ios platforms.
func (s *IosCredentialsService) resolveIosIdentifier(ctx context.Context, appId string, identifierId string) (*store.AppIdentifierRef, error) {
	if s.repo == nil || s.identifiers == nil {
		return nil, store.ErrNotSupportedInStatelessMode
	}
	ref, err := s.identifiers.GetAppIdentifierByID(ctx, appId, identifierId)
	if err != nil {
		return nil, err
	}
	if ref == nil {
		return nil, &store.ErrResourceNotFound{Resource: "app identifier", Identifier: identifierId}
	}
	if ref.Platform != types.PlatformIOS {
		return nil, validation.Errorf("identifier", "identifier %q is an %s identifier, ios credentials require an ios one", ref.Identifier, ref.Platform)
	}
	return ref, nil
}

// GetIosCredentialsMetadata answers for every ios identifier; an identifier without a stored setting is automatic.
func (s *IosCredentialsService) GetIosCredentialsMetadata(ctx context.Context, appId string, identifierId string) (*IosCredentialsMetadata, error) {
	ref, err := s.resolveIosIdentifier(ctx, appId, identifierId)
	if err != nil {
		return nil, err
	}
	setting, err := s.repo.GetIosSigningSetting(ctx, ref.Id)
	if err != nil {
		return nil, err
	}
	metadata := &IosCredentialsMetadata{
		Identifier: ref.Identifier,
		Signing:    IosSigningSettingView{Mode: types.IosSigningAutomatic},
	}
	if setting != nil && setting.Mode == types.IosSigningCertificate {
		certificate, err := s.selectedCertificate(ctx, setting.CertificateId)
		if err != nil {
			return nil, err
		}
		metadata.Signing = IosSigningSettingView{Mode: setting.Mode, Certificate: certificate, CertificateMissing: certificate == nil}
	}
	return metadata, nil
}

// selectedCertificate returns display metadata for a selected certificate, or nil when it is missing.
func (s *IosCredentialsService) selectedCertificate(ctx context.Context, certificateId *string) (*IosCertificateMetadata, error) {
	if certificateId == nil {
		return nil, nil
	}
	certificate, err := s.repo.GetIosCertificate(ctx, *certificateId)
	if err != nil || certificate == nil {
		return nil, err
	}
	return &IosCertificateMetadata{
		Id:           certificate.Id,
		CommonName:   certificate.CommonName,
		SerialNumber: certificate.SerialNumber,
		Type:         certificate.Type,
		TeamID:       certificate.TeamID,
		ExpiresAt:    certificate.ExpiresAt.UTC().Format(time.RFC3339),
		Source:       certificate.Source,
	}, nil
}

// UpdateIosSigningSetting stores the signing choice of the identifier.
func (s *IosCredentialsService) UpdateIosSigningSetting(ctx context.Context, appId string, identifierId string, input IosSigningSettingInput) error {
	ref, err := s.resolveIosIdentifier(ctx, appId, identifierId)
	if err != nil {
		return err
	}
	setting := store.IosSigningSetting{Mode: input.Mode}
	switch input.Mode {
	case types.IosSigningAutomatic:
	case types.IosSigningCertificate:
		certificate, err := s.selectableCertificate(ctx, appId, input.CertificateId)
		if err != nil {
			return err
		}
		setting.CertificateId = &certificate.Id
	default:
		return validation.Errorf("mode", "mode must be %q or %q", types.IosSigningAutomatic, types.IosSigningCertificate)
	}
	if err := s.repo.UpsertIosSigningSetting(ctx, ref.Id, setting); err != nil {
		return err
	}
	metadata := map[string]any{"mode": input.Mode}
	if setting.CertificateId != nil {
		metadata["certificate_id"] = *setting.CertificateId
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionIosSigningUpdated,
		TargetType:    "ios_signing",
		TargetID:      ref.Id,
		TargetDisplay: ref.Identifier,
		AppID:         appId,
		Metadata:      metadata,
	})
	return nil
}

// selectableCertificate loads an unexpired distribution certificate of the pool that belongs to the
// team of the app's API key, when the team can be told.
func (s *IosCredentialsService) selectableCertificate(ctx context.Context, appId string, certificateId string) (*store.IosCertificate, error) {
	if _, err := uuid.Parse(certificateId); err != nil {
		return nil, validation.Errorf("certificateId", "certificate id must be a UUID")
	}
	certificate, err := s.repo.GetIosCertificate(ctx, certificateId)
	if err != nil {
		return nil, err
	}
	if certificate == nil {
		return nil, validation.Errorf("", "This certificate is not on xprem")
	}
	if !time.Now().Before(certificate.ExpiresAt) {
		return nil, validation.Errorf("", "This certificate expired on %s", certificate.ExpiresAt.UTC().Format(time.DateOnly))
	}
	if certificate.Type != types.IosCertificateDistribution {
		return nil, validation.Errorf("", "Only a distribution certificate can sign App Store and Ad Hoc builds")
	}
	team, err := s.appleTeamID(ctx, appId)
	if err != nil {
		return nil, err
	}
	if team != "" && team != certificate.TeamID {
		return nil, validation.Errorf("", "This certificate belongs to team %s, the App Store Connect API key to team %s", certificate.TeamID, team)
	}
	return certificate, nil
}
