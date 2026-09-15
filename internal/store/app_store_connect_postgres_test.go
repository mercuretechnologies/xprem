package store_test

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"time"
	"xprem/internal/appstoreconnect"
	"xprem/internal/appstoreconnect/appstoreconnecttest"
	"xprem/internal/auditlog"
	"xprem/internal/crypto"
	"xprem/internal/ios"
	"xprem/internal/ios/iostest"
	"xprem/internal/services"
	"xprem/internal/store"
	"xprem/internal/types"
	"xprem/internal/validation"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type appStoreConnectFixture struct {
	*iosFixture
	apple *appstoreconnecttest.Server
	appId string
}

func newAppStoreConnectFixture(t *testing.T) *appStoreConnectFixture {
	t.Helper()
	f := &appStoreConnectFixture{iosFixture: newIosFixture(t), apple: appstoreconnecttest.New(t)}
	f.appId = insertBareApp(t, f.pool)
	f.service.SetAppStoreConnectBaseURL(f.apple.BaseURL)
	return f
}

func (f *appStoreConnectFixture) validKey() services.AppStoreConnectApiKeyInput {
	return services.AppStoreConnectApiKeyInput{
		KeyID:      appstoreconnecttest.KeyID,
		IssuerID:   appstoreconnecttest.IssuerID,
		PrivateKey: f.apple.PrivateKeyPEM,
	}
}

func (f *appStoreConnectFixture) saveKey(t *testing.T, appId string) {
	t.Helper()
	require.NoError(t, f.service.SaveAppStoreConnectApiKey(context.Background(), appId, f.validKey()))
}

func TestAppStoreConnectApiKeyValidation(t *testing.T) {
	f := newAppStoreConnectFixture(t)
	ctx := context.Background()

	metadata, err := f.service.GetAppStoreConnectApiKeyMetadata(ctx, f.appId)
	require.NoError(t, err)
	assert.Nil(t, metadata)

	for name, mutate := range map[string]func(*services.AppStoreConnectApiKeyInput){
		"lowercase key id":  func(i *services.AppStoreConnectApiKeyInput) { i.KeyID = "abc123defg" },
		"short key id":      func(i *services.AppStoreConnectApiKeyInput) { i.KeyID = "ABC123" },
		"issuer not a uuid": func(i *services.AppStoreConnectApiKeyInput) { i.IssuerID = "issuer" },
		"garbage key":       func(i *services.AppStoreConnectApiKeyInput) { i.PrivateKey = "not a key" },
		"oversized key":     func(i *services.AppStoreConnectApiKeyInput) { i.PrivateKey = strings.Repeat("x", 8*1024+1) },
	} {
		input := f.validKey()
		mutate(&input)
		var valErr *validation.Error
		assert.ErrorAs(t, f.service.SaveAppStoreConnectApiKey(ctx, f.appId, input), &valErr, name)
	}
	assert.Zero(t, f.apple.RequestCount("GET /v1/bundleIds"), "malformed keys never reach Apple")

	rejected := f.validKey()
	rejected.KeyID = "ZZZ999ZZZZ"
	requireValidationMessage(t, f.service.SaveAppStoreConnectApiKey(ctx, f.appId, rejected), "Apple rejected this API key: check the Key ID, Issuer ID and .p8 file")
	f.apple.Status = http.StatusForbidden
	requireValidationMessage(t, f.service.SaveAppStoreConnectApiKey(ctx, f.appId, f.validKey()), "This API key cannot manage certificates and profiles: create it with the Admin role")
	f.apple.Status = http.StatusServiceUnavailable
	assert.ErrorIs(t, f.service.SaveAppStoreConnectApiKey(ctx, f.appId, f.validKey()), appstoreconnect.ErrUnavailable)
	metadata, err = f.service.GetAppStoreConnectApiKeyMetadata(ctx, f.appId)
	require.NoError(t, err)
	assert.Nil(t, metadata, "a key Apple did not accept is not stored")

	f.apple.Status = 0
	require.NoError(t, f.service.SaveAppStoreConnectApiKey(ctx, strings.ToUpper(f.appId), f.validKey()))
	metadata, err = f.service.GetAppStoreConnectApiKeyMetadata(ctx, f.appId)
	require.NoError(t, err)
	require.NotNil(t, metadata)
	assert.Equal(t, appstoreconnecttest.KeyID, metadata.KeyID)
	assert.Equal(t, appstoreconnecttest.IssuerID, metadata.IssuerID)
	var sealed string
	require.NoError(t, f.pool.QueryRow(ctx, "SELECT sealed_private_key FROM app_store_connect_api_keys WHERE app_id = $1", f.appId).Scan(&sealed))
	plain, err := crypto.UnsealAESGCM(sealed, []byte(iosMasterKey), []byte(f.appId+"|app_store_connect_api_keys|private_key"))
	require.NoError(t, err)
	assert.Equal(t, f.apple.PrivateKeyPEM, string(plain))

	require.NoError(t, f.service.DeleteAppStoreConnectApiKey(ctx, f.appId))
	notFoundErr := (*store.ErrResourceNotFound)(nil)
	assert.ErrorAs(t, f.service.DeleteAppStoreConnectApiKey(ctx, f.appId), &notFoundErr)
	assert.Equal(t, []auditlog.Action{auditlog.ActionAppStoreConnectApiKeySaved, auditlog.ActionAppStoreConnectApiKeyDeleted}, f.actions)
}

