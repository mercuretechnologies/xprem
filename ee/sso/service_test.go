// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package sso

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"xprem/internal/crypto"
	"xprem/internal/services"
	"xprem/internal/store"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeUserRepo is an in-memory services.UserRepository, mirroring the fake
// used by the community service tests.
type fakeUserRepo struct {
	users map[string]store.User
}

func newFakeUserRepo() *fakeUserRepo {
	return &fakeUserRepo{users: map[string]store.User{}}
}

func (r *fakeUserRepo) InsertUser(_ context.Context, params store.InsertUserParameters) (store.User, error) {
	email := store.NormalizeEmail(params.Email)
	for _, user := range r.users {
		if user.Email == email {
			return store.User{}, &store.ErrResourceAlreadyExists{Resource: "user", Identifier: email}
		}
	}
	user := store.User{Id: params.ID, Email: email, PasswordHash: params.PasswordHash, IsAdmin: params.IsAdmin, Enabled: params.Enabled, CreatedAt: time.Now()}
	r.users[params.ID] = user
	return user, nil
}

func (r *fakeUserRepo) GetUserByEmail(_ context.Context, email string) (store.User, error) {
	normalizedEmail := store.NormalizeEmail(email)
	for _, user := range r.users {
		if user.Email == normalizedEmail {
			return user, nil
		}
	}
	return store.User{}, &store.ErrResourceNotFound{Resource: "user", Identifier: normalizedEmail}
}

func (r *fakeUserRepo) GetUserByID(_ context.Context, id string) (store.User, error) {
	user, ok := r.users[id]
	if !ok {
		return store.User{}, &store.ErrResourceNotFound{Resource: "user", Identifier: id}
	}
	return user, nil
}

func (r *fakeUserRepo) GetUsers(_ context.Context) ([]store.User, error) {
	users := make([]store.User, 0, len(r.users))
	for _, user := range r.users {
		users = append(users, user)
	}
	return users, nil
}

func (r *fakeUserRepo) DeleteUserByID(_ context.Context, id string) error {
	delete(r.users, id)
	return nil
}

func (r *fakeUserRepo) UpdateUserPassword(_ context.Context, id string, passwordHash string) error {
	user := r.users[id]
	user.PasswordHash = passwordHash
	r.users[id] = user
	return nil
}

func (r *fakeUserRepo) UpdateUserIsAdmin(_ context.Context, id string, isAdmin bool) error {
	user := r.users[id]
	user.IsAdmin = isAdmin
	r.users[id] = user
	return nil
}

func (r *fakeUserRepo) UpdateUserEnabled(_ context.Context, id string, enabled bool) error {
	user := r.users[id]
	user.Enabled = enabled
	r.users[id] = user
	return nil
}

func (r *fakeUserRepo) BumpUserSessionVersion(_ context.Context, id string) error {
	user, ok := r.users[id]
	if !ok {
		return &store.ErrResourceNotFound{Resource: "user", Identifier: id}
	}
	user.SessionVersion++
	r.users[id] = user
	return nil
}

func (r *fakeUserRepo) TouchUserLastConnected(_ context.Context, id string) error {
	user, ok := r.users[id]
	if !ok {
		return &store.ErrResourceNotFound{Resource: "user", Identifier: id}
	}
	now := time.Now()
	user.LastConnectedAt = &now
	r.users[id] = user
	return nil
}

// fakeSSORepo is an in-memory SSORepository backed by a fakeUserRepo for the
// provisioning writes.
type fakeSSORepo struct {
	cfg    *SSOConfig
	cfgErr error
	users  *fakeUserRepo
	// identities maps issuer|subject to a user id.
	identities map[string]string
	touchCalls int
	saveCalls  int
	// provisionRaces simulates another replica winning the first sign-in:
	// the next ProvisionUser call persists under a different user id and
	// answers ErrResourceAlreadyExists, like the real store would.
	provisionRaces bool
}

func newFakeSSORepo(users *fakeUserRepo, cfg *SSOConfig) *fakeSSORepo {
	return &fakeSSORepo{cfg: cfg, users: users, identities: map[string]string{}}
}

func identityKey(issuer string, subject string) string {
	return issuer + "|" + subject
}

func (r *fakeSSORepo) GetConfig(_ context.Context) (*SSOConfig, error) {
	if r.cfgErr != nil {
		return r.cfg, r.cfgErr
	}
	if r.cfg == nil {
		return nil, nil
	}
	cfgCopy := *r.cfg
	return &cfgCopy, nil
}

func (r *fakeSSORepo) SaveConfig(_ context.Context, cfg SSOConfig) error {
	r.saveCalls++
	r.cfg = &cfg
	return nil
}

func (r *fakeSSORepo) DeleteConfig(_ context.Context) error {
	r.cfg = nil
	return nil
}

func (r *fakeSSORepo) FindUserBySubject(ctx context.Context, issuer string, subject string) (store.User, error) {
	userID, ok := r.identities[identityKey(issuer, subject)]
	if !ok {
		return store.User{}, &store.ErrResourceNotFound{Resource: "sso identity", Identifier: subject}
	}
	return r.users.GetUserByID(ctx, userID)
}

func (r *fakeSSORepo) LinkIdentity(_ context.Context, issuer string, subject string, userID string, _ string) error {
	key := identityKey(issuer, subject)
	if _, exists := r.identities[key]; exists {
		return &store.ErrResourceAlreadyExists{Resource: "sso identity", Identifier: subject}
	}
	r.identities[key] = userID
	return nil
}

