package repository_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
	"xprem/internal/auditlog"
	"xprem/internal/database"
	"xprem/internal/database/postgres"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/ios"
	"xprem/internal/ios/iostest"
	"xprem/internal/providers/appstoreconnect/appstoreconnecttest"
	"xprem/internal/repository"
	"xprem/internal/services"
	"xprem/internal/types"
	"xprem/internal/validation"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	iosTeamID    = appstoreconnecttest.TeamID
	iosMasterKey = "0123456789abcdef0123456789abcdef"
)

type iosFixture struct {
	service     *services.IosCredentialsService
	identifiers *repository.PostgresAppIdentifierRepository
	pool        *pgxpool.Pool
	actions     []auditlog.Action
}

func newIosFixture(t *testing.T) *iosFixture {
	t.Helper()
	_, identifiers, pool := setupCredentialsStores(t)
	t.Setenv("AWSSM_DB_KEYS_MASTER_KEY_SECRET_ID", "")
	t.Setenv("DB_KEYS_MASTER_KEY_B64", base64.StdEncoding.EncodeToString([]byte(iosMasterKey)))
	engine := &database.Engine{Queries: pgdb.New(pool), DB: pool}
	f := &iosFixture{
		service:     services.NewIosCredentialsService(repository.NewPostgresIosCredentialsRepository(engine), identifiers),
		identifiers: identifiers,
		pool:        pool,
	}
	f.service.SetOnAuditEvent(func(_ context.Context, event auditlog.Event) {
		f.actions = append(f.actions, event.Action)
	})
	f.service.SetCertificateCreationLock(postgres.AdvisoryLocker(pool, postgres.IosCertificateLockID, "ios certificate"))
	return f
}

type poolCertificate struct {
	identity        iostest.Identity
	certificateType types.IosCertificateType
	teamID          string
}

// insertPoolCertificate stores a certificate in the pool the way xprem would, without its sealed file.
func (f *iosFixture) insertPoolCertificate(t *testing.T, certificate poolCertificate) string {
	t.Helper()
	id := uuid.NewString()
	_, err := f.pool.Exec(context.Background(), `INSERT INTO ios_certificates (
		id, sealed_certificate, sealed_certificate_password, common_name, serial_number,
		fingerprint_sha1, certificate_type, team_id, expires_at
	) VALUES ($1, 'sealed', 'sealed', $2, $3, $4, $5, $6, $7)`,
		id, certificate.identity.Certificate.Subject.CommonName, certificate.identity.Certificate.SerialNumber.Text(16),
		ios.Fingerprint(certificate.identity.Certificate.Raw), certificate.certificateType, certificate.teamID, certificate.identity.Certificate.NotAfter)
	require.NoError(t, err)
	return id
}

func (f *iosFixture) hasIosCredentials(t *testing.T, appId string, identifierId string) bool {
	t.Helper()
	rows, err := f.identifiers.GetAppIdentifiers(context.Background(), appId)
	require.NoError(t, err)
	for _, row := range rows {
		if row.Id == identifierId {
			return row.HasIosCredentials
		}
	}
	t.Fatalf("identifier %s not listed", identifierId)
	return false
}

func newDistributionIdentity(expiresAt time.Time) iostest.Identity {
	return iostest.NewIdentity("Apple Distribution: Example Inc ("+iosTeamID+")", iosTeamID, expiresAt)
}

func requireValidationMessage(t *testing.T, err error, message string) {
	t.Helper()
	var valErr *validation.Error
	require.ErrorAs(t, err, &valErr)
	assert.Contains(t, valErr.Error(), message)
}

