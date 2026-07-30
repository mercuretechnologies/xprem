package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"testing"
	"time"
	"xprem/internal/crypto"
	"xprem/internal/store"

	"github.com/golang-jwt/jwt/v5"
)

const testVerifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"

type fakeTokenUserRepo struct {
	users  map[string]store.User
	bumped []string
}

func (f *fakeTokenUserRepo) GetUserByID(_ context.Context, id string) (store.User, error) {
	user, ok := f.users[id]
	if !ok {
		return store.User{}, &store.ErrResourceNotFound{Resource: "user", Identifier: id}
	}
	return user, nil
}

func (f *fakeTokenUserRepo) BumpUserSessionVersion(_ context.Context, id string) error {
	f.bumped = append(f.bumped, id)
	user := f.users[id]
	user.SessionVersion++
	f.users[id] = user
	return nil
}

// fakeRefreshLedger mirrors the semantics of PostgresRefreshTokenStore.
type fakeRefreshLedger struct {
	rows map[string]*store.RefreshToken
}

func newFakeRefreshLedger() *fakeRefreshLedger {
	return &fakeRefreshLedger{rows: map[string]*store.RefreshToken{}}
}

func (f *fakeRefreshLedger) InsertRefreshToken(_ context.Context, params store.InsertRefreshTokenParameters) error {
	f.rows[params.ID] = &store.RefreshToken{
		Id:        params.ID,
		UserId:    params.UserID,
		FamilyId:  params.FamilyID,
		ExpiresAt: params.ExpiresAt,
	}
	return nil
}

func (f *fakeRefreshLedger) RotateRefreshToken(_ context.Context, params store.RotateRefreshTokenParameters) (store.RefreshToken, error) {
	row, ok := f.rows[params.OldID]
	if !ok || row.UsedAt != nil || time.Now().After(row.ExpiresAt) {
		return store.RefreshToken{}, &store.ErrResourceNotFound{Resource: "refresh token", Identifier: params.OldID}
	}
	now := time.Now()
	row.UsedAt = &now
	successor := params.NewID
	row.ReplacedBy = &successor
	f.rows[successor] = &store.RefreshToken{
		Id:        successor,
		UserId:    row.UserId,
		FamilyId:  row.FamilyId,
		ExpiresAt: params.ExpiresAt,
	}
	return *row, nil
}

func (f *fakeRefreshLedger) GetRefreshToken(_ context.Context, id string, replayGrace time.Duration) (store.RefreshToken, error) {
	row, ok := f.rows[id]
	if !ok {
		return store.RefreshToken{}, &store.ErrResourceNotFound{Resource: "refresh token", Identifier: id}
	}
	result := *row
	result.UsedRecently = row.UsedAt != nil && time.Since(*row.UsedAt) < replayGrace
	return result, nil
}

func (f *fakeRefreshLedger) DeleteRefreshTokenFamily(_ context.Context, familyId string) error {
	for id, row := range f.rows {
		if row.FamilyId == familyId {
			delete(f.rows, id)
		}
	}
	return nil
}

func (f *fakeRefreshLedger) DeleteExpiredRefreshTokens(_ context.Context, _ string) error {
	return nil
}

// tokenTestSetup returns a service wired with all fakes plus a freshly
// consented code ready to exchange.
func tokenTestSetup(t *testing.T) (*OAuthService, *fakeTokenUserRepo, *fakeRefreshLedger, ExchangeAuthorizationCodeRequest) {
	t.Helper()
	t.Setenv("BASE_URL", "https://ota.example.com")
	t.Setenv("JWT_SECRET", "test-secret")
	clientRepo := &fakeClientRepo{}
	codeRepo := &fakeCodeRepo{}
	userRepo := &fakeTokenUserRepo{users: map[string]store.User{
		"user-1": {Id: "user-1", Email: "a@b.c", Enabled: true, SessionVersion: 3},
	}}
	ledger := newFakeRefreshLedger()
	service := NewOAuthService(clientRepo, codeRepo, ledger, userRepo)

	clientID := seedClient(t, clientRepo, "http://127.0.0.1:53422/callback")
	digest := sha256.Sum256([]byte(testVerifier))
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])
	redirectURL, err := service.CreateAuthorizationCode(context.Background(), "user-1", AuthorizationRequest{
		ClientID:            clientID,
		RedirectURI:         "http://127.0.0.1:53422/callback",
		ResponseType:        "code",
		CodeChallenge:       challenge,
		CodeChallengeMethod: "S256",
	})
	if err != nil {
		t.Fatal(err)
	}
	code := codeRepo.inserted[len(codeRepo.inserted)-1].ID
	_ = redirectURL

	return service, userRepo, ledger, ExchangeAuthorizationCodeRequest{
		Code:         code,
		ClientID:     clientID,
		RedirectURI:  "http://127.0.0.1:53422/callback",
		CodeVerifier: testVerifier,
	}
}

