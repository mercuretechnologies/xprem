package mcptools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"xprem/internal/repository"
	"xprem/internal/services"
	"xprem/internal/types"
	"xprem/internal/validation"

	mcpprot "github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeApiKeys struct {
	created []string
	revoked []string
	err     error
}

func (f *fakeApiKeys) GenerateAPIKey(_ context.Context, _ string, name string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.created = append(f.created, name)
	return "xprem_secret_abcd", nil
}

func (f *fakeApiKeys) GetApiKeysMetadata(_ context.Context, _ string) ([]types.ApiKeyMetadata, error) {
	if f.err != nil {
		return nil, f.err
	}
	return []types.ApiKeyMetadata{{ID: "3", Name: "ci", Hint: "xprem_*******abcd"}}, nil
}

func (f *fakeApiKeys) RevokeApiKey(_ context.Context, _ string, apiKeyId string) error {
	if f.err != nil {
		return f.err
	}
	f.revoked = append(f.revoked, apiKeyId)
	return nil
}

func apiKeyDeps() (Deps, *fakeApiKeys, *[]string) {
	deps, _ := writeDeps()
	fake := &fakeApiKeys{}
	deps.ApiKeys = fake
	var invalidated []string
	deps.OnApiKeysChanged = func(appID string) { invalidated = append(invalidated, appID) }
	return deps, fake, &invalidated
}

// Creating and revoking go through Deps.Authorize with the permission their
// route twin declares (apikeys:manage), and a denial stops them.
func TestApiKeyWritesAuthorize(t *testing.T) {
	ctx := context.Background()
	req := callToolRequestFor(writePrincipal)
	calls := map[string]func(deps Deps) error{
		"create_api_key": func(deps Deps) error {
			_, _, err := createApiKeyHandler(deps)(ctx, req, CreateApiKeyInput{AppId: "app-1", Name: "ci"})
			return err
		},
		"revoke_api_key": func(deps Deps) error {
			_, _, err := revokeApiKeyHandler(deps)(ctx, req, RevokeApiKeyInput{AppId: "app-1", ApiKeyId: "3"})
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			deps, fake, invalidated := apiKeyDeps()
			var seen Access
			deps.Authorize = func(_ context.Context, _ *services.DashboardPrincipal, appID string, access Access) error {
				seen = access
				if appID != "app-1" {
					return errors.New("wrong app")
				}
				return nil
			}
			if err := call(deps); err != nil {
				t.Fatalf("allowed call must succeed, got %v", err)
			}
			if seen.Perm != "apikeys:manage" || seen.Fallback != FallbackAdminOnly {
				t.Errorf("expected apikeys:manage/admin-only, got %+v", seen)
			}
			if len(*invalidated) != 1 || (*invalidated)[0] != "app-1" {
				t.Errorf("the app's api keys cache must be invalidated once, got %v", *invalidated)
			}

			denied, deniedFake, deniedInvalidated := apiKeyDeps()
			denied.Authorize = func(_ context.Context, _ *services.DashboardPrincipal, _ string, _ Access) error {
				return errors.New("permission denied")
			}
			if err := call(denied); err == nil {
				t.Fatal("a denied call must fail")
			}
			if len(deniedFake.created)+len(deniedFake.revoked) != 0 || len(*deniedInvalidated) != 0 {
				t.Fatal("a denied call must not reach the service")
			}
			_ = fake
		})
	}
}

// The plaintext key is only ever returned once, by create_api_key.
func TestCreateApiKeyReturnsTheKey(t *testing.T) {
	deps, fake, _ := apiKeyDeps()
	_, output, err := createApiKeyHandler(deps)(context.Background(), callToolRequestFor(writePrincipal), CreateApiKeyInput{AppId: "app-1", Name: "  ci  "})
	if err != nil {
		t.Fatal(err)
	}
	if output.ApiKey != "xprem_secret_abcd" || output.Name != "ci" {
		t.Fatalf("unexpected output: %+v", output)
	}
	if len(fake.created) != 1 || fake.created[0] != "ci" {
		t.Fatalf("expected one key named ci, got %v", fake.created)
	}
}

