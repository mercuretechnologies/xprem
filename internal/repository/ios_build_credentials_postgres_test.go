package repository_test

import (
	"context"
	"errors"
	"testing"
	"xprem/internal/database"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/ios"
	"xprem/internal/providers/appstoreconnect/appstoreconnecttest"
	"xprem/internal/repository"
	"xprem/internal/services"
	"xprem/internal/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrepareIosBuildCredentialsAutomatic(t *testing.T) {
	f := newAppStoreConnectFixture(t)
	ctx := context.Background()
	identifierId := insertIdentifier(t, f.identifiers, f.appId, types.PlatformIOS, "com.example.app")
	prepare := func(distribution types.IosDistribution) (*services.IosBuildCredentials, error) {
		return f.service.PrepareIosBuildCredentials(ctx, f.appId, identifierId, distribution)
	}

	_, err := prepare(types.IosDistributionAppStore)
	requireValidationMessage(t, err, "add an API key")
	f.saveKey(t, f.appId)
	_, err = prepare("enterprise")
	requireValidationMessage(t, err, "distribution must be")

	f.apple.CertificateLimitReached = true
	_, err = prepare(types.IosDistributionAppStore)
	requireValidationMessage(t, err, "import the .p12 of an existing certificate")
	f.apple.CertificateLimitReached = false

	credentials, err := prepare(types.IosDistributionAppStore)
	require.NoError(t, err)
	certificate, err := ios.ParseCertificate(credentials.CertificateP12, credentials.CertificatePassword)
	require.NoError(t, err)
	assert.Equal(t, types.IosCertificateDistribution, certificate.Type)
	assert.Equal(t, iosTeamID, credentials.TeamID)
	assert.NotEmpty(t, credentials.ProvisioningProfile)
	require.Len(t, f.apple.BundleIDs, 1)
	assert.Equal(t, "com.example.app", f.apple.BundleIDs[0].Identifier)
	assert.Equal(t, "xprem com example app", f.apple.BundleIDs[0].Name)
	require.Len(t, f.apple.Profiles, 1)
	assert.Equal(t, "IOS_APP_STORE", f.apple.Profiles[0].Type)

	certificateRequests := f.apple.RequestCount("POST /v1/certificates")
	again, err := prepare(types.IosDistributionAppStore)
	require.NoError(t, err)
	assert.Equal(t, credentials.ProvisioningProfile, again.ProvisioningProfile)
	reused, err := ios.ParseCertificate(again.CertificateP12, again.CertificatePassword)
	require.NoError(t, err)
	assert.Equal(t, certificate.FingerprintSHA1, reused.FingerprintSHA1)
	assert.Equal(t, certificateRequests, f.apple.RequestCount("POST /v1/certificates"), "the pool certificate is reused")
	assert.Equal(t, 1, f.apple.RequestCount("POST /v1/bundleIds"))
	assert.Equal(t, 1, f.apple.RequestCount("POST /v1/profiles"), "a current profile is reused")

	_, err = prepare(types.IosDistributionAdHoc)
	requireValidationMessage(t, err, "Register at least one iPhone")
	f.apple.Devices = []appstoreconnecttest.Device{
		{ID: "DEVICE1", UDID: "UDID-1", Platform: "IOS", DeviceClass: "IPHONE", Status: "ENABLED"},
		{ID: "DEVICE2", UDID: "UDID-2", Platform: "IOS", DeviceClass: "IPHONE", Status: "DISABLED"},
		{ID: "DEVICE3", UDID: "UDID-3", Platform: "TV_OS", DeviceClass: "APPLE_TV", Status: "ENABLED"},
	}
	adHoc, err := prepare(types.IosDistributionAdHoc)
	require.NoError(t, err)
	require.Len(t, f.apple.Profiles, 2)
	assert.Equal(t, []string{"DEVICE1"}, f.apple.Profiles[1].DeviceIDs)

	f.apple.Devices = append(f.apple.Devices, appstoreconnecttest.Device{ID: "DEVICE4", UDID: "UDID-4", Platform: "IOS", DeviceClass: "IPAD", Status: "ENABLED"})
	refreshed, err := prepare(types.IosDistributionAdHoc)
	require.NoError(t, err)
	assert.NotEqual(t, adHoc.ProvisioningProfile, refreshed.ProvisioningProfile)
	require.Len(t, f.apple.Profiles, 2, "the outdated profile is replaced")
	assert.Equal(t, []string{"DEVICE1", "DEVICE4"}, f.apple.Profiles[1].DeviceIDs)

	f.apple.Profiles[0].State = "INVALID"
	_, err = prepare(types.IosDistributionAppStore)
	require.NoError(t, err)
	assert.Equal(t, 4, f.apple.RequestCount("POST /v1/profiles"), "an invalid profile is replaced")
}

