package services

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"xprem/internal/appstoreconnect"
	"xprem/internal/auditlog"
	"xprem/internal/crypto"
	"xprem/internal/ios"
	"xprem/internal/keyStore"
	"xprem/internal/store"
	"xprem/internal/types"
	"xprem/internal/validation"

	"github.com/google/uuid"
)

const maxAppStoreConnectPrivateKeyBytes = 8 * 1024

var appStoreConnectKeyIDPattern = regexp.MustCompile(`^[A-Z0-9]{10}$`)

// AppStoreConnectApiKeyInput is the team API key submitted from the dashboard.
type AppStoreConnectApiKeyInput struct {
	KeyID      string
	IssuerID   string
	PrivateKey string
}

// AppStoreConnectApiKeyMetadata is the non-secret projection of a stored API key.
type AppStoreConnectApiKeyMetadata struct {
	KeyID     string `json:"keyId"`
	IssuerID  string `json:"issuerId"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

// IosSigningCertificate is an unexpired distribution certificate of the Apple team; only those
// whose private key xprem holds are selectable.
type IosSigningCertificate struct {
	AppleId            string  `json:"appleId"`
	Name               string  `json:"name"`
	SerialNumber       string  `json:"serialNumber"`
	ExpiresAt          string  `json:"expiresAt"`
	FingerprintSHA1    string  `json:"fingerprintSha1"`
	XpremCertificateId *string `json:"xpremCertificateId"`
	Selectable         bool    `json:"selectable"`
}

// appStoreConnectKeyAAD binds encrypted API key material to its app and purpose.
func appStoreConnectKeyAAD(appId string) []byte {
	return []byte(appId + "|app_store_connect_api_keys|private_key")
}

// SaveAppStoreConnectApiKey checks the key against Apple before replacing the app's stored key.
func (s *IosCredentialsService) SaveAppStoreConnectApiKey(ctx context.Context, appId string, input AppStoreConnectApiKeyInput) error {
	appId, err := s.canonicalAppID(appId)
	if err != nil {
		return err
	}
	if !appStoreConnectKeyIDPattern.MatchString(input.KeyID) {
		return validation.Errorf("keyId", "Key ID must be 10 uppercase letters or digits")
	}
	if _, err := uuid.Parse(input.IssuerID); err != nil || len(input.IssuerID) != 36 {
		return validation.Errorf("issuerId", "Issuer ID must be a UUID")
	}
	if len(input.PrivateKey) > maxAppStoreConnectPrivateKeyBytes {
		return validation.Errorf("privateKey", "private key exceeds the %d KB limit", maxAppStoreConnectPrivateKeyBytes/1024)
	}
	privateKey, err := appstoreconnect.ParsePrivateKey(input.PrivateKey)
	if err != nil {
		return validation.Errorf("privateKey", "private key must be the .p8 file of an App Store Connect API key")
	}
	client := appstoreconnect.NewClient(s.appStoreConnectBaseURL, input.KeyID, input.IssuerID, privateKey)
	if err := client.VerifyAccess(ctx); err != nil {
		return appStoreConnectError(err)
	}
	sealed, err := crypto.SealAESGCM([]byte(input.PrivateKey), []byte(keyStore.ReadDBKeysMasterKey()), appStoreConnectKeyAAD(appId))
	if err != nil {
		return fmt.Errorf("failed to seal app store connect private key: %w", err)
	}
	err = s.repo.UpsertAppStoreConnectApiKey(ctx, appId, store.SealedAppStoreConnectApiKey{
		KeyID:            input.KeyID,
		IssuerID:         input.IssuerID,
		SealedPrivateKey: sealed,
	})
	if err != nil {
		return err
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionAppStoreConnectApiKeySaved,
		TargetType:    "app_store_connect_api_key",
		TargetID:      appId,
		TargetDisplay: input.KeyID,
		AppID:         appId,
		Metadata:      map[string]any{"key_id": input.KeyID, "issuer_id": input.IssuerID},
	})
	return nil
}

// GetAppStoreConnectApiKeyMetadata returns (nil, nil) when the app has no key.
func (s *IosCredentialsService) GetAppStoreConnectApiKeyMetadata(ctx context.Context, appId string) (*AppStoreConnectApiKeyMetadata, error) {
	appId, err := s.canonicalAppID(appId)
	if err != nil {
		return nil, err
	}
	key, err := s.repo.GetAppStoreConnectApiKey(ctx, appId)
	if err != nil || key == nil {
		return nil, err
	}
	return &AppStoreConnectApiKeyMetadata{
		KeyID:     key.KeyID,
		IssuerID:  key.IssuerID,
		CreatedAt: key.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: key.UpdatedAt.UTC().Format(time.RFC3339),
	}, nil
}

// DeleteAppStoreConnectApiKey deletes the app team credentials and records the management event.
func (s *IosCredentialsService) DeleteAppStoreConnectApiKey(ctx context.Context, appId string) error {
	appId, err := s.canonicalAppID(appId)
	if err != nil {
		return err
	}
	if err := s.repo.DeleteAppStoreConnectApiKey(ctx, appId); err != nil {
		return err
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:     auditlog.ActionAppStoreConnectApiKeyDeleted,
		TargetType: "app_store_connect_api_key",
		TargetID:   appId,
		AppID:      appId,
	})
	return nil
}

// ListIosSigningCertificates returns the team's unexpired distribution certificates, latest expiry first.
func (s *IosCredentialsService) ListIosSigningCertificates(ctx context.Context, appId string, identifierId string) ([]IosSigningCertificate, error) {
	if _, err := s.resolveIosIdentifier(ctx, appId, identifierId); err != nil {
		return nil, err
	}
	client, err := s.appStoreConnectClient(ctx, appId)
	if err != nil {
		return nil, err
	}
	appleCertificates, err := client.ListDistributionCertificates(ctx)
	if err != nil {
		return nil, appStoreConnectError(err)
	}
	pool, err := s.repo.ListIosCertificates(ctx)
	if err != nil {
		return nil, err
	}
	certificates := []IosSigningCertificate{}
	for _, appleCertificate := range appleCertificates {
		parsed, err := x509.ParseCertificate(appleCertificate.DER)
		if err != nil || !time.Now().Before(parsed.NotAfter) {
			continue
		}
		certificate := IosSigningCertificate{
			AppleId:         appleCertificate.ID,
			Name:            parsed.Subject.CommonName,
			SerialNumber:    strings.ToUpper(parsed.SerialNumber.Text(16)),
			ExpiresAt:       parsed.NotAfter.UTC().Format(time.RFC3339),
			FingerprintSHA1: ios.Fingerprint(appleCertificate.DER),
		}
		for _, stored := range pool {
			if stored.FingerprintSHA1 == certificate.FingerprintSHA1 {
				certificate.XpremCertificateId = &stored.Id
				certificate.Selectable = true
			}
		}
		certificates = append(certificates, certificate)
	}
	// RFC3339 UTC timestamps sort chronologically as strings.
	slices.SortStableFunc(certificates, func(a, b IosSigningCertificate) int {
		return strings.Compare(b.ExpiresAt, a.ExpiresAt)
	})
	return certificates, nil
}

const maxIosCertificateBytes = 64 * 1024

// IosCertificateImportInput is the .p12 of an Apple certificate submitted to unlock it.
type IosCertificateImportInput struct {
	FingerprintSHA1      string
	CertificateP12Base64 string
	CertificatePassword  string
}

// iosCertificateAAD binds an encrypted certificate field to its certificate ID and purpose.
func iosCertificateAAD(certificateId string, field string) []byte {
	return []byte(certificateId + "|ios_certificates|" + field)
}

// decodeIosUpload decodes a size-limited base64 upload and returns a field-specific validation error.
func decodeIosUpload(field string, label string, encoded string, maxBytes int) ([]byte, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, validation.Errorf(field, "%s is not valid base64", label)
	}
	if len(data) == 0 {
		return nil, validation.Errorf(field, "%s is empty", label)
	}
	if len(data) > maxBytes {
		return nil, validation.Errorf(field, "%s exceeds the %d KB limit", label, maxBytes/1024)
	}
	return data, nil
}

// ImportIosCertificate stores the .p12 of one of the team's distribution certificates in the pool, which
// makes it selectable, and returns the refreshed certificate listing. The identifier's signing setting is unchanged.
func (s *IosCredentialsService) ImportIosCertificate(ctx context.Context, appId string, identifierId string, input IosCertificateImportInput) ([]IosSigningCertificate, error) {
	if _, err := s.resolveIosIdentifier(ctx, appId, identifierId); err != nil {
		return nil, err
	}
	p12, err := decodeIosUpload("certificateP12", "certificate", input.CertificateP12Base64, maxIosCertificateBytes)
	if err != nil {
		return nil, err
	}
	certificate, err := ios.ParseCertificate(p12, input.CertificatePassword)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(certificate.FingerprintSHA1, input.FingerprintSHA1) {
		return nil, validation.Errorf("", "This .p12 contains a different certificate than the one you selected")
	}
	if certificate.Type != types.IosCertificateDistribution {
		return nil, validation.Errorf("", "Only a distribution certificate can sign App Store and Ad Hoc builds")
	}
	certificates, err := s.ListIosSigningCertificates(ctx, appId, identifierId)
	if err != nil {
		return nil, err
	}
	index := slices.IndexFunc(certificates, func(listed IosSigningCertificate) bool {
		return listed.FingerprintSHA1 == certificate.FingerprintSHA1
	})
	if index < 0 {
		return nil, validation.Errorf("", "This certificate is not an unexpired distribution certificate of the Apple team of this app's App Store Connect API key")
	}
	masterKey := []byte(keyStore.ReadDBKeysMasterKey())
	certificateId, err := s.repo.SaveIosCertificate(ctx, store.IosCertificate{
		CommonName:      certificate.CommonName,
		SerialNumber:    certificate.SerialNumber,
		FingerprintSHA1: certificate.FingerprintSHA1,
		Type:            certificate.Type,
		TeamID:          certificate.TeamID,
		ExpiresAt:       certificate.ExpiresAt,
		Source:          types.IosCertificateUploaded,
	}, func(certificateId string) (string, string, error) {
		sealedCertificate, err := crypto.SealAESGCM(p12, masterKey, iosCertificateAAD(certificateId, "certificate"))
		if err != nil {
			return "", "", fmt.Errorf("failed to seal certificate: %w", err)
		}
		sealedPassword, err := crypto.SealAESGCM([]byte(input.CertificatePassword), masterKey, iosCertificateAAD(certificateId, "certificate_password"))
		if err != nil {
			return "", "", fmt.Errorf("failed to seal certificate password: %w", err)
		}
		return sealedCertificate, sealedPassword, nil
	})
	if err != nil {
		return nil, err
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionIosCertificateSaved,
		TargetType:    "ios_certificate",
		TargetID:      certificateId,
		TargetDisplay: certificate.CommonName,
		AppID:         appId,
		Metadata:      map[string]any{"fingerprint_sha1": certificate.FingerprintSHA1, "common_name": certificate.CommonName},
	})
	certificates[index].XpremCertificateId = &certificateId
	certificates[index].Selectable = true
	return certificates, nil
}

// appleTeamID reads the team of the app's API key from its distribution certificates; "" when the app
// has no key or the team lists none.
func (s *IosCredentialsService) appleTeamID(ctx context.Context, appId string) (string, error) {
	appId, err := s.canonicalAppID(appId)
	if err != nil {
		return "", err
	}
	key, err := s.repo.GetAppStoreConnectApiKey(ctx, appId)
	if err != nil || key == nil {
		return "", err
	}
	client, err := s.appStoreConnectClient(ctx, appId)
	if err != nil {
		return "", err
	}
	certificates, err := client.ListDistributionCertificates(ctx)
	if err != nil {
		return "", appStoreConnectError(err)
	}
	for _, certificate := range certificates {
		parsed, err := x509.ParseCertificate(certificate.DER)
		if err == nil && len(parsed.Subject.OrganizationalUnit) > 0 {
			return parsed.Subject.OrganizationalUnit[0], nil
		}
	}
	return "", nil
}

// appStoreConnectClient decrypts the app API key and constructs its authenticated Apple client.
func (s *IosCredentialsService) appStoreConnectClient(ctx context.Context, appId string) (*appstoreconnect.Client, error) {
	appId, err := s.canonicalAppID(appId)
	if err != nil {
		return nil, err
	}
	key, err := s.repo.GetAppStoreConnectApiKey(ctx, appId)
	if err != nil {
		return nil, err
	}
	if key == nil {
		return nil, validation.Errorf("", "Connect App Store Connect first: add an API key for this app")
	}
	pemText, err := crypto.UnsealAESGCM(key.SealedPrivateKey, []byte(keyStore.ReadDBKeysMasterKey()), appStoreConnectKeyAAD(appId))
	if err != nil {
		return nil, fmt.Errorf("failed to unseal app store connect private key: %w", err)
	}
	privateKey, err := appstoreconnect.ParsePrivateKey(string(pemText))
	if err != nil {
		return nil, fmt.Errorf("stored app store connect private key: %w", err)
	}
	return appstoreconnect.NewClient(s.appStoreConnectBaseURL, key.KeyID, key.IssuerID, privateKey), nil
}

// appStoreConnectError turns Apple's refusals into validation errors; outages are returned unchanged.
func appStoreConnectError(err error) error {
	var apiErr *appstoreconnect.APIError
	if !errors.As(err, &apiErr) {
		return err
	}
	switch apiErr.Status {
	case http.StatusUnauthorized:
		return validation.Errorf("", "Apple rejected this API key: check the Key ID, Issuer ID and .p8 file")
	case http.StatusForbidden:
		return validation.Errorf("", "This API key cannot manage certificates and profiles: create it with the Admin role")
	}
	return validation.Errorf("", "App Store Connect refused the request: %s", apiErr.Detail)
}