func TestApiKeyInputsAreRequired(t *testing.T) {
	ctx := context.Background()
	req := callToolRequestFor(writePrincipal)
	deps, fake, _ := apiKeyDeps()
	if _, _, err := createApiKeyHandler(deps)(ctx, req, CreateApiKeyInput{AppId: "app-1", Name: " "}); err == nil {
		t.Error("a blank name must be refused")
	}
	if _, _, err := revokeApiKeyHandler(deps)(ctx, req, RevokeApiKeyInput{AppId: "app-1"}); err == nil {
		t.Error("a missing apiKeyId must be refused")
	}
	if len(fake.created)+len(fake.revoked) != 0 {
		t.Fatal("invalid input must not reach the service")
	}
}

// Validation and not-found errors carry what to fix; anything else is masked.
func TestApiKeyWriteErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		err         error
		passThrough bool
	}{
		"validation": {validation.Errorf("apiKeyId", "must be numeric"), true},
		"not found":  {&repository.ErrResourceNotFound{}, true},
		"internal":   {errors.New("pq: connection refused"), false},
	} {
		t.Run(name, func(t *testing.T) {
			deps, fake, invalidated := apiKeyDeps()
			fake.err = tc.err
			_, _, err := revokeApiKeyHandler(deps)(context.Background(), callToolRequestFor(writePrincipal), RevokeApiKeyInput{AppId: "app-1", ApiKeyId: "3"})
			if err == nil {
				t.Fatal("expected an error")
			}
			if tc.passThrough != (err == tc.err) {
				t.Errorf("passThrough=%v, got %v", tc.passThrough, err)
			}
			if !tc.passThrough && strings.Contains(err.Error(), "pq:") {
				t.Errorf("internal error leaked: %v", err)
			}
			if len(*invalidated) != 0 {
				t.Error("a failed write must not invalidate the cache")
			}
		})
	}
}

// Listing is a read: any account that sees the app, like GET /apiKeys, and
// it never exposes key material.
func TestGetApiKeys(t *testing.T) {
	deps, _, _ := apiKeyDeps()
	deps.VisibleApps = func(_ context.Context, _ *services.DashboardPrincipal) (bool, map[string]bool, error) {
		return true, map[string]bool{"app-visible": true}, nil
	}
	req := callToolRequestFor(&services.DashboardPrincipal{UserId: "member-1"})
	_, output, err := getApiKeysHandler(deps)(context.Background(), req, GetApiKeysInput{AppId: "app-visible"})
	if err != nil {
		t.Fatal(err)
	}
	if len(output.ApiKeys) != 1 || output.ApiKeys[0].Hint != "xprem_*******abcd" {
		t.Fatalf("unexpected output: %+v", output)
	}
	if _, _, err := getApiKeysHandler(deps)(context.Background(), req, GetApiKeysInput{AppId: "app-hidden"}); err == nil {
		t.Fatal("an invisible app must be refused")
	}
}

func TestApiKeyToolAnnotations(t *testing.T) {
	deps, _, _ := apiKeyDeps()
	deps.CanUseSomewhere = func(_ context.Context, _ *services.DashboardPrincipal, _ Access) bool { return true }
	server := mcpprot.NewServer(&mcpprot.Implementation{Name: "test", Version: "0"}, nil)
	Configurator(deps)(context.Background(), writePrincipal, server)
	tools := listToolAnnotations(t, server)

	if annotations := tools["get_api_keys"]; annotations == nil || !annotations.ReadOnlyHint {
		t.Error("get_api_keys must declare readOnlyHint")
	}
	for name, wantDestructive := range map[string]bool{"create_api_key": false, "revoke_api_key": true} {
		annotations, ok := tools[name]
		if !ok || annotations == nil {
			t.Fatalf("%s is not registered with annotations", name)
		}
		if annotations.ReadOnlyHint || annotations.DestructiveHint == nil || *annotations.DestructiveHint != wantDestructive {
			t.Errorf("%s: expected a write with destructiveHint=%v, got %+v", name, wantDestructive, annotations)
		}
	}
}