func TestPrepareIosBuildCredentialsSelectedCertificate(t *testing.T) {
	f := newAppStoreConnectFixture(t)
	ctx := context.Background()
	identifierId := insertIdentifier(t, f.identifiers, f.appId, types.PlatformIOS, "com.example.app")
	f.saveKey(t, f.appId)

	credentials, err := f.service.PrepareIosBuildCredentials(ctx, f.appId, identifierId, types.IosDistributionAppStore)
	require.NoError(t, err)
	created, err := ios.ParseCertificate(credentials.CertificateP12, credentials.CertificatePassword)
	require.NoError(t, err)
	var certificateId string
	require.NoError(t, f.pool.QueryRow(ctx, "SELECT id FROM ios_certificates WHERE fingerprint_sha1 = $1", created.FingerprintSHA1).Scan(&certificateId))
	require.NoError(t, f.service.UpdateIosSigningSetting(ctx, f.appId, identifierId, services.IosSigningSettingInput{Mode: types.IosSigningCertificate, CertificateId: certificateId}))

	_, err = f.service.PrepareIosBuildCredentials(ctx, f.appId, identifierId, types.IosDistributionAppStore)
	require.NoError(t, err)
	assert.Equal(t, 1, f.apple.RequestCount("POST /v1/certificates"))

	_, err = f.pool.Exec(ctx, "UPDATE ios_certificates SET fingerprint_sha1 = 'REVOKED-' || id WHERE id = $1", certificateId)
	require.NoError(t, err)
	_, err = f.service.PrepareIosBuildCredentials(ctx, f.appId, identifierId, types.IosDistributionAppStore)
	requireValidationMessage(t, err, "was revoked at Apple")
	assert.Equal(t, 1, f.apple.RequestCount("POST /v1/certificates"), "a selected certificate is never replaced silently")
}

type failingSaveCertificateRepo struct {
	services.IosCredentialsRepository
}

func (r *failingSaveCertificateRepo) SaveIosCertificate(context.Context, repository.IosCertificate, repository.SealIosCertificateFunc) (string, error) {
	return "", errors.New("database is down")
}

func TestPrepareIosBuildCredentialsRevokesAnUnstoredCertificate(t *testing.T) {
	f := newAppStoreConnectFixture(t)
	ctx := context.Background()
	identifierId := insertIdentifier(t, f.identifiers, f.appId, types.PlatformIOS, "com.example.app")
	f.saveKey(t, f.appId)
	repo := repository.NewPostgresIosCredentialsRepository(&database.Engine{Queries: pgdb.New(f.pool), DB: f.pool})
	service := services.NewIosCredentialsService(&failingSaveCertificateRepo{IosCredentialsRepository: repo}, f.identifiers)
	service.SetAppStoreConnectBaseURL(f.apple.BaseURL)

	_, err := service.PrepareIosBuildCredentials(ctx, f.appId, identifierId, types.IosDistributionAppStore)
	require.ErrorContains(t, err, "database is down")
	assert.Equal(t, 1, f.apple.RequestCount("POST /v1/certificates"))
	assert.Empty(t, f.apple.Profiles)
	assert.Equal(t, 1, f.apple.RequestCount("DELETE /v1/certificates/CERT1"), "the certificate Apple issued is revoked")
}

func TestPrepareIosBuildCredentialsCreatesOneCertificateForSimultaneousBuilds(t *testing.T) {
	f := newAppStoreConnectFixture(t)
	ctx := context.Background()
	f.saveKey(t, f.appId)
	identifiers := []string{
		insertIdentifier(t, f.identifiers, f.appId, types.PlatformIOS, "com.example.one"),
		insertIdentifier(t, f.identifiers, f.appId, types.PlatformIOS, "com.example.two"),
		insertIdentifier(t, f.identifiers, f.appId, types.PlatformIOS, "com.example.three"),
	}
	errs := make(chan error, len(identifiers))
	for _, identifierId := range identifiers {
		go func() {
			_, err := f.service.PrepareIosBuildCredentials(ctx, f.appId, identifierId, types.IosDistributionAppStore)
			errs <- err
		}()
	}
	for range identifiers {
		require.NoError(t, <-errs)
	}
	assert.Equal(t, 1, f.apple.RequestCount("POST /v1/certificates"))
}

func TestPrepareIosBuildCredentialsKeepsTheCertificateOfADisconnectedBuild(t *testing.T) {
	f := newAppStoreConnectFixture(t)
	identifierId := insertIdentifier(t, f.identifiers, f.appId, types.PlatformIOS, "com.example.app")
	f.saveKey(t, f.appId)
	disconnected, disconnect := context.WithCancel(context.Background())
	f.apple.AfterCertificateCreated = disconnect

	_, err := f.service.PrepareIosBuildCredentials(disconnected, f.appId, identifierId, types.IosDistributionAppStore)
	require.ErrorContains(t, err, "context canceled", "the request stops at its next Apple call")

	f.apple.AfterCertificateCreated = nil
	_, err = f.service.PrepareIosBuildCredentials(context.Background(), f.appId, identifierId, types.IosDistributionAppStore)
	require.NoError(t, err)
	assert.Equal(t, 1, f.apple.RequestCount("POST /v1/certificates"), "the retry signs with the certificate the first request created")
}