func (r *fakeSSORepo) ProvisionUser(ctx context.Context, params store.InsertUserParameters, issuer string, subject string) (store.User, error) {
	key := identityKey(issuer, subject)
	if r.provisionRaces {
		r.provisionRaces = false
		otherReplicaID := "other-replica-" + params.ID
		if _, err := r.users.InsertUser(ctx, store.InsertUserParameters{ID: otherReplicaID, Email: params.Email, Enabled: params.Enabled}); err != nil {
			return store.User{}, err
		}
		r.identities[key] = otherReplicaID
		return store.User{}, &store.ErrResourceAlreadyExists{Resource: "user", Identifier: params.Email}
	}
	if _, exists := r.identities[key]; exists {
		return store.User{}, &store.ErrResourceAlreadyExists{Resource: "sso identity", Identifier: subject}
	}
	user, err := r.users.InsertUser(ctx, params)
	if err != nil {
		return store.User{}, err
	}
	r.identities[key] = user.Id
	return user, nil
}

func (r *fakeSSORepo) TouchLastLogin(_ context.Context, _ string, _ string) error {
	r.touchCalls++
	return nil
}

// fakeIdP is a minimal OIDC provider: discovery, JWKS and a token endpoint
// answering RS256-signed id_tokens carrying whatever claims the test sets.
type fakeIdP struct {
	key           *rsa.PrivateKey
	server        *httptest.Server
	issuer        string
	discoveryHits int
	claims        jwt.MapClaims
	lastTokenForm url.Values
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	idp := &fakeIdP{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		idp.discoveryHits++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                idp.issuer,
			"authorization_endpoint":                idp.issuer + "/auth",
			"token_endpoint":                        idp.issuer + "/token",
			"jwks_uri":                              idp.issuer + "/keys",
			"userinfo_endpoint":                     idp.issuer + "/userinfo",
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]any{{
				"kty": "RSA",
				"alg": "RS256",
				"use": "sig",
				"kid": "test-key",
				"n":   base64.RawURLEncoding.EncodeToString(idp.key.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(idp.key.E)).Bytes()),
			}},
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		idp.lastTokenForm = r.PostForm
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, idp.claims)
		token.Header["kid"] = "test-key"
		signed, err := token.SignedString(idp.key)
		require.NoError(t, err)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "test-access-token",
			"token_type":   "Bearer",
			"expires_in":   3600,
			"id_token":     signed,
		})
	})
	idp.server = httptest.NewServer(mux)
	idp.issuer = idp.server.URL
	t.Cleanup(idp.server.Close)
	return idp
}

const (
	testClientID = "test-client-id"
	testSubject  = "subject-1"
	testEmail    = "member@acme.com"
)

func testConfigFor(idp *fakeIdP) *SSOConfig {
	return &SSOConfig{
		Issuer:       idp.issuer,
		ClientID:     testClientID,
		ClientSecret: "test-client-secret",
		ProviderName: "Test IdP",
		Scopes:       "openid profile email",
		Enabled:      true,
		GroupsClaim:  "groups",
	}
}

func newTestService(t *testing.T, repo SSORepository, users *fakeUserRepo) (*SSOService, *services.DashboardAuthService) {
	t.Helper()
	t.Setenv("JWT_SECRET", "test-secret")
	t.Setenv("BASE_URL", "http://localhost:3000")
	sessions := services.NewDashboardAuthService(users, nil)
	service := NewSSOService(repo, users, sessions)
	service.licenseValid = func() bool { return true }
	return service, sessions
}

// completeFlow drives one full login round-trip against the fake IdP. mutate
// tweaks the id_token claims after the defaults (which mirror what a real IdP
// would answer for the authorization request BeginLogin produced).
func completeFlow(t *testing.T, service *SSOService, idp *fakeIdP, mutate func(claims jwt.MapClaims)) (*services.DashboardSession, error) {
	t.Helper()
	ctx := context.Background()
	begin, err := service.BeginLogin(ctx)
	require.NoError(t, err)
	authURL, err := url.Parse(begin.AuthURL)
	require.NoError(t, err)
	query := authURL.Query()
	claims := jwt.MapClaims{
		"iss":            idp.issuer,
		"sub":            testSubject,
		"aud":            testClientID,
		"exp":            time.Now().Add(5 * time.Minute).Unix(),
		"iat":            time.Now().Unix(),
		"nonce":          query.Get("nonce"),
		"email":          testEmail,
		"email_verified": true,
	}
	if mutate != nil {
		mutate(claims)
	}
	idp.claims = claims
	return service.CompleteLogin(ctx, begin.FlowToken, query.Get("state"), "test-code")
}

