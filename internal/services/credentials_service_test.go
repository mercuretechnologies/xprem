// Service-level tests for the Android credentials vault: sealing (with the
// app-bound AAD), validation, the metadata projection, audit emission and the
// stateless-mode refusal. SQL persistence is covered by the store tests.
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

type fakeCredentialsRepo struct {
	byAppId map[string]store.SealedAndroidCredentials
}

func newFakeCredentialsRepo() *fakeCredentialsRepo {
	return &fakeCredentialsRepo{byAppId: map[string]store.SealedAndroidCredentials{}}
}

func (f *fakeCredentialsRepo) UpsertAndroidCredentials(_ context.Context, appId string, credentials store.SealedAndroidCredentials) error {
	f.byAppId[appId] = credentials
	return nil
}

func (f *fakeCredentialsRepo) GetAndroidCredentials(_ context.Context, appId string) (*store.SealedAndroidCredentials, error) {
	credentials, ok := f.byAppId[appId]
	if !ok {
		return nil, nil
	}
	return &credentials, nil
}

func (f *fakeCredentialsRepo) DeleteAndroidCredentials(_ context.Context, appId string) error {
	if _, ok := f.byAppId[appId]; !ok {
		return &store.ErrResourceNotFound{Resource: "android credentials", Identifier: appId}
	}
	delete(f.byAppId, appId)
	return nil
}

const testMasterKey = "0123456789abcdef0123456789abcdef"

func setMasterKey(t *testing.T) {
	t.Helper()
	t.Setenv("AWSSM_DB_KEYS_MASTER_KEY_SECRET_ID", "")
	t.Setenv("DB_KEYS_MASTER_KEY_B64", base64.StdEncoding.EncodeToString([]byte(testMasterKey)))
}

func validAndroidInput() AndroidCredentialsInput {
	return AndroidCredentialsInput{
		AndroidPackage:   "com.example.app",
		KeyAlias:         "upload",
		KeystoreBase64:   base64.StdEncoding.EncodeToString([]byte("fake-jks-bytes")),
		KeystorePassword: "keystore-pass",
		KeyPassword:      "key-pass",
	}
}

