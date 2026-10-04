package services

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"slices"
	"time"
	"xprem/internal/auditlog"
	"xprem/internal/crypto"
	"xprem/internal/ios"
	"xprem/internal/keyStore"
	"xprem/internal/providers/appstoreconnect"
	"xprem/internal/repository"
	"xprem/internal/types"
	"xprem/internal/validation"

	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

const iosCertificateCreationTimeout = 3 * time.Minute

// IosBuildCredentials is what a CLI needs to sign one iOS build.
type IosBuildCredentials struct {
	CertificateP12      []byte
	CertificatePassword string
	ProvisioningProfile []byte
	TeamID              string
}

// PrepareIosBuildCredentials returns the certificate and provisioning profile that sign a build of the
// identifier, creating at Apple the bundle id, certificate and profile that are missing.
func (s *IosCredentialsService) PrepareIosBuildCredentials(ctx context.Context, appId string, identifierId string, distribution types.IosDistribution) (*IosBuildCredentials, error) {
	ref, err := s.resolveIosIdentifier(ctx, appId, identifierId)
	if err != nil {
		return nil, err
	}
	profileType, err := iosProfileType(distribution)
	if err != nil {
		return nil, err
	}
	client, err := s.appStoreConnectClient(ctx, appId)
	if err != nil {
		return nil, err
	}
	certificate, appleCertificateId, err := s.signingCertificate(ctx, client, appId, ref.Id)
	if err != nil {
		return nil, err
	}
	bundleID, err := client.FindBundleID(ctx, ref.Identifier)
	if err == nil && bundleID == nil {
		var created appstoreconnect.BundleID
		created, err = client.CreateBundleID(ctx, ref.Identifier, bundleIDName(ref.Identifier))
		bundleID = &created
	}
	if err != nil {
		return nil, appStoreConnectError(err)
	}
	var deviceIDs []string
	if distribution == types.IosDistributionAdHoc {
		if deviceIDs, err = adHocDeviceIDs(ctx, client); err != nil {
			return nil, err
		}
	}
	profile, err := ensureProfile(ctx, client, appstoreconnect.ProfileInput{
		Name:           "xprem managed - " + ref.Identifier + " - " + string(distribution),
		Type:           profileType,
		BundleID:       bundleID.ID,
		CertificateIDs: []string{appleCertificateId},
		DeviceIDs:      deviceIDs,
	})
	if err != nil {
		return nil, appStoreConnectError(err)
	}
	p12, password, err := s.unsealIosCertificate(ctx, certificate.Id)
	if err != nil {
		return nil, err
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionIosCredentialsDownloaded,
		TargetType:    "ios_credentials",
		TargetID:      ref.Id,
		TargetDisplay: ref.Identifier,
		AppID:         appId,
		Metadata:      map[string]any{"distribution": distribution, "certificate_id": certificate.Id},
	})
	return &IosBuildCredentials{CertificateP12: p12, CertificatePassword: password, ProvisioningProfile: profile.Content, TeamID: certificate.TeamID}, nil
}

func iosProfileType(distribution types.IosDistribution) (string, error) {
	switch distribution {
	case types.IosDistributionAppStore:
		return appstoreconnect.ProfileTypeAppStore, nil
	case types.IosDistributionAdHoc:
		return appstoreconnect.ProfileTypeAdHoc, nil
	}
	return "", validation.Errorf("distribution", "distribution must be %q or %q", types.IosDistributionAppStore, types.IosDistributionAdHoc)
}

var bundleIDNameSeparators = regexp.MustCompile(`[^A-Za-z0-9]+`)

// bundleIDName is the display name of a bundle id xprem registers; Apple refuses punctuation in it.
func bundleIDName(identifier string) string {
	return "xprem " + bundleIDNameSeparators.ReplaceAllString(identifier, " ")
}

// signingCertificate returns the pool certificate that signs the identifier with its Apple id: the
// selected one, or in automatic mode the pool certificate of the team that expires last, created when there is none.
func (s *IosCredentialsService) signingCertificate(ctx context.Context, client *appstoreconnect.Client, appId string, identifierId string) (*repository.IosCertificate, string, error) {
	setting, err := s.repo.GetIosSigningSetting(ctx, identifierId)
	if err != nil {
		return nil, "", err
	}
	if setting == nil || setting.Mode != types.IosSigningCertificate {
		return s.automaticCertificate(ctx, client, appId)
	}
	appleIds, err := appleCertificateIds(ctx, client)
	if err != nil {
		return nil, "", err
	}
	var selected *repository.IosCertificate
	if setting.CertificateId != nil {
		if selected, err = s.repo.GetIosCertificate(ctx, *setting.CertificateId); err != nil {
			return nil, "", err
		}
	}
	if selected == nil {
		return nil, "", validation.Errorf("", "The selected certificate no longer exists: select another one or switch to automatic management")
	}
	if !time.Now().Before(selected.ExpiresAt) {
		return nil, "", validation.Errorf("", "The selected certificate expired on %s: select another one or switch to automatic management", selected.ExpiresAt.UTC().Format(time.DateOnly))
	}
	appleId, listed := appleIds[selected.FingerprintSHA1]
	if !listed {
		return nil, "", validation.Errorf("", "The selected certificate was revoked at Apple: select another one or switch to automatic management")
	}
	return selected, appleId, nil
}