func TestBeginLoginBuildsAuthorizationURL(t *testing.T) {
	idp := newFakeIdP(t)
	users := newFakeUserRepo()
	service, _ := newTestService(t, newFakeSSORepo(users, testConfigFor(idp)), users)

	begin, err := service.BeginLogin(context.Background())
	require.NoError(t, err)

	authURL, err := url.Parse(begin.AuthURL)
	require.NoError(t, err)
	assert.Equal(t, idp.issuer+"/auth", authURL.Scheme+"://"+authURL.Host+authURL.Path)
	query := authURL.Query()
	assert.Equal(t, testClientID, query.Get("client_id"))
	assert.Equal(t, "code", query.Get("response_type"))
	assert.Equal(t, "http://localhost:3000/auth/sso/callback", query.Get("redirect_uri"))
	assert.Contains(t, strings.Fields(query.Get("scope")), "openid")
	assert.NotEmpty(t, query.Get("state"))
	assert.NotEmpty(t, query.Get("nonce"))
	assert.Equal(t, "S256", query.Get("code_challenge_method"))
	assert.NotEmpty(t, query.Get("code_challenge"))

	// The flow token is a short-lived HS256 JWT carrying the per-login state;
	// its PKCE verifier must hash to the challenge sent to the IdP.
	claims := jwt.MapClaims{}
	_, err = crypto.DecodeAndExtractJWTToken("test-secret", begin.FlowToken, &claims)
	require.NoError(t, err)
	assert.Equal(t, flowSubject, claims["sub"])
	assert.Equal(t, flowClaimType, claims["type"])
	assert.Equal(t, query.Get("state"), claims["state"])
	assert.Equal(t, query.Get("nonce"), claims["nonce"])
	verifierHash := sha256.Sum256([]byte(claims["verifier"].(string)))
	assert.Equal(t, query.Get("code_challenge"), base64.RawURLEncoding.EncodeToString(verifierHash[:]))
}

func TestCompleteLoginProvisionsMember(t *testing.T) {
	idp := newFakeIdP(t)
	users := newFakeUserRepo()
	repo := newFakeSSORepo(users, testConfigFor(idp))
	service, sessions := newTestService(t, repo, users)

	session, err := completeFlow(t, service, idp, nil)
	require.NoError(t, err)
	require.NotNil(t, session)

	// The code exchange used PKCE and our authorization code.
	assert.Equal(t, "test-code", idp.lastTokenForm.Get("code"))
	assert.NotEmpty(t, idp.lastTokenForm.Get("code_verifier"))

	// A member account was provisioned: non-admin, no usable password.
	user, err := users.GetUserByEmail(context.Background(), testEmail)
	require.NoError(t, err)
	assert.False(t, user.IsAdmin)
	assert.Empty(t, user.PasswordHash)
	assert.Equal(t, user.Id, repo.identities[identityKey(idp.issuer, testSubject)])

	// The pair is an ordinary dashboard session for that account.
	principal, err := sessions.ValidateSession(session.Token)
	require.NoError(t, err)
	assert.Equal(t, user.Id, principal.UserId)
	assert.Equal(t, testEmail, principal.Email)
	assert.False(t, principal.IsAdmin)
	_, err = sessions.RefreshSession(context.Background(), session.RefreshToken)
	require.NoError(t, err)
}

func TestCompleteLoginPrefersKnownSubjectOverEmail(t *testing.T) {
	idp := newFakeIdP(t)
	users := newFakeUserRepo()
	repo := newFakeSSORepo(users, testConfigFor(idp))
	service, sessions := newTestService(t, repo, users)

	_, err := completeFlow(t, service, idp, nil)
	require.NoError(t, err)
	provisioned, err := users.GetUserByEmail(context.Background(), testEmail)
	require.NoError(t, err)

	// Same subject comes back with a changed email at the IdP: the mapping by
	// subject wins, no second account appears, and the sign-in is recorded.
	session, err := completeFlow(t, service, idp, func(claims jwt.MapClaims) {
		claims["email"] = "renamed@acme.com"
	})
	require.NoError(t, err)
	principal, err := sessions.ValidateSession(session.Token)
	require.NoError(t, err)
	assert.Equal(t, provisioned.Id, principal.UserId)
	assert.Len(t, users.users, 1)
	assert.Equal(t, 1, repo.touchCalls)
}

func TestCompleteLoginLinksExistingAccountByEmail(t *testing.T) {
	idp := newFakeIdP(t)
	users := newFakeUserRepo()
	existing, err := users.InsertUser(context.Background(), store.InsertUserParameters{
		ID: "existing-user", Email: testEmail, PasswordHash: "some-bcrypt-hash", IsAdmin: true, Enabled: true,
	})
	require.NoError(t, err)
	repo := newFakeSSORepo(users, testConfigFor(idp))
	service, sessions := newTestService(t, repo, users)

	session, err := completeFlow(t, service, idp, nil)
	require.NoError(t, err)

	// Linked, not duplicated: role and password are preserved.
	principal, err := sessions.ValidateSession(session.Token)
	require.NoError(t, err)
	assert.Equal(t, existing.Id, principal.UserId)
	assert.True(t, principal.IsAdmin)
	assert.Len(t, users.users, 1)
	assert.Equal(t, existing.Id, repo.identities[identityKey(idp.issuer, testSubject)])
	stored, err := users.GetUserByID(context.Background(), existing.Id)
	require.NoError(t, err)
	assert.Equal(t, "some-bcrypt-hash", stored.PasswordHash)
}

func TestCompleteLoginRetriesAfterConcurrentFirstSignIn(t *testing.T) {
	idp := newFakeIdP(t)
	users := newFakeUserRepo()
	repo := newFakeSSORepo(users, testConfigFor(idp))
	repo.provisionRaces = true
	service, sessions := newTestService(t, repo, users)

	session, err := completeFlow(t, service, idp, nil)
	require.NoError(t, err)

	// The retry found the account provisioned by the "other replica".
	principal, err := sessions.ValidateSession(session.Token)
	require.NoError(t, err)
	assert.Equal(t, repo.identities[identityKey(idp.issuer, testSubject)], principal.UserId)
	assert.Len(t, users.users, 1)
}