func TestExchangeAuthorizationCode(t *testing.T) {
	service, _, ledger, req := tokenTestSetup(t)

	response, err := service.ExchangeAuthorizationCode(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	principal, _, err := service.AuthenticateMCPToken(context.Background(), response.AccessToken)
	if err != nil {
		t.Fatalf("the minted access token must verify: %v", err)
	}
	if principal.UserId != "user-1" {
		t.Errorf("access token minted for the wrong user: %+v", principal)
	}
	if response.RefreshToken == "" || len(ledger.rows) != 1 {
		t.Error("the exchange must open a refresh chain")
	}

	// The code is spent: a second exchange must fail.
	if _, err := service.ExchangeAuthorizationCode(context.Background(), req); err != ErrInvalidGrant {
		t.Fatalf("a code must be single-use, got %v", err)
	}
}

func TestExchangeRejections(t *testing.T) {
	mutations := map[string]func(*ExchangeAuthorizationCodeRequest){
		"wrong verifier": func(r *ExchangeAuthorizationCodeRequest) {
			r.CodeVerifier = "wrong-wrong-wrong-wrong-wrong-wrong-wrong-wrong"
		},
		"wrong client":       func(r *ExchangeAuthorizationCodeRequest) { r.ClientID = "other-client" },
		"wrong redirect_uri": func(r *ExchangeAuthorizationCodeRequest) { r.RedirectURI = "http://127.0.0.1:9/cb" },
		"foreign resource":   func(r *ExchangeAuthorizationCodeRequest) { r.Resource = "https://other.example.com/mcp" },
		"unknown code":       func(r *ExchangeAuthorizationCodeRequest) { r.Code = "11111111-1111-1111-1111-111111111111" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			service, _, _, req := tokenTestSetup(t)
			mutate(&req)
			if _, err := service.ExchangeAuthorizationCode(context.Background(), req); err != ErrInvalidGrant {
				t.Fatalf("expected ErrInvalidGrant, got %v", err)
			}
		})
	}
}

func TestRefreshRotation(t *testing.T) {
	service, _, _, req := tokenTestSetup(t)
	first, err := service.ExchangeAuthorizationCode(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	second, err := service.RefreshAccessToken(context.Background(), first.RefreshToken)
	if err != nil {
		t.Fatalf("a fresh refresh token must rotate: %v", err)
	}
	if _, _, err := service.AuthenticateMCPToken(context.Background(), second.AccessToken); err != nil {
		t.Fatalf("the refreshed access token must verify: %v", err)
	}
	if second.RefreshToken == first.RefreshToken {
		t.Fatal("rotation must issue a new refresh token")
	}
}

func TestRefreshReplayRevokesEverything(t *testing.T) {
	service, userRepo, ledger, req := tokenTestSetup(t)
	first, err := service.ExchangeAuthorizationCode(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RefreshAccessToken(context.Background(), first.RefreshToken); err != nil {
		t.Fatal(err)
	}

	// Backdate the spend beyond the replay grace: the re-presentation is now
	// a theft signal, not a race.
	for _, row := range ledger.rows {
		if row.UsedAt != nil {
			spent := time.Now().Add(-2 * refreshReplayGrace)
			row.UsedAt = &spent
		}
	}
	if _, err := service.RefreshAccessToken(context.Background(), first.RefreshToken); err != ErrInvalidGrant {
		t.Fatalf("expected ErrInvalidGrant on replay, got %v", err)
	}
	if len(userRepo.bumped) != 1 || userRepo.bumped[0] != "user-1" {
		t.Error("a replay must bump the account's session version")
	}
	if len(ledger.rows) != 0 {
		t.Error("a replay must revoke the whole family")
	}
}

func TestRefreshRejectsDashboardToken(t *testing.T) {
	service, _, ledger, req := tokenTestSetup(t)
	if _, err := service.ExchangeAuthorizationCode(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	// Same ledger, other audience: a dashboard refresh JWT naming a live MCP
	// row must be refused before the ledger is ever consulted.
	var mcpRowId string
	for id := range ledger.rows {
		mcpRowId = id
	}
	dashboardToken, err := crypto.GenerateJWTToken("test-secret", jwt.MapClaims{
		"sub":    "admin-dashboard",
		"type":   "refreshToken",
		"exp":    time.Now().Add(time.Hour).Unix(),
		"userId": "user-1",
		"sv":     3,
		"jti":    mcpRowId,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RefreshAccessToken(context.Background(), dashboardToken); err != ErrInvalidGrant {
		t.Fatalf("expected ErrInvalidGrant, got %v", err)
	}
	if ledger.rows[mcpRowId].UsedAt != nil {
		t.Fatal("the cross-audience attempt must not have touched the row")
	}
}