// automaticCertificate returns the usable pool certificate that expires last, creating one when
// there is none. Builds that find none take turns, and look again once it is theirs.
func (s *IosCredentialsService) automaticCertificate(ctx context.Context, client *appstoreconnect.Client, appId string) (*repository.IosCertificate, string, error) {
	certificate, appleId, err := s.latestPoolCertificate(ctx, client)
	if err != nil || certificate != nil {
		return certificate, appleId, err
	}
	if s.lockCertificateCreation != nil {
		release, err := s.lockCertificateCreation(ctx)
		if err != nil {
			return nil, "", err
		}
		defer release()
		certificate, appleId, err = s.latestPoolCertificate(ctx, client)
		if err != nil || certificate != nil {
			return certificate, appleId, err
		}
	}
	return s.createIosCertificate(ctx, client, appId)
}

// latestPoolCertificate returns the pool certificate Apple still lists for the team that expires last, or nil.
func (s *IosCredentialsService) latestPoolCertificate(ctx context.Context, client *appstoreconnect.Client) (*repository.IosCertificate, string, error) {
	appleIds, err := appleCertificateIds(ctx, client)
	if err != nil {
		return nil, "", err
	}
	pool, err := s.repo.ListIosCertificates(ctx)
	if err != nil {
		return nil, "", err
	}
	var latest *repository.IosCertificate
	for i, certificate := range pool {
		_, listed := appleIds[certificate.FingerprintSHA1]
		if listed && time.Now().Before(certificate.ExpiresAt) && (latest == nil || certificate.ExpiresAt.After(latest.ExpiresAt)) {
			latest = &pool[i]
		}
	}
	if latest == nil {
		return nil, "", nil
	}
	return latest, appleIds[latest.FingerprintSHA1], nil
}

// appleCertificateIds maps the fingerprint of each distribution certificate of the team to its Apple id.
func appleCertificateIds(ctx context.Context, client *appstoreconnect.Client) (map[string]string, error) {
	appleCertificates, err := client.ListDistributionCertificates(ctx)
	if err != nil {
		return nil, appStoreConnectError(err)
	}
	appleIds := map[string]string{}
	for _, appleCertificate := range appleCertificates {
		appleIds[ios.Fingerprint(appleCertificate.DER)] = appleCertificate.ID
	}
	return appleIds, nil
}

// createIosCertificate has Apple issue a distribution certificate for a new private key and stores it
// in the pool; a certificate that cannot be stored is revoked.
func (s *IosCredentialsService) createIosCertificate(ctx context.Context, client *appstoreconnect.Client, appId string) (*repository.IosCertificate, string, error) {
	// A certificate Apple issues must be stored or revoked even if the CLI disconnected meanwhile.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), iosCertificateCreationTimeout)
	defer cancel()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, "", err
	}
	request, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "xprem"}}, privateKey)
	if err != nil {
		return nil, "", err
	}
	created, err := client.CreateDistributionCertificate(ctx, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: request})))
	if err != nil {
		var apiErr *appstoreconnect.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusConflict {
			return nil, "", validation.Errorf("", "Apple refused to create a distribution certificate: %s If the team reached its certificate limit, import the .p12 of an existing certificate or revoke one in the Apple developer portal", apiErr.Detail)
		}
		return nil, "", appStoreConnectError(err)
	}
	certificate, err := s.storeCreatedCertificate(ctx, appId, privateKey, created.DER)
	if err != nil {
		if revokeErr := client.RevokeCertificate(ctx, created.ID); revokeErr != nil {
			log.Printf("ios distribution certificate %s was created at Apple but not stored, and revoking it failed: %v", created.ID, revokeErr)
		}
		return nil, "", err
	}
	return certificate, created.ID, nil
}