// With manual validation on, a first sign-in creates the account but does not
// let it in: the admin gets something concrete to approve on the Users page,
// and the person gets an error saying to wait rather than a generic refusal.
func TestManualUserValidationProvisionsDisabledAccounts(t *testing.T) {
	idp := newFakeIdP(t)
	users := newFakeUserRepo()
	cfg := testConfigFor(idp)
	cfg.ManualUserValidation = true
	service, sessions := newTestService(t, newFakeSSORepo(users, cfg), users)

	_, err := completeFlow(t, service, idp, nil)
	assert.ErrorIs(t, err, ErrSSOAccountPendingApproval)

	// The account exists, disabled, so an admin can find and approve it.
	provisioned, err := users.GetUserByEmail(context.Background(), testEmail)
	require.NoError(t, err)
	assert.False(t, provisioned.Enabled)
	assert.False(t, provisioned.IsAdmin)

	// Retrying before approval keeps failing, without duplicating the account.
	_, err = completeFlow(t, service, idp, nil)
	assert.ErrorIs(t, err, ErrSSOAccountPendingApproval)
	all, err := users.GetUsers(context.Background())
	require.NoError(t, err)
	assert.Len(t, all, 1)

	// Once approved, the very same flow signs in.
	require.NoError(t, users.UpdateUserEnabled(context.Background(), provisioned.Id, true))
	session, err := completeFlow(t, service, idp, nil)
	require.NoError(t, err)
	principal, err := sessions.ValidateSession(session.Token)
	require.NoError(t, err)
	assert.Equal(t, testEmail, principal.Email)
}

// Turning manual validation on must not strand people who already had access:
// linking an identity onto an existing account leaves its enabled flag alone.
func TestManualUserValidationLeavesExistingAccountsAlone(t *testing.T) {
	idp := newFakeIdP(t)
	users := newFakeUserRepo()
	existing, err := users.InsertUser(context.Background(), store.InsertUserParameters{
		ID: "existing-user", Email: testEmail, PasswordHash: "some-bcrypt-hash", IsAdmin: true, Enabled: true,
	})
	require.NoError(t, err)
	cfg := testConfigFor(idp)
	cfg.ManualUserValidation = true
	service, _ := newTestService(t, newFakeSSORepo(users, cfg), users)

	_, err = completeFlow(t, service, idp, nil)
	require.NoError(t, err)

	linked, err := users.GetUserByID(context.Background(), existing.Id)
	require.NoError(t, err)
	assert.True(t, linked.Enabled)
}

// A disabled account is refused whatever put it in that state: manual
// validation is one way in, an admin revoking access is the other.
func TestCompleteLoginRejectsRevokedAccount(t *testing.T) {
	idp := newFakeIdP(t)
	users := newFakeUserRepo()
	service, _ := newTestService(t, newFakeSSORepo(users, testConfigFor(idp)), users)

	_, err := completeFlow(t, service, idp, nil)
	require.NoError(t, err)

	provisioned, err := users.GetUserByEmail(context.Background(), testEmail)
	require.NoError(t, err)
	require.NoError(t, users.UpdateUserEnabled(context.Background(), provisioned.Id, false))

	_, err = completeFlow(t, service, idp, nil)
	assert.ErrorIs(t, err, ErrSSOAccountPendingApproval)
}

func TestCompleteLoginRejectsNonceMismatch(t *testing.T) {
	idp := newFakeIdP(t)
	users := newFakeUserRepo()
	service, _ := newTestService(t, newFakeSSORepo(users, testConfigFor(idp)), users)

	_, err := completeFlow(t, service, idp, func(claims jwt.MapClaims) {
		claims["nonce"] = "not-the-minted-nonce"
	})
	assert.ErrorContains(t, err, "nonce mismatch")
	assert.Empty(t, users.users)
}

func TestCompleteLoginRejectsStateMismatch(t *testing.T) {
	idp := newFakeIdP(t)
	users := newFakeUserRepo()
	service, _ := newTestService(t, newFakeSSORepo(users, testConfigFor(idp)), users)

	begin, err := service.BeginLogin(context.Background())
	require.NoError(t, err)
	_, err = service.CompleteLogin(context.Background(), begin.FlowToken, "tampered-state", "test-code")
	assert.ErrorContains(t, err, "state mismatch")
}

func TestCompleteLoginRejectsForeignAndExpiredFlowTokens(t *testing.T) {
	idp := newFakeIdP(t)
	users := newFakeUserRepo()
	service, _ := newTestService(t, newFakeSSORepo(users, testConfigFor(idp)), users)

	// A dashboard session token signed with the same secret is not a flow
	// token: the sub/type claims fence the two token kinds off.
	foreign, err := crypto.GenerateJWTToken("test-secret", jwt.MapClaims{
		"sub": "admin-dashboard", "type": "token", "exp": time.Now().Add(time.Hour).Unix(),
	})
	require.NoError(t, err)
	_, err = service.CompleteLogin(context.Background(), foreign, "state", "code")
	assert.ErrorContains(t, err, "invalid sso flow token")

	expired, err := crypto.GenerateJWTToken("test-secret", jwt.MapClaims{
		"sub": flowSubject, "type": flowClaimType,
		"exp":   time.Now().Add(-time.Minute).Unix(),
		"state": "s", "nonce": "n", "verifier": "v",
	})
	require.NoError(t, err)
	_, err = service.CompleteLogin(context.Background(), expired, "s", "code")
	assert.ErrorContains(t, err, "invalid sso flow token")

	tampered, err := crypto.GenerateJWTToken("another-secret", jwt.MapClaims{
		"sub": flowSubject, "type": flowClaimType,
		"exp":   time.Now().Add(time.Minute).Unix(),
		"state": "s", "nonce": "n", "verifier": "v",
	})
	require.NoError(t, err)
	_, err = service.CompleteLogin(context.Background(), tampered, "s", "code")
	assert.ErrorContains(t, err, "invalid sso flow token")
}