func TestSaveAndroidCredentialsSealsEveryTouchedSecret(t *testing.T) {
	setMasterKey(t)
	repo := newFakeCredentialsRepo()
	service := NewCredentialsService(repo)

	input := validAndroidInput()
	input.GoogleServiceAccountKeyJSON = `{"type":"service_account"}`
	require.NoError(t, service.SaveAndroidCredentials(context.Background(), "app-1", input))

	sealed := repo.byAppId["app-1"]
	assert.Equal(t, "com.example.app", sealed.AndroidPackage)
	assert.Equal(t, "upload", sealed.KeyAlias)

	keystore, err := crypto.UnsealAESGCM(sealed.SealedKeystore, []byte(testMasterKey), androidCredentialAAD("app-1", "keystore"))
	require.NoError(t, err)
	assert.Equal(t, "fake-jks-bytes", string(keystore))
	keystorePassword, err := crypto.UnsealAESGCM(sealed.SealedKeystorePassword, []byte(testMasterKey), androidCredentialAAD("app-1", "keystore_password"))
	require.NoError(t, err)
	assert.Equal(t, "keystore-pass", string(keystorePassword))
	keyPassword, err := crypto.UnsealAESGCM(sealed.SealedKeyPassword, []byte(testMasterKey), androidCredentialAAD("app-1", "key_password"))
	require.NoError(t, err)
	assert.Equal(t, "key-pass", string(keyPassword))
	require.NotNil(t, sealed.SealedGoogleServiceAccountKey)
	gsa, err := crypto.UnsealAESGCM(*sealed.SealedGoogleServiceAccountKey, []byte(testMasterKey), androidCredentialAAD("app-1", "google_service_account_key"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"service_account"}`, string(gsa))

	// No sealed field is readable without the exact app binding.
	_, err = crypto.UnsealAESGCM(sealed.SealedKeystore, []byte(testMasterKey), androidCredentialAAD("app-2", "keystore"))
	assert.Error(t, err)
}

func TestSaveAndroidCredentialsRejectsInvalidInput(t *testing.T) {
	setMasterKey(t)
	service := NewCredentialsService(newFakeCredentialsRepo())
	ctx := context.Background()

	for name, mutate := range map[string]func(*AndroidCredentialsInput){
		"bad package":        func(i *AndroidCredentialsInput) { i.AndroidPackage = "no-dots" },
		"empty alias":        func(i *AndroidCredentialsInput) { i.KeyAlias = "" },
		"empty keystore pwd": func(i *AndroidCredentialsInput) { i.KeystorePassword = "" },
		"empty key pwd":      func(i *AndroidCredentialsInput) { i.KeyPassword = "" },
		"bad base64":         func(i *AndroidCredentialsInput) { i.KeystoreBase64 = "not base64!!" },
		"empty keystore":     func(i *AndroidCredentialsInput) { i.KeystoreBase64 = "" },
		"bad gsa json":       func(i *AndroidCredentialsInput) { i.GoogleServiceAccountKeyJSON = "{broken" },
	} {
		input := validAndroidInput()
		mutate(&input)
		err := service.SaveAndroidCredentials(ctx, "app-1", input)
		require.Error(t, err, name)
		var valErr *validation.Error
		assert.ErrorAs(t, err, &valErr, name)
	}
}

func TestAndroidCredentialsMetadataCarriesNoSecret(t *testing.T) {
	setMasterKey(t)
	repo := newFakeCredentialsRepo()
	service := NewCredentialsService(repo)
	require.NoError(t, service.SaveAndroidCredentials(context.Background(), "app-1", validAndroidInput()))

	metadata, err := service.GetAndroidCredentialsMetadata(context.Background(), "app-1")
	require.NoError(t, err)
	require.NotNil(t, metadata)
	assert.Equal(t, "com.example.app", metadata.AndroidPackage)
	assert.Equal(t, "upload", metadata.KeyAlias)
	assert.False(t, metadata.HasGoogleServiceAccountKey)

	missing, err := service.GetAndroidCredentialsMetadata(context.Background(), "unknown-app")
	require.NoError(t, err)
	assert.Nil(t, missing)
}

func TestAndroidCredentialsAuditEvents(t *testing.T) {
	setMasterKey(t)
	repo := newFakeCredentialsRepo()
	service := NewCredentialsService(repo)
	var recorded []auditlog.Event
	service.SetOnAuditEvent(func(_ context.Context, event auditlog.Event) {
		recorded = append(recorded, event)
	})

	require.NoError(t, service.SaveAndroidCredentials(context.Background(), "app-1", validAndroidInput()))
	require.NoError(t, service.DeleteAndroidCredentials(context.Background(), "app-1"))

	require.Len(t, recorded, 2)
	assert.Equal(t, auditlog.ActionAndroidCredentialsSaved, recorded[0].Action)
	assert.Equal(t, "com.example.app", recorded[0].TargetDisplay)
	assert.NotContains(t, recorded[0].Metadata, "keystore")
	assert.Equal(t, auditlog.ActionAndroidCredentialsDeleted, recorded[1].Action)
	assert.Equal(t, "com.example.app", recorded[1].TargetDisplay)
}

func TestAndroidCredentialsUnsupportedInStatelessMode(t *testing.T) {
	service := NewCredentialsService(nil)
	ctx := context.Background()
	assert.ErrorIs(t, service.SaveAndroidCredentials(ctx, "app-1", validAndroidInput()), store.ErrNotSupportedInStatelessMode)
	_, err := service.GetAndroidCredentialsMetadata(ctx, "app-1")
	assert.ErrorIs(t, err, store.ErrNotSupportedInStatelessMode)
	assert.ErrorIs(t, service.DeleteAndroidCredentials(ctx, "app-1"), store.ErrNotSupportedInStatelessMode)
}