func TestListIosSigningCertificates(t *testing.T) {
	f := newAppStoreConnectFixture(t)
	ctx := context.Background()
	identifierId := insertIdentifier(t, f.identifiers, f.appId, types.PlatformIOS, "com.example.app")
	_, err := f.service.ListIosSigningCertificates(ctx, f.appId, identifierId)
	requireValidationMessage(t, err, "add an API key")
	f.saveKey(t, f.appId)

	held := newDistributionIdentity(time.Now().Add(365 * 24 * time.Hour))
	outside := newDistributionIdentity(time.Now().Add(2 * 365 * 24 * time.Hour))
	expired := newDistributionIdentity(time.Now().Add(-time.Hour))
	heldId := f.insertPoolCertificate(t, poolCertificate{identity: held, certificateType: types.IosCertificateDistribution, teamID: iosTeamID})
	appleIds := map[string]string{}
	for name, identity := range map[string]iostest.Identity{"held": held, "outside": outside, "expired": expired} {
		appleIds[name] = f.apple.RegisterCertificate(identity.Certificate.Raw)
	}

	certificates, err := f.service.ListIosSigningCertificates(ctx, f.appId, identifierId)
	require.NoError(t, err)
	assert.Equal(t, []services.IosSigningCertificate{
		{
			AppleId:         appleIds["outside"],
			Name:            outside.Certificate.Subject.CommonName,
			SerialNumber:    strings.ToUpper(outside.Certificate.SerialNumber.Text(16)),
			ExpiresAt:       outside.Certificate.NotAfter.UTC().Format(time.RFC3339),
			FingerprintSHA1: ios.Fingerprint(outside.Certificate.Raw),
		},
		{
			AppleId:            appleIds["held"],
			Name:               held.Certificate.Subject.CommonName,
			SerialNumber:       strings.ToUpper(held.Certificate.SerialNumber.Text(16)),
			ExpiresAt:          held.Certificate.NotAfter.UTC().Format(time.RFC3339),
			FingerprintSHA1:    ios.Fingerprint(held.Certificate.Raw),
			XpremCertificateId: &heldId,
			Selectable:         true,
		},
	}, certificates, "expired certificates are left out, latest expiry first")

	f.apple.Status = http.StatusServiceUnavailable
	_, err = f.service.ListIosSigningCertificates(ctx, f.appId, identifierId)
	assert.ErrorIs(t, err, appstoreconnect.ErrUnavailable)
}