func TestLoginFlowGuards(t *testing.T) {
	idp := newFakeIdP(t)
	users := newFakeUserRepo()
	ctx := context.Background()

	// Stateless mode: no repository at all.
	service, _ := newTestService(t, nil, users)
	_, err := service.BeginLogin(ctx)
	assert.ErrorIs(t, err, ErrSSORequiresControlPlane)
	assert.False(t, service.Enabled(ctx))

	// Not configured yet.
	service, _ = newTestService(t, newFakeSSORepo(users, nil), users)
	_, err = service.BeginLogin(ctx)
	assert.ErrorIs(t, err, ErrSSONotConfigured)

	// Configured but toggled off.
	disabledCfg := testConfigFor(idp)
	disabledCfg.Enabled = false
	service, _ = newTestService(t, newFakeSSORepo(users, disabledCfg), users)
	_, err = service.BeginLogin(ctx)
	assert.ErrorIs(t, err, ErrSSONotConfigured)
	assert.False(t, service.Enabled(ctx))

	// No valid license: both halves of the flow refuse server-side.
	service, _ = newTestService(t, newFakeSSORepo(users, testConfigFor(idp)), users)
	service.licenseValid = func() bool { return false }
	_, err = service.BeginLogin(ctx)
	assert.ErrorIs(t, err, ErrSSORequiresValidLicense)
	_, err = service.CompleteLogin(ctx, "whatever", "state", "code")
	assert.ErrorIs(t, err, ErrSSORequiresValidLicense)
	assert.False(t, service.Enabled(ctx))
	assert.False(t, service.PublicConfig(ctx).Enabled)
}

func TestCompleteLoginFallsBackToPreferredUsername(t *testing.T) {
	idp := newFakeIdP(t)
	users := newFakeUserRepo()
	// preferred_username is never covered by email_verified, so it is only
	// usable when the admin trusts the provider (the Entra path).
	cfg := testConfigFor(idp)
	cfg.TrustUnverifiedEmail = true
	service, _ := newTestService(t, newFakeSSORepo(users, cfg), users)

	// Entra without the optional email claim: the address usually lives in
	// preferred_username. Normalization applies on the way in.
	_, err := completeFlow(t, service, idp, func(claims jwt.MapClaims) {
		delete(claims, "email")
		claims["preferred_username"] = " Member@ACME.com "
	})
	require.NoError(t, err)
	_, err = users.GetUserByEmail(context.Background(), testEmail)
	assert.NoError(t, err)
}

// The verified-email gate protects domain authorization and account
// lookup/linking: an unverified email is refused before any of them, so it
// can neither pass a domain allowlist nor take over an existing account.
func TestCompleteLoginRejectsUnverifiedEmail(t *testing.T) {
	idp := newFakeIdP(t)
	users := newFakeUserRepo()
	// An existing account an attacker would try to take over by asserting its
	// address from a tenant they control.
	victim, err := users.InsertUser(context.Background(), store.InsertUserParameters{
		ID: "victim", Email: testEmail, PasswordHash: "victim-hash", IsAdmin: true, Enabled: true,
	})
	require.NoError(t, err)
	repo := newFakeSSORepo(users, testConfigFor(idp))
	service, _ := newTestService(t, repo, users)

	// email_verified explicitly false: refused.
	_, err = completeFlow(t, service, idp, func(claims jwt.MapClaims) {
		claims["email_verified"] = false
	})
	assert.ErrorIs(t, err, ErrSSOEmailUnverified)

	// email_verified absent entirely (the Entra shape): also refused by default.
	_, err = completeFlow(t, service, idp, func(claims jwt.MapClaims) {
		delete(claims, "email_verified")
	})
	assert.ErrorIs(t, err, ErrSSOEmailUnverified)

	// No identity was linked and the victim account is untouched.
	assert.Empty(t, repo.identities)
	stored, err := users.GetUserByID(context.Background(), victim.Id)
	require.NoError(t, err)
	assert.Equal(t, "victim-hash", stored.PasswordHash)
}

func TestCompleteLoginTrustsUnverifiedEmailWhenConfigured(t *testing.T) {
	idp := newFakeIdP(t)
	users := newFakeUserRepo()
	cfg := testConfigFor(idp)
	cfg.TrustUnverifiedEmail = true
	service, _ := newTestService(t, newFakeSSORepo(users, cfg), users)

	// With the provider explicitly trusted, an absent email_verified provisions
	// normally (the documented single-tenant Entra path).
	_, err := completeFlow(t, service, idp, func(claims jwt.MapClaims) {
		delete(claims, "email_verified")
	})
	require.NoError(t, err)
	_, err = users.GetUserByEmail(context.Background(), testEmail)
	assert.NoError(t, err)
}

// email_verified is accepted both as a JSON boolean and as the string "true",
// since IdPs differ.
func TestCompleteLoginAcceptsStringEmailVerified(t *testing.T) {
	idp := newFakeIdP(t)
	users := newFakeUserRepo()
	service, _ := newTestService(t, newFakeSSORepo(users, testConfigFor(idp)), users)

	_, err := completeFlow(t, service, idp, func(claims jwt.MapClaims) {
		claims["email_verified"] = "true"
	})
	assert.NoError(t, err)
}