func (s *IosCredentialsService) storeCreatedCertificate(ctx context.Context, appId string, privateKey *rsa.PrivateKey, der []byte) (*repository.IosCertificate, error) {
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("apple distribution certificate: %w", err)
	}
	secret, err := randomSecret()
	if err != nil {
		return nil, err
	}
	password := hex.EncodeToString(secret)
	p12, err := pkcs12.LegacyDES.Encode(privateKey, leaf, nil, password)
	if err != nil {
		return nil, fmt.Errorf("encode distribution certificate: %w", err)
	}
	parsed, err := ios.ParseCertificate(p12, password)
	if err != nil {
		return nil, err
	}
	certificateId, err := s.saveIosCertificate(ctx, appId, parsed, p12, password)
	if err != nil {
		return nil, err
	}
	return &repository.IosCertificate{Id: certificateId, FingerprintSHA1: parsed.FingerprintSHA1, TeamID: parsed.TeamID, ExpiresAt: parsed.ExpiresAt}, nil
}

var adHocDeviceClasses = []string{"IPHONE", "IPAD", "IPOD"}

// adHocDeviceIDs lists the Apple ids of the enabled devices an Ad Hoc profile can include.
func adHocDeviceIDs(ctx context.Context, client *appstoreconnect.Client) ([]string, error) {
	devices, err := client.ListIOSDevices(ctx)
	if err != nil {
		return nil, appStoreConnectError(err)
	}
	var ids []string
	for _, device := range devices {
		if device.Status == appleDeviceEnabled && slices.Contains(adHocDeviceClasses, device.DeviceClass) {
			ids = append(ids, device.ID)
		}
	}
	if len(ids) == 0 {
		return nil, validation.Errorf("", "Register at least one iPhone before building for Ad Hoc distribution")
	}
	return ids, nil
}

// ensureProfile returns the team's profile named input.Name, replacing it when it is invalid or no
// longer holds the certificate and exactly the devices of input.
func ensureProfile(ctx context.Context, client *appstoreconnect.Client, input appstoreconnect.ProfileInput) (*appstoreconnect.Profile, error) {
	existing, err := client.FindProfile(ctx, input.Name)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		current, err := profileMatches(ctx, client, existing, input)
		if err != nil {
			return nil, err
		}
		if current {
			return existing, nil
		}
		if err := client.DeleteProfile(ctx, existing.ID); err != nil {
			return nil, err
		}
	}
	created, err := client.CreateProfile(ctx, input)
	if err != nil {
		return nil, err
	}
	if len(created.Content) == 0 {
		return nil, fmt.Errorf("%w: profile %s has no content", appstoreconnect.ErrUnavailable, created.ID)
	}
	return &created, nil
}

func profileMatches(ctx context.Context, client *appstoreconnect.Client, profile *appstoreconnect.Profile, input appstoreconnect.ProfileInput) (bool, error) {
	if profile.State != appstoreconnect.ProfileStateActive || profile.Type != input.Type || len(profile.Content) == 0 {
		return false, nil
	}
	certificateIDs, err := client.ProfileCertificateIDs(ctx, profile.ID)
	if err != nil || !slices.Contains(certificateIDs, input.CertificateIDs[0]) {
		return false, err
	}
	deviceIDs, err := client.ProfileDeviceIDs(ctx, profile.ID)
	if err != nil {
		return false, err
	}
	wanted := slices.Clone(input.DeviceIDs)
	slices.Sort(wanted)
	slices.Sort(deviceIDs)
	return slices.Equal(wanted, deviceIDs), nil
}

func (s *IosCredentialsService) unsealIosCertificate(ctx context.Context, certificateId string) ([]byte, string, error) {
	file, err := s.repo.GetIosCertificateFile(ctx, certificateId)
	if err != nil {
		return nil, "", err
	}
	if file == nil {
		return nil, "", &repository.ErrResourceNotFound{Resource: "ios certificate", Identifier: certificateId}
	}
	masterKey := []byte(keyStore.ReadDBKeysMasterKey())
	p12, err := crypto.UnsealAESGCM(file.SealedCertificate, masterKey, iosCertificateAAD(certificateId, "certificate"))
	if err != nil {
		return nil, "", fmt.Errorf("failed to unseal certificate: %w", err)
	}
	password, err := crypto.UnsealAESGCM(file.SealedPassword, masterKey, iosCertificateAAD(certificateId, "certificate_password"))
	if err != nil {
		return nil, "", fmt.Errorf("failed to unseal certificate password: %w", err)
	}
	if p12, err = ios.KeychainP12(p12, string(password)); err != nil {
		return nil, "", fmt.Errorf("failed to re-encode certificate: %w", err)
	}
	return p12, string(password), nil
}