func TestImportIosCertificate(t *testing.T) {
	f := newAppStoreConnectFixture(t)
	ctx := context.Background()
	identifierId := insertIdentifier(t, f.identifiers, f.appId, types.PlatformIOS, "com.example.app")
	outside := newDistributionIdentity(time.Now().Add(365 * 24 * time.Hour))
	fingerprint := ios.Fingerprint(outside.Certificate.Raw)
	importP12 := func(fingerprint string, p12 []byte, password string) ([]services.IosSigningCertificate, error) {
		return f.service.ImportIosCertificate(ctx, f.appId, identifierId, services.IosCertificateImportInput{
			FingerprintSHA1:      fingerprint,
			CertificateP12Base64: base64.StdEncoding.EncodeToString(p12),
			CertificatePassword:  password,
		})
	}

	_, err := importP12(fingerprint, outside.P12("secret"), "secret")
	requireValidationMessage(t, err, "add an API key")
	f.saveKey(t, f.appId)
	f.apple.RegisterCertificate(outside.Certificate.Raw)

	_, err = importP12(fingerprint, outside.P12("secret"), "wrong")
	requireValidationMessage(t, err, "certificatePassword: the password is incorrect")
	other := newDistributionIdentity(time.Now().Add(time.Hour))
	_, err = importP12(fingerprint, other.P12(""), "")
	requireValidationMessage(t, err, "This .p12 contains a different certificate than the one you selected")
	_, err = importP12(ios.Fingerprint(other.Certificate.Raw), other.P12(""), "")
	requireValidationMessage(t, err, "not an unexpired distribution certificate of the Apple team")
	expired := newDistributionIdentity(time.Now().Add(-time.Hour))
	f.apple.RegisterCertificate(expired.Certificate.Raw)
	_, err = importP12(ios.Fingerprint(expired.Certificate.Raw), expired.P12(""), "")
	requireValidationMessage(t, err, "expired")
	development := iostest.NewIdentity("Apple Development: Jane Doe (FGHIJ67890)", iosTeamID, time.Now().Add(time.Hour))
	_, err = importP12(ios.Fingerprint(development.Certificate.Raw), development.P12(""), "")
	requireValidationMessage(t, err, "Only a distribution certificate")
	_, err = importP12(fingerprint, []byte("x"), "")
	var valErr *validation.Error
	assert.ErrorAs(t, err, &valErr)
	pool, err := f.pool.Query(ctx, "SELECT 1 FROM ios_certificates WHERE fingerprint_sha1 = ANY($1)", []string{fingerprint, ios.Fingerprint(other.Certificate.Raw), ios.Fingerprint(expired.Certificate.Raw)})
	require.NoError(t, err)
	assert.False(t, pool.Next(), "refused files are not stored")
	pool.Close()

	certificates, err := importP12(strings.ToLower(fingerprint), outside.P12("secret"), "secret")
	require.NoError(t, err)
	require.Len(t, certificates, 1)
	require.NotNil(t, certificates[0].XpremCertificateId)
	assert.True(t, certificates[0].Selectable)
	listed, err := f.service.ListIosSigningCertificates(ctx, f.appId, identifierId)
	require.NoError(t, err)
	assert.Equal(t, certificates, listed, "the response is the refreshed listing")
	certificateId := *certificates[0].XpremCertificateId

	readSealed := func() (string, string, types.IosCertificateSource) {
		var p12, password string
		var source types.IosCertificateSource
		require.NoError(t, f.pool.QueryRow(ctx, "SELECT sealed_certificate, sealed_certificate_password, source FROM ios_certificates WHERE id = $1", certificateId).Scan(&p12, &password, &source))
		plainP12, err := crypto.UnsealAESGCM(p12, []byte(iosMasterKey), []byte(certificateId+"|ios_certificates|certificate"))
		require.NoError(t, err)
		plainPassword, err := crypto.UnsealAESGCM(password, []byte(iosMasterKey), []byte(certificateId+"|ios_certificates|certificate_password"))
		require.NoError(t, err)
		_, err = ios.ParseCertificate(plainP12, string(plainPassword))
		require.NoError(t, err)
		return string(plainP12), string(plainPassword), source
	}
	_, password, source := readSealed()
	assert.Equal(t, "secret", password)
	assert.Equal(t, types.IosCertificateUploaded, source)
	metadata, err := f.service.GetIosCredentialsMetadata(ctx, f.appId, identifierId)
	require.NoError(t, err)
	assert.Equal(t, services.IosSigningSettingView{Mode: types.IosSigningAutomatic}, metadata.Signing, "importing does not select the certificate")

	_, err = f.pool.Exec(ctx, "UPDATE ios_certificates SET source = 'generated' WHERE id = $1", certificateId)
	require.NoError(t, err)
	certificates, err = importP12(fingerprint, outside.P12(""), "")
	require.NoError(t, err)
	assert.Equal(t, certificateId, *certificates[0].XpremCertificateId, "a re-upload keeps the pool row")
	_, password, source = readSealed()
	assert.Empty(t, password, "a re-upload replaces the file and its password")
	assert.Equal(t, types.IosCertificateGenerated, source, "a re-upload keeps the source")

	assert.Equal(t, []auditlog.Action{
		auditlog.ActionAppStoreConnectApiKeySaved,
		auditlog.ActionIosCertificateSaved,
		auditlog.ActionIosCertificateSaved,
	}, f.actions)
}