func TestCompleteLoginRequiresAnEmail(t *testing.T) {
	idp := newFakeIdP(t)
	users := newFakeUserRepo()
	service, _ := newTestService(t, newFakeSSORepo(users, testConfigFor(idp)), users)

	// A UPN that is not an address (common for on-prem synced Entra users)
	// must not be mistaken for one.
	_, err := completeFlow(t, service, idp, func(claims jwt.MapClaims) {
		delete(claims, "email")
		claims["preferred_username"] = "DOMAIN\\member"
	})
	assert.ErrorIs(t, err, ErrSSOEmailMissing)
	assert.Empty(t, users.users)
}

func TestCompleteLoginEnforcesAllowedEmailDomains(t *testing.T) {
	idp := newFakeIdP(t)
	users := newFakeUserRepo()
	cfg := testConfigFor(idp)
	cfg.AllowedEmailDomains = []string{"acme.com"}
	service, _ := newTestService(t, newFakeSSORepo(users, cfg), users)

	_, err := completeFlow(t, service, idp, func(claims jwt.MapClaims) {
		claims["email"] = "intruder@other.com"
	})
	assert.ErrorIs(t, err, ErrSSOAccessRestricted)
	assert.Empty(t, users.users)

	_, err = completeFlow(t, service, idp, nil)
	assert.NoError(t, err)
}

func TestCompleteLoginEnforcesAllowedGroups(t *testing.T) {
	idp := newFakeIdP(t)
	users := newFakeUserRepo()
	cfg := testConfigFor(idp)
	cfg.AllowedGroups = []string{"dashboard-users", "eng"}
	service, _ := newTestService(t, newFakeSSORepo(users, cfg), users)

	// Wrong groups.
	_, err := completeFlow(t, service, idp, func(claims jwt.MapClaims) {
		claims["groups"] = []string{"sales"}
	})
	assert.ErrorIs(t, err, ErrSSOAccessRestricted)

	// Claim absent entirely (e.g. groups claim not configured at the IdP).
	_, err = completeFlow(t, service, idp, nil)
	assert.ErrorIs(t, err, ErrSSOAccessRestricted)
	assert.Empty(t, users.users)

	// One matching group among several is enough.
	_, err = completeFlow(t, service, idp, func(claims jwt.MapClaims) {
		claims["groups"] = []string{"sales", "eng"}
	})
	assert.NoError(t, err)

	// A single-string claim value is accepted too.
	_, err = completeFlow(t, service, idp, func(claims jwt.MapClaims) {
		claims["groups"] = "dashboard-users"
	})
	assert.NoError(t, err)
}

func TestCompleteLoginReadsConfiguredGroupsClaim(t *testing.T) {
	idp := newFakeIdP(t)
	users := newFakeUserRepo()
	cfg := testConfigFor(idp)
	cfg.AllowedGroups = []string{"eng"}
	cfg.GroupsClaim = "roles"
	service, _ := newTestService(t, newFakeSSORepo(users, cfg), users)

	// Groups under the default claim name do not count when the config says
	// the IdP uses another one.
	_, err := completeFlow(t, service, idp, func(claims jwt.MapClaims) {
		claims["groups"] = []string{"eng"}
	})
	assert.ErrorIs(t, err, ErrSSOAccessRestricted)

	_, err = completeFlow(t, service, idp, func(claims jwt.MapClaims) {
		claims["roles"] = []string{"eng"}
	})
	assert.NoError(t, err)
}

