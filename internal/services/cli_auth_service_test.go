package services

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"xprem/internal/auditlog"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeCliAuthRepo struct {
	keyID       int64
	nameErr     error
	nameQueries int
}

func (f *fakeCliAuthRepo) ValidateCliCredential(_ context.Context, _ string, _ types.Auth) (int64, error) {
	return f.keyID, nil
}
func (f *fakeCliAuthRepo) InsertApiKey(_ context.Context, _ string, _ string, _ string, _ string) (int64, error) {
	return 43, nil
}
func (f *fakeCliAuthRepo) GetApiKeysMetadataByAppID(_ context.Context, _ string) ([]pgdb.GetApiKeysMetadataByAppIDRow, error) {
	return []pgdb.GetApiKeysMetadataByAppIDRow{
		{ID: 41, Name: "other-key"},
		{ID: 42, Name: "ci-production"},
	}, nil
}
func (f *fakeCliAuthRepo) GetApiKeyNameByID(_ context.Context, _ string, apiKeyId int64) (string, error) {
	f.nameQueries++
	if f.nameErr != nil {
		return "", f.nameErr
	}
	if apiKeyId == 42 {
		return "ci-production", nil
	}
	return "other-key", nil
}
func (f *fakeCliAuthRepo) RevokeApiKeyByID(_ context.Context, apiKeyId int64, _ string) (string, error) {
	if apiKeyId == 42 {
		return "ci-production", nil
	}
	return "other-key", nil
}

func TestValidateCliCredentialResolvesKeyIdentity(t *testing.T) {
	repo := &fakeCliAuthRepo{keyID: 42}
	cliAuth := NewCliAuthService(repo, nil)
	cliAuth.SetAuditActive(func() bool { return true })

	credential, err := cliAuth.ValidateCliCredential(context.Background(), "app-1", types.Auth{}, "", netip.Addr{})
	require.NoError(t, err)
	assert.Equal(t, CliCredential{AppID: "app-1", KeyID: "42", KeyName: "ci-production"}, credential)
}

func TestValidateCliCredentialSurvivesMetadataFailure(t *testing.T) {
	// The name only enriches the audit display: an unreadable name must not
	// fail a valid credential, it just degrades to the app-scope display.
	repo := &fakeCliAuthRepo{keyID: 42, nameErr: errors.New("lookup unavailable")}
	cliAuth := NewCliAuthService(repo, nil)
	cliAuth.SetAuditActive(func() bool { return true })

	credential, err := cliAuth.ValidateCliCredential(context.Background(), "app-1", types.Auth{}, "", netip.Addr{})
	require.NoError(t, err)
	assert.Equal(t, CliCredential{AppID: "app-1", KeyID: "42"}, credential)
}

func TestValidateCliCredentialSkipsLookupWhenAuditInactive(t *testing.T) {
	// Community deployments must not pay the metadata query for a name the
	// dropped event would never display.
	repo := &fakeCliAuthRepo{keyID: 42}
	cliAuth := NewCliAuthService(repo, nil)

	credential, err := cliAuth.ValidateCliCredential(context.Background(), "app-1", types.Auth{}, "", netip.Addr{})
	require.NoError(t, err)
	assert.Equal(t, CliCredential{AppID: "app-1", KeyID: "42"}, credential)
	assert.Zero(t, repo.nameQueries)

	cliAuth.SetAuditActive(func() bool { return false })
	_, err = cliAuth.ValidateCliCredential(context.Background(), "app-1", types.Auth{}, "", netip.Addr{})
	require.NoError(t, err)
	assert.Zero(t, repo.nameQueries)
}

func TestApiKeyLifecycleEmitsAuditEvents(t *testing.T) {
	repo := &fakeCliAuthRepo{keyID: 42}
	recorder := &fakeAuditRecorder{}
	cliAuth := NewCliAuthService(repo, nil)
	cliAuth.SetOnAuditEvent(recorder.Record)
	ctx := adminManagementCtx()

	_, err := cliAuth.GenerateAPIKey(ctx, "app-1", "deploy key")
	require.NoError(t, err)
	require.Len(t, recorder.events, 1)
	created := recorder.events[0]
	assert.Equal(t, auditlog.ActionAPIKeyCreated, created.Action)
	assert.Equal(t, "43", created.TargetID)
	assert.Equal(t, "deploy key", created.TargetDisplay)
	assert.Equal(t, "app-1", created.AppID)
	assert.NotEmpty(t, created.Metadata["hint"])

	// The revocation entry names the key it removed: read before the delete.
	require.NoError(t, cliAuth.RevokeApiKey(ctx, "app-1", "42"))
	require.Len(t, recorder.events, 2)
	revoked := recorder.events[1]
	assert.Equal(t, auditlog.ActionAPIKeyRevoked, revoked.Action)
	assert.Equal(t, "42", revoked.TargetID)
	assert.Equal(t, "ci-production", revoked.TargetDisplay)
}

func TestValidateCliCredentialStatelessHasNoKeyIdentity(t *testing.T) {
	// Stateless mode's credential is the app's Expo token: id 0, no name.
	repo := &fakeCliAuthRepo{keyID: 0}
	cliAuth := NewCliAuthService(repo, nil)
	cliAuth.SetAuditActive(func() bool { return true })

	credential, err := cliAuth.ValidateCliCredential(context.Background(), "app-1", types.Auth{}, "", netip.Addr{})
	require.NoError(t, err)
	assert.Equal(t, CliCredential{AppID: "app-1"}, credential)
	assert.Zero(t, repo.nameQueries)
}
