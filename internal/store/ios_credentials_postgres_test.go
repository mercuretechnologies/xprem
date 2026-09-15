package store_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
	"xprem/internal/appstoreconnect/appstoreconnecttest"
	"xprem/internal/auditlog"
	"xprem/internal/database"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/ios"
	"xprem/internal/ios/iostest"
	"xprem/internal/services"
	"xprem/internal/store"
	"xprem/internal/types"
	"xprem/internal/validation"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
	identifiers *store.PostgresAppIdentifierStore
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
		service:     services.NewIosCredentialsService(store.NewPostgresIosCredentialsStore(engine), identifiers),
		identifiers: identifiers,
		pool:        pool,
	}
	f.service.SetOnAuditEvent(func(_ context.Context, event auditlog.Event) {
		f.actions = append(f.actions, event.Action)
	})
	return f
}

type poolCertificate struct {
	identity        iostest.Identity
	certificateType types.IosCertificateType
	teamID          string
}

type execer interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// insertPoolCertificate stores a certificate in the pool the way xprem would, without its sealed file.
func insertPoolCertificate(t *testing.T, db execer, certificate poolCertificate) string {
	t.Helper()
	id := uuid.NewString()
	_, err := db.Exec(context.Background(), `INSERT INTO ios_certificates (
		id, sealed_certificate, sealed_certificate_password, common_name, serial_number,
		fingerprint_sha1, certificate_type, team_id, expires_at, source
	) VALUES ($1, 'sealed', 'sealed', $2, $3, $4, $5, $6, $7, 'uploaded')`,
		id, certificate.identity.Certificate.Subject.CommonName, certificate.identity.Certificate.SerialNumber.Text(16),
		ios.Fingerprint(certificate.identity.Certificate.Raw), certificate.certificateType, certificate.teamID, certificate.identity.Certificate.NotAfter)
	require.NoError(t, err)
	return id
}

func (f *iosFixture) insertPoolCertificate(t *testing.T, certificate poolCertificate) string {
	t.Helper()
	return insertPoolCertificate(t, f.pool, certificate)
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

	require.NoError(t, update(types.IosSigningCertificate, certificateId), "without an API key the team cannot be checked")
	f.saveKey(t, f.appId)
	assert.True(t, f.hasIosCredentials(t, f.appId, identifierId), "an API key is enough")

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
			Source:       types.IosCertificateUploaded,
		},
	}, signing())

	expired := f.insertPoolCertificate(t, poolCertificate{identity: newDistributionIdentity(time.Now().Add(-time.Hour)), certificateType: types.IosCertificateDistribution, teamID: iosTeamID})
	development := f.insertPoolCertificate(t, poolCertificate{identity: newDistributionIdentity(time.Now().Add(time.Hour)), certificateType: types.IosCertificateDevelopment, teamID: iosTeamID})
	otherTeam := f.insertPoolCertificate(t, poolCertificate{identity: newDistributionIdentity(time.Now().Add(time.Hour)), certificateType: types.IosCertificateDistribution, teamID: "ZZZZZ99999"})
	f.apple.RegisterCertificate(valid.Certificate.Raw)
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
		auditlog.ActionIosSigningUpdated,
		auditlog.ActionAppStoreConnectApiKeySaved,
		auditlog.ActionIosSigningUpdated,
		auditlog.ActionIosSigningUpdated,
	}, f.actions)
}

func readMigration(t *testing.T, name string) (string, string) {
	t.Helper()
	migration, err := os.ReadFile("../database/postgres/migrations/" + name)
	require.NoError(t, err)
	up, down, found := strings.Cut(string(migration), "-- +goose Down")
	require.True(t, found)
	return up, down
}