func TestSaveConfigValidatesDiscoveryBeforePersisting(t *testing.T) {
	idp := newFakeIdP(t)
	users := newFakeUserRepo()
	repo := newFakeSSORepo(users, nil)
	service, _ := newTestService(t, repo, users)

	// A server without a discovery document: the IdP error surfaces to the
	// admin and nothing is stored.
	deadServer := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(deadServer.Close)
	_, err := service.SaveConfig(context.Background(), SaveConfigInput{
		Issuer: deadServer.URL, ClientID: testClientID, ClientSecret: "secret", Enabled: true,
	})
	validationErr := (*ConfigValidationError)(nil)
	require.ErrorAs(t, err, &validationErr)
	assert.ErrorContains(t, err, "OIDC discovery failed")
	assert.Equal(t, 0, repo.saveCalls)
	assert.Nil(t, repo.cfg)

	// Against the live fake IdP the same input persists, with defaults filled.
	view, err := service.SaveConfig(context.Background(), SaveConfigInput{
		Issuer: idp.issuer, ClientID: testClientID, ClientSecret: "secret", Enabled: true,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, repo.saveCalls)
	assert.Equal(t, "SSO", view.ProviderName)
	assert.Equal(t, "openid profile email", view.Scopes)
	assert.Equal(t, "groups", view.GroupsClaim)
	assert.True(t, view.HasClientSecret)
	assert.Equal(t, "http://localhost:3000/auth/sso/callback", view.RedirectURI)
}

func TestSaveConfigKeepsStoredSecretWhenLeftEmpty(t *testing.T) {
	idp := newFakeIdP(t)
	users := newFakeUserRepo()
	repo := newFakeSSORepo(users, testConfigFor(idp))
	service, _ := newTestService(t, repo, users)

	_, err := service.SaveConfig(context.Background(), SaveConfigInput{
		Issuer: idp.issuer, ClientID: "rotated-client-id", ClientSecret: "", Enabled: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "test-client-secret", repo.cfg.ClientSecret)
	assert.Equal(t, "rotated-client-id", repo.cfg.ClientID)

	// Without any stored configuration an empty secret cannot mean anything.
	emptyRepo := newFakeSSORepo(users, nil)
	service, _ = newTestService(t, emptyRepo, users)
	_, err = service.SaveConfig(context.Background(), SaveConfigInput{
		Issuer: idp.issuer, ClientID: testClientID, Enabled: true,
	})
	assert.ErrorContains(t, err, "client secret is required")
}

// Disabling SSO must work even while the IdP is unreachable: that is the
// dashboard break-glass path. Discovery only guards enabled configurations.
func TestSaveConfigSkipsDiscoveryWhenDisabled(t *testing.T) {
	users := newFakeUserRepo()
	repo := newFakeSSORepo(users, nil)
	service, _ := newTestService(t, repo, users)
	deadIdP, hits := newDiscoveryCounter(t, 0, true)

	view, err := service.SaveConfig(context.Background(), SaveConfigInput{
		Issuer: deadIdP.URL, ClientID: testClientID, ClientSecret: "secret", Enabled: false,
	})
	require.NoError(t, err)
	assert.False(t, view.Enabled)
	assert.Equal(t, 1, repo.saveCalls)
	assert.Equal(t, int32(0), hits.Load())

	// Re-enabling the same configuration runs the live check again.
	_, err = service.SaveConfig(context.Background(), SaveConfigInput{
		Issuer: deadIdP.URL, ClientID: testClientID, Enabled: true,
	})
	require.Error(t, err)
	assert.ErrorContains(t, err, "OIDC discovery failed")
	assert.Equal(t, int32(1), hits.Load())
}

func TestSaveConfigRequiresLicense(t *testing.T) {
	idp := newFakeIdP(t)
	users := newFakeUserRepo()
	repo := newFakeSSORepo(users, nil)
	service, _ := newTestService(t, repo, users)
	service.licenseValid = func() bool { return false }

	_, err := service.SaveConfig(context.Background(), SaveConfigInput{
		Issuer: idp.issuer, ClientID: testClientID, ClientSecret: "secret",
	})
	assert.ErrorIs(t, err, ErrSSORequiresValidLicense)
	assert.ErrorIs(t, service.DeleteConfig(context.Background()), ErrSSORequiresValidLicense)
}

func TestIssuerChangeRefreshesDiscoveryCache(t *testing.T) {
	firstIdP := newFakeIdP(t)
	secondIdP := newFakeIdP(t)
	users := newFakeUserRepo()
	repo := newFakeSSORepo(users, testConfigFor(firstIdP))
	service, _ := newTestService(t, repo, users)

	_, err := service.BeginLogin(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, firstIdP.discoveryHits)

	// Repointing the issuer re-discovers; the old cache entry is gone.
	_, err = service.SaveConfig(context.Background(), SaveConfigInput{
		Issuer: secondIdP.issuer, ClientID: testClientID, ClientSecret: "secret", Enabled: true,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, secondIdP.discoveryHits)

	begin, err := service.BeginLogin(context.Background())
	require.NoError(t, err)
	assert.Contains(t, begin.AuthURL, secondIdP.issuer)
	// Served from the cache primed by SaveConfig: no extra discovery.
	assert.Equal(t, 1, secondIdP.discoveryHits)
	assert.Equal(t, 1, firstIdP.discoveryHits)
}

func TestNormalizeConfigInput(t *testing.T) {
	valid := SaveConfigInput{Issuer: "https://idp.acme.com", ClientID: "client", ClientSecret: "secret"}

	cases := []struct {
		name    string
		mutate  func(input *SaveConfigInput)
		message string
	}{
		{"empty issuer", func(i *SaveConfigInput) { i.Issuer = "  " }, "issuer URL is required"},
		{"issuer without host", func(i *SaveConfigInput) { i.Issuer = "not a url" }, "must be a valid URL"},
		{"plain http issuer", func(i *SaveConfigInput) { i.Issuer = "http://idp.acme.com" }, "must use https"},
		{"empty client id", func(i *SaveConfigInput) { i.ClientID = "" }, "client id is required"},
		{"scopes without openid", func(i *SaveConfigInput) { i.Scopes = "profile email" }, "must include \"openid\""},
		{"domain with spaces", func(i *SaveConfigInput) { i.AllowedEmailDomains = []string{"ac me.com"} }, "not a valid email domain"},
		{"domain with user part", func(i *SaveConfigInput) { i.AllowedEmailDomains = []string{"jane@acme.com"} }, "not a valid email domain"},
		{"provider name too long", func(i *SaveConfigInput) { i.ProviderName = strings.Repeat("x", 61) }, "under 60 characters"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			input := valid
			testCase.mutate(&input)
			_, err := normalizeConfigInput(input)
			validationErr := (*ConfigValidationError)(nil)
			require.ErrorAs(t, err, &validationErr)
			assert.ErrorContains(t, err, testCase.message)
		})
	}

	// http is tolerated for loopback issuers (local Keycloak/Dex testing).
	input := valid
	input.Issuer = "http://localhost:8080/realms/test"
	_, err := normalizeConfigInput(input)
	assert.NoError(t, err)

	// Normalization: pasted "@Domain" entries, duplicate groups, extra spaces.
	input = valid
	input.ProviderName = "  Microsoft  "
	input.Scopes = "  openid   profile "
	input.AllowedEmailDomains = []string{"@Acme.COM", "acme.com", " "}
	input.AllowedGroups = []string{" eng ", "eng", ""}
	cfg, err := normalizeConfigInput(input)
	require.NoError(t, err)
	assert.Equal(t, "Microsoft", cfg.ProviderName)
	assert.Equal(t, "openid profile", cfg.Scopes)
	assert.Equal(t, []string{"acme.com"}, cfg.AllowedEmailDomains)
	assert.Equal(t, []string{"eng"}, cfg.AllowedGroups)
	assert.Equal(t, "groups", cfg.GroupsClaim)
}

func TestGetAdminConfigSurvivesUnreadableSecret(t *testing.T) {
	idp := newFakeIdP(t)
	users := newFakeUserRepo()
	cfg := testConfigFor(idp)
	cfg.ClientSecret = ""
	repo := newFakeSSORepo(users, cfg)
	repo.cfgErr = ErrClientSecretUnreadable
	service, _ := newTestService(t, repo, users)

	// The card still renders the configuration and prompts for the secret.
	view, err := service.GetAdminConfig(context.Background())
	require.NoError(t, err)
	assert.False(t, view.HasClientSecret)
	assert.Equal(t, idp.issuer, view.Issuer)

	// Sign-ins are impossible until the secret is re-entered, and the
	// enforcement signal drops with them: password login stays available.
	assert.False(t, service.Enabled(context.Background()))
	_, err = service.BeginLogin(context.Background())
	assert.ErrorIs(t, err, ErrClientSecretUnreadable)

	// Re-submitting without a secret cannot silently keep the unreadable one.
	_, err = service.SaveConfig(context.Background(), SaveConfigInput{
		Issuer: idp.issuer, ClientID: testClientID, Enabled: true,
	})
	assert.ErrorIs(t, err, ErrClientSecretUnreadable)
}

// newDiscoveryCounter is a discovery-only endpoint with an atomic hit
// counter, optional latency and optional failure, for exercising the
// provider-cache concurrency without the full fake IdP.
func newDiscoveryCounter(t *testing.T, delay time.Duration, fail bool) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	hits := &atomic.Int32{}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if fail {
			http.Error(w, "discovery unavailable", http.StatusInternalServerError)
			return
		}
		time.Sleep(delay)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 server.URL,
			"authorization_endpoint": server.URL + "/auth",
			"token_endpoint":         server.URL + "/token",
			"jwks_uri":               server.URL + "/keys",
		})
	}))
	t.Cleanup(server.Close)
	return server, hits
}