func TestIosSigningSetting(t *testing.T) {
	f := newAppStoreConnectFixture(t)
	ctx := context.Background()
	identifierId := insertIdentifier(t, f.identifiers, f.appId, types.PlatformIOS, "com.example.app")
	valid := newDistributionIdentity(time.Now().Add(365 * 24 * time.Hour))
	certificateId := f.insertPoolCertificate(t, poolCertificate{identity: valid, certificateType: types.IosCertificateDistribution, teamID: iosTeamID})
	update := func(mode types.IosSigningMode, certificateId string) error {
		return f.service.UpdateIosSigningSetting(ctx, f.appId, identifierId, services.IosSigningSettingInput{Mode: mode, CertificateId: certificateId})
	}
	signing := func() services.IosSigningSettingView {
		metadata, err := f.service.GetIosCredentialsMetadata(ctx, f.appId, identifierId)
		require.NoError(t, err)
		return metadata.Signing
	}

	metadata, err := f.service.GetIosCredentialsMetadata(ctx, f.appId, identifierId)
	require.NoError(t, err)
	encoded, err := json.Marshal(metadata)
	require.NoError(t, err)
	assert.JSONEq(t, `{"identifier":"com.example.app","signing":{"mode":"automatic","certificate":null,"certificateMissing":false}}`, string(encoded))
	assert.False(t, f.hasIosCredentials(t, f.appId, identifierId), "no API key")

	requireValidationMessage(t, update(types.IosSigningCertificate, certificateId), "Add an App Store Connect API key first")
	assert.Equal(t, services.IosSigningSettingView{Mode: types.IosSigningAutomatic}, signing(), "a refused update keeps the setting")
	f.saveKey(t, f.appId)
	assert.True(t, f.hasIosCredentials(t, f.appId, identifierId), "an API key is enough")

	requireValidationMessage(t, update(types.IosSigningCertificate, certificateId), "Apple lists no distribution certificate for this team")
	f.apple.RegisterCertificate(valid.Certificate.Raw)
	require.NoError(t, update(types.IosSigningCertificate, certificateId))
	assert.Equal(t, services.IosSigningSettingView{
		Mode: types.IosSigningCertificate,
		Certificate: &services.IosCertificateMetadata{
			Id:           certificateId,
			CommonName:   valid.Certificate.Subject.CommonName,
			SerialNumber: valid.Certificate.SerialNumber.Text(16),
			Type:         types.IosCertificateDistribution,
			TeamID:       iosTeamID,
			ExpiresAt:    valid.Certificate.NotAfter.UTC().Format(time.RFC3339),
		},
	}, signing())

	expired := f.insertPoolCertificate(t, poolCertificate{identity: newDistributionIdentity(time.Now().Add(-time.Hour)), certificateType: types.IosCertificateDistribution, teamID: iosTeamID})
	development := f.insertPoolCertificate(t, poolCertificate{identity: newDistributionIdentity(time.Now().Add(time.Hour)), certificateType: types.IosCertificateDevelopment, teamID: iosTeamID})
	otherTeam := f.insertPoolCertificate(t, poolCertificate{identity: newDistributionIdentity(time.Now().Add(time.Hour)), certificateType: types.IosCertificateDistribution, teamID: "ZZZZZ99999"})
	requireValidationMessage(t, update(types.IosSigningCertificate, uuid.NewString()), "not on xprem")
	requireValidationMessage(t, update(types.IosSigningCertificate, "not-a-uuid"), "UUID")
	requireValidationMessage(t, update(types.IosSigningCertificate, expired), "expired")
	requireValidationMessage(t, update(types.IosSigningCertificate, development), "distribution certificate")
	requireValidationMessage(t, update(types.IosSigningCertificate, otherTeam), "belongs to team ZZZZZ99999, the App Store Connect API key to team "+iosTeamID)
	requireValidationMessage(t, update("manual", ""), "mode")
	assert.Equal(t, certificateId, signing().Certificate.Id, "a refused update keeps the setting")

	_, err = f.pool.Exec(ctx, "DELETE FROM ios_certificates WHERE id = $1", certificateId)
	require.NoError(t, err)
	assert.Equal(t, services.IosSigningSettingView{Mode: types.IosSigningCertificate, CertificateMissing: true}, signing())

	require.NoError(t, update(types.IosSigningAutomatic, certificateId), "the certificate id is ignored in automatic mode")
	assert.Equal(t, services.IosSigningSettingView{Mode: types.IosSigningAutomatic}, signing())

	androidId := insertIdentifier(t, f.identifiers, f.appId, types.PlatformAndroid, "com.example.app")
	var valErr *validation.Error
	assert.ErrorAs(t, f.service.UpdateIosSigningSetting(ctx, f.appId, androidId, services.IosSigningSettingInput{Mode: types.IosSigningAutomatic}), &valErr)

	assert.Equal(t, []auditlog.Action{
		auditlog.ActionAppStoreConnectApiKeySaved,
		auditlog.ActionIosSigningUpdated,
		auditlog.ActionIosSigningUpdated,
	}, f.actions)
}

func TestIosCredentialsScopedToApp(t *testing.T) {
	f := newAppStoreConnectFixture(t)
	ctx := context.Background()
	identifierId := insertIdentifier(t, f.identifiers, f.appId, types.PlatformIOS, "com.example.app")
	valid := newDistributionIdentity(time.Now().Add(365 * 24 * time.Hour))
	certificateId := f.insertPoolCertificate(t, poolCertificate{identity: valid, certificateType: types.IosCertificateDistribution, teamID: iosTeamID})
	f.saveKey(t, f.appId)
	f.apple.RegisterCertificate(valid.Certificate.Raw)
	require.NoError(t, f.service.UpdateIosSigningSetting(ctx, f.appId, identifierId, services.IosSigningSettingInput{Mode: types.IosSigningCertificate, CertificateId: certificateId}))

	otherAppId := insertBareApp(t, f.pool)
	notFoundErr := (*repository.ErrResourceNotFound)(nil)
	_, err := f.service.GetIosCredentialsMetadata(ctx, otherAppId, identifierId)
	assert.ErrorAs(t, err, &notFoundErr)
	assert.ErrorAs(t, f.service.UpdateIosSigningSetting(ctx, otherAppId, identifierId, services.IosSigningSettingInput{Mode: types.IosSigningAutomatic}), &notFoundErr)
	_, err = f.service.ListIosSigningCertificates(ctx, otherAppId, identifierId)
	assert.ErrorAs(t, err, &notFoundErr)
	_, err = f.service.ImportIosCertificate(ctx, otherAppId, identifierId, services.IosCertificateImportInput{
		FingerprintSHA1:      ios.Fingerprint(valid.Certificate.Raw),
		CertificateP12Base64: base64.StdEncoding.EncodeToString(valid.P12("")),
	})
	assert.ErrorAs(t, err, &notFoundErr)

	metadata, err := f.service.GetIosCredentialsMetadata(ctx, f.appId, identifierId)
	require.NoError(t, err)
	require.NotNil(t, metadata.Signing.Certificate)
	assert.Equal(t, certificateId, metadata.Signing.Certificate.Id, "the first app's setting is unchanged")
}
