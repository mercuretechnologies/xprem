// Service-level tests for the Android credentials vault: sealing (with the
// identifier-bound AAD), validation, identifier resolution, the metadata
// projection, audit emission and the stateless-mode refusal. SQL persistence
// is covered by the store tests.
package services

import (
	"context"
	"encoding/base64"
	"testing"
	"xprem/internal/auditlog"
	"xprem/internal/crypto"
	"xprem/internal/store"
	"xprem/internal/validation"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeIdentifierRepo struct {
	byId map[string]store.AppIdentifierRef
	// appId every identifier belongs to; a mismatch resolves to nil.
	appId string
}

func newFakeIdentifierRepo(appId string) *fakeIdentifierRepo {
	return &fakeIdentifierRepo{byId: map[string]store.AppIdentifierRef{}, appId: appId}
}

func (f *fakeIdentifierRepo) add(id, platform, identifier string) {
	f.byId[id] = store.AppIdentifierRef{Id: id, Platform: platform, Identifier: identifier}
}

func (f *fakeIdentifierRepo) InsertAppIdentifier(_ context.Context, _ string, _ string, _ string) (string, error) {
	panic("not used in credentials tests")
}

func (f *fakeIdentifierRepo) GetAppIdentifiers(_ context.Context, _ string) ([]store.AppIdentifierRow, error) {
	panic("not used in credentials tests")
}

func (f *fakeIdentifierRepo) GetAppIdentifierByID(_ context.Context, appId string, identifierId string) (*store.AppIdentifierRef, error) {
	if appId != f.appId {
		return nil, nil
	}
	ref, ok := f.byId[identifierId]
	if !ok {
		return nil, nil
	}
	return &ref, nil
}

func (f *fakeIdentifierRepo) DeleteAppIdentifier(_ context.Context, _ string, _ string) error {
	panic("not used in credentials tests")
}

type fakeCredentialsRepo struct {
	byIdentifierId map[string]store.SealedAndroidCredentials
}

func newFakeCredentialsRepo() *fakeCredentialsRepo {
	return &fakeCredentialsRepo{byIdentifierId: map[string]store.SealedAndroidCredentials{}}
}

func (f *fakeCredentialsRepo) UpsertAndroidCredentials(_ context.Context, identifierId string, credentials store.SealedAndroidCredentials) error {
	f.byIdentifierId[identifierId] = credentials
	return nil
}

func (f *fakeCredentialsRepo) GetAndroidCredentials(_ context.Context, identifierId string) (*store.SealedAndroidCredentials, error) {
	credentials, ok := f.byIdentifierId[identifierId]
	if !ok {
		return nil, nil
	}
	return &credentials, nil
}

func (f *fakeCredentialsRepo) DeleteAndroidCredentials(_ context.Context, identifierId string) error {
	if _, ok := f.byIdentifierId[identifierId]; !ok {
		return &store.ErrResourceNotFound{Resource: "android credentials", Identifier: identifierId}
	}
	delete(f.byIdentifierId, identifierId)
	return nil
}

const testMasterKey = "0123456789abcdef0123456789abcdef"

const (
	testAppId        = "app-1"
	testIdentifierId = "11111111-1111-1111-1111-111111111111"
)

func setMasterKey(t *testing.T) {
	t.Helper()
	t.Setenv("AWSSM_DB_KEYS_MASTER_KEY_SECRET_ID", "")
	t.Setenv("DB_KEYS_MASTER_KEY_B64", base64.StdEncoding.EncodeToString([]byte(testMasterKey)))
}

func newCredentialsFixture() (*CredentialsService, *fakeCredentialsRepo, *fakeIdentifierRepo) {
	identifiers := newFakeIdentifierRepo(testAppId)
	identifiers.add(testIdentifierId, PlatformAndroid, "com.example.app")
	repo := newFakeCredentialsRepo()
	return NewCredentialsService(repo, identifiers), repo, identifiers
}

func validAndroidInput() AndroidCredentialsInput {
	return AndroidCredentialsInput{
		KeyAlias:         "upload",
		KeystoreBase64:   base64.StdEncoding.EncodeToString([]byte("fake-jks-bytes")),
		KeystorePassword: "keystore-pass",
		KeyPassword:      "key-pass",
	}
}

func TestSaveAndroidCredentialsSealsEveryTouchedSecret(t *testing.T) {
	setMasterKey(t)
	service, repo, _ := newCredentialsFixture()

	input := validAndroidInput()
	input.GoogleServiceAccountKeyJSON = `{"type":"service_account"}`
	require.NoError(t, service.SaveAndroidCredentials(context.Background(), testAppId, testIdentifierId, input))

	sealed := repo.byIdentifierId[testIdentifierId]
	assert.Equal(t, "upload", sealed.KeyAlias)

	keystore, err := crypto.UnsealAESGCM(sealed.SealedKeystore, []byte(testMasterKey), androidCredentialAAD(testIdentifierId, "keystore"))
	require.NoError(t, err)
	assert.Equal(t, "fake-jks-bytes", string(keystore))
	keystorePassword, err := crypto.UnsealAESGCM(sealed.SealedKeystorePassword, []byte(testMasterKey), androidCredentialAAD(testIdentifierId, "keystore_password"))
	require.NoError(t, err)
	assert.Equal(t, "keystore-pass", string(keystorePassword))
	keyPassword, err := crypto.UnsealAESGCM(sealed.SealedKeyPassword, []byte(testMasterKey), androidCredentialAAD(testIdentifierId, "key_password"))
	require.NoError(t, err)
	assert.Equal(t, "key-pass", string(keyPassword))
	require.NotNil(t, sealed.SealedGoogleServiceAccountKey)
	gsa, err := crypto.UnsealAESGCM(*sealed.SealedGoogleServiceAccountKey, []byte(testMasterKey), androidCredentialAAD(testIdentifierId, "google_service_account_key"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"service_account"}`, string(gsa))

	// No sealed field is readable under another identifier's binding.
	_, err = crypto.UnsealAESGCM(sealed.SealedKeystore, []byte(testMasterKey), androidCredentialAAD("22222222-2222-2222-2222-222222222222", "keystore"))
	assert.Error(t, err)
}

func TestSaveAndroidCredentialsResolvesTheIdentifier(t *testing.T) {
	setMasterKey(t)
	service, _, identifiers := newCredentialsFixture()
	ctx := context.Background()

	// Unknown identifier id.
	err := service.SaveAndroidCredentials(ctx, testAppId, "33333333-3333-3333-3333-333333333333", validAndroidInput())
	notFoundErr := (*store.ErrResourceNotFound)(nil)
	assert.ErrorAs(t, err, &notFoundErr)

	// Identifier of another app resolves to not-found too.
	err = service.SaveAndroidCredentials(ctx, "other-app", testIdentifierId, validAndroidInput())
	assert.ErrorAs(t, err, &notFoundErr)

	// An ios identifier cannot carry android credentials.
	identifiers.add("44444444-4444-4444-4444-444444444444", PlatformIOS, "com.example.app")
	err = service.SaveAndroidCredentials(ctx, testAppId, "44444444-4444-4444-4444-444444444444", validAndroidInput())
	var valErr *validation.Error
	assert.ErrorAs(t, err, &valErr)
}

func TestSaveAndroidCredentialsRejectsInvalidInput(t *testing.T) {
	setMasterKey(t)
	service, _, _ := newCredentialsFixture()
	ctx := context.Background()

	for name, mutate := range map[string]func(*AndroidCredentialsInput){
		"empty alias":        func(i *AndroidCredentialsInput) { i.KeyAlias = "" },
		"empty keystore pwd": func(i *AndroidCredentialsInput) { i.KeystorePassword = "" },
		"empty key pwd":      func(i *AndroidCredentialsInput) { i.KeyPassword = "" },
		"bad base64":         func(i *AndroidCredentialsInput) { i.KeystoreBase64 = "not base64!!" },
		"empty keystore":     func(i *AndroidCredentialsInput) { i.KeystoreBase64 = "" },
		"bad gsa json":       func(i *AndroidCredentialsInput) { i.GoogleServiceAccountKeyJSON = "{broken" },
	} {
		input := validAndroidInput()
		mutate(&input)
		err := service.SaveAndroidCredentials(ctx, testAppId, testIdentifierId, input)
		require.Error(t, err, name)
		var valErr *validation.Error
		assert.ErrorAs(t, err, &valErr, name)
	}
}

func TestAndroidCredentialsMetadataCarriesNoSecret(t *testing.T) {
	setMasterKey(t)
	service, _, _ := newCredentialsFixture()
	require.NoError(t, service.SaveAndroidCredentials(context.Background(), testAppId, testIdentifierId, validAndroidInput()))

	metadata, err := service.GetAndroidCredentialsMetadata(context.Background(), testAppId, testIdentifierId)
	require.NoError(t, err)
	require.NotNil(t, metadata)
	assert.Equal(t, "com.example.app", metadata.Identifier)
	assert.Equal(t, "upload", metadata.KeyAlias)
	assert.False(t, metadata.HasGoogleServiceAccountKey)
}

func TestAndroidCredentialsAuditEvents(t *testing.T) {
	setMasterKey(t)
	service, _, _ := newCredentialsFixture()
	var recorded []auditlog.Event
	service.SetOnAuditEvent(func(_ context.Context, event auditlog.Event) {
		recorded = append(recorded, event)
	})

	require.NoError(t, service.SaveAndroidCredentials(context.Background(), testAppId, testIdentifierId, validAndroidInput()))
	require.NoError(t, service.DeleteAndroidCredentials(context.Background(), testAppId, testIdentifierId))

	require.Len(t, recorded, 2)
	assert.Equal(t, auditlog.ActionAndroidCredentialsSaved, recorded[0].Action)
	assert.Equal(t, testIdentifierId, recorded[0].TargetID)
	assert.Equal(t, "com.example.app", recorded[0].TargetDisplay)
	assert.NotContains(t, recorded[0].Metadata, "keystore")
	assert.Equal(t, auditlog.ActionAndroidCredentialsDeleted, recorded[1].Action)
	assert.Equal(t, "com.example.app", recorded[1].TargetDisplay)
}

func TestAndroidCredentialsUnsupportedInStatelessMode(t *testing.T) {
	service := NewCredentialsService(nil, nil)
	ctx := context.Background()
	assert.ErrorIs(t, service.SaveAndroidCredentials(ctx, testAppId, testIdentifierId, validAndroidInput()), store.ErrNotSupportedInStatelessMode)
	_, err := service.GetAndroidCredentialsMetadata(ctx, testAppId, testIdentifierId)
	assert.ErrorIs(t, err, store.ErrNotSupportedInStatelessMode)
	assert.ErrorIs(t, service.DeleteAndroidCredentials(ctx, testAppId, testIdentifierId), store.ErrNotSupportedInStatelessMode)
}