// Concurrent sign-ins on a cold cache must share one discovery request, and
// none of them may hold the cache mutex during the network fetch.
func TestProviderDiscoveryIsSingleFlight(t *testing.T) {
	users := newFakeUserRepo()
	service, _ := newTestService(t, newFakeSSORepo(users, nil), users)
	server, hits := newDiscoveryCounter(t, 150*time.Millisecond, false)

	var wg sync.WaitGroup
	discoveryErrors := make([]error, 8)
	for i := range discoveryErrors {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, discoveryErrors[index] = service.provider(server.URL)
		}(i)
	}
	wg.Wait()
	for _, err := range discoveryErrors {
		require.NoError(t, err)
	}
	assert.Equal(t, int32(1), hits.Load())

	// And the result is cached: no further request.
	_, err := service.provider(server.URL)
	require.NoError(t, err)
	assert.Equal(t, int32(1), hits.Load())
}

func TestProviderDiscoveryFailureCooldownAndSaveBypass(t *testing.T) {
	users := newFakeUserRepo()
	service, _ := newTestService(t, newFakeSSORepo(users, nil), users)
	server, hits := newDiscoveryCounter(t, 0, true)

	_, err := service.provider(server.URL)
	require.Error(t, err)
	assert.Equal(t, int32(1), hits.Load())

	// Within the cooldown the sign-in path fails fast, without a new request.
	_, err = service.provider(server.URL)
	require.Error(t, err)
	assert.Equal(t, int32(1), hits.Load())

	// The admin's "save and test" path always performs a live discovery.
	_, err = service.discoverProvider(server.URL)
	require.Error(t, err)
	assert.Equal(t, int32(2), hits.Load())

	// The cooldown is issuer-specific: another issuer is attempted normally.
	otherServer, otherHits := newDiscoveryCounter(t, 0, true)
	_, err = service.provider(otherServer.URL)
	require.Error(t, err)
	assert.Equal(t, int32(1), otherHits.Load())
}

func TestPublicConfigAndEnabled(t *testing.T) {
	idp := newFakeIdP(t)
	users := newFakeUserRepo()
	service, _ := newTestService(t, newFakeSSORepo(users, testConfigFor(idp)), users)

	public := service.PublicConfig(context.Background())
	assert.True(t, public.Enabled)
	assert.Equal(t, "Test IdP", public.ProviderName)
	assert.True(t, service.Enabled(context.Background()))

	// A failing config read collapses to "disabled" instead of an error:
	// the enforcement signal must fail open for password sign-ins.
	repo := newFakeSSORepo(users, testConfigFor(idp))
	repo.cfgErr = errors.New("database unreachable")
	service, _ = newTestService(t, repo, users)
	assert.False(t, service.Enabled(context.Background()))
	assert.False(t, service.PublicConfig(context.Background()).Enabled)
}