func TestIosSigningSettingsMigrations(t *testing.T) {
	_, _, pool := setupCredentialsStores(t)
	ctx := context.Background()
	settingsUp, settingsDown := readMigration(t, "20260915140000_ios_signing_settings.sql")
	singleUp, singleDown := readMigration(t, "20260915150000_ios_single_signing_setting.sql")
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	exec := func(sql string, arguments ...any) {
		t.Helper()
		_, err := tx.Exec(ctx, sql, arguments...)
		require.NoError(t, err)
	}
	tableExists := func(table string) bool {
		var exists bool
		require.NoError(t, tx.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", table).Scan(&exists))
		return exists
	}

	exec(singleDown)
	exec(settingsDown)
	appId := uuid.NewString()
	exec("INSERT INTO apps (id, name) VALUES ($1, 'migration')", appId)
	linked, adHocOnly, both := uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO app_identifiers (id, app_id, platform, identifier)
		VALUES ($1, $4, 'ios', 'com.example.linked'), ($2, $4, 'ios', 'com.example.adhoc'), ($3, $4, 'ios', 'com.example.both')`, linked, adHocOnly, both, appId)
	certificateId := insertPoolCertificate(t, tx, poolCertificate{identity: newDistributionIdentity(time.Now().Add(time.Hour)), certificateType: types.IosCertificateDistribution, teamID: iosTeamID})
	exec("INSERT INTO ios_identifier_certificates (app_identifier_id, certificate_id) VALUES ($1, $2)", linked, certificateId)
	exec(`INSERT INTO ios_provisioning_profiles (id, app_identifier_id, bundle_identifier, profile_uuid, name, team_id, profile_type, expires_at, certificate_fingerprints, sealed_profile)
		VALUES ($1, $2, 'com.example.linked', 'UUID', 'Profile', $3, 'app-store', now() + interval '1 day', '{}', 'sealed')`, uuid.NewString(), linked, iosTeamID)

	exec(settingsUp)
	assert.False(t, tableExists("ios_identifier_certificates"))
	assert.False(t, tableExists("ios_provisioning_profiles"))
	assert.True(t, tableExists("ios_certificates"), "the pool is kept")
	var distribution, mode string
	require.NoError(t, tx.QueryRow(ctx, "SELECT distribution, mode FROM ios_signing_settings WHERE app_identifier_id = $1 AND certificate_id = $2", linked, certificateId).Scan(&distribution, &mode))
	assert.Equal(t, []string{"app-store", "certificate"}, []string{distribution, mode}, "a link becomes an App Store certificate setting")
	exec(`INSERT INTO ios_signing_settings (app_identifier_id, distribution, mode, certificate_id)
		VALUES ($1, 'ad-hoc', 'certificate', $3), ($2, 'app-store', 'automatic', NULL), ($2, 'ad-hoc', 'certificate', $3)`, adHocOnly, both, certificateId)

	exec(singleUp)
	rows, err := tx.Query(ctx, `SELECT app_identifier_id::text, mode, COALESCE(certificate_id::text, '') FROM ios_signing_settings
		WHERE app_identifier_id IN ($1, $2, $3)`, linked, adHocOnly, both)
	require.NoError(t, err)
	settings, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) ([3]string, error) {
		var setting [3]string
		err := row.Scan(&setting[0], &setting[1], &setting[2])
		return setting, err
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, [][3]string{
		{linked, "certificate", certificateId},
		{adHocOnly, "certificate", certificateId},
		{both, "automatic", ""},
	}, settings, "one setting per identifier, the App Store one winning")

	savepoint, err := tx.Begin(ctx)
	require.NoError(t, err)
	_, err = savepoint.Exec(ctx, "INSERT INTO ios_signing_settings (app_identifier_id, mode) VALUES ($1, 'automatic')", both)
	require.Error(t, err, "one setting per identifier")
	require.NoError(t, savepoint.Rollback(ctx))
	_, err = tx.Exec(ctx, "UPDATE ios_signing_settings SET mode = 'automatic' WHERE app_identifier_id = $1", linked)
	require.Error(t, err, "automatic mode carries no certificate")
}

func TestIosProfileTypeUniqueMigration(t *testing.T) {
	_, _, pool := setupCredentialsStores(t)
	ctx := context.Background()
	migration, err := os.ReadFile("../database/postgres/migrations/20260915110000_ios_profile_type_unique.sql")
	require.NoError(t, err)
	up, down, found := strings.Cut(string(migration), "-- +goose Down")
	require.True(t, found)
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	insert := func(id int, profileType string) error {
		savepoint, err := tx.Begin(ctx)
		require.NoError(t, err)
		if _, err := savepoint.Exec(ctx, "INSERT INTO ios_provisioning_profiles VALUES ($1, 1, 'com.example.app', $2)", id, profileType); err != nil {
			require.NoError(t, savepoint.Rollback(ctx))
			return err
		}
		return savepoint.Commit(ctx)
	}
	_, err = tx.Exec(ctx, `CREATE TEMP TABLE ios_provisioning_profiles (
		id INTEGER PRIMARY KEY, app_identifier_id INTEGER, bundle_identifier TEXT, profile_type TEXT,
		CONSTRAINT ios_provisioning_profiles_app_identifier_id_bundle_identifi_key UNIQUE (app_identifier_id, bundle_identifier)
	) ON COMMIT DROP`)
	require.NoError(t, err)
	require.NoError(t, insert(1, "app-store"))

	_, err = tx.Exec(ctx, up)
	require.NoError(t, err)
	require.NoError(t, insert(2, "ad-hoc"), "another type of the same bundle id")
	require.Error(t, insert(3, "ad-hoc"), "the same type twice")

	savepoint, err := tx.Begin(ctx)
	require.NoError(t, err)
	_, err = savepoint.Exec(ctx, down)
	require.ErrorContains(t, err, "Cannot restore one provisioning profile per bundle identifier")
	require.NoError(t, savepoint.Rollback(ctx))

	_, err = tx.Exec(ctx, "DELETE FROM ios_provisioning_profiles WHERE id = 2")
	require.NoError(t, err)
	_, err = tx.Exec(ctx, down)
	require.NoError(t, err)
	var count int
	require.NoError(t, tx.QueryRow(ctx, "SELECT COUNT(*) FROM ios_provisioning_profiles").Scan(&count))
	assert.Equal(t, 1, count, "rows survive both directions")
	require.Error(t, insert(4, "ad-hoc"), "one profile per bundle id again")
}
