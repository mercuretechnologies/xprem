package mcptools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"xprem/config"
	"xprem/internal/repository"
	"xprem/internal/services"
	"xprem/internal/validation"

	mcpprot "github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeAppCreator struct {
	names []string
	modes []config.KeysMode
	err   error
}

func (f *fakeAppCreator) CreateApp(_ context.Context, displayName string, keysConfig config.KeysConfig) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.names = append(f.names, displayName)
	f.modes = append(f.modes, keysConfig.Mode)
	return "new-app-id", nil
}

func appDeps() (Deps, *fakeAppCreator, *int) {
	deps := readDeps()
	fake := &fakeAppCreator{}
	deps.AppCreator = fake
	invalidations := 0
	deps.OnAppsChanged = func() { invalidations++ }
	return deps, fake, &invalidations
}

var adminPrincipal = &services.DashboardPrincipal{UserId: "admin-1", IsAdmin: true}

// An admin creates an app with server-generated, database-sealed keys, the
// same default the dashboard offers.
func TestCreateAppAsAdmin(t *testing.T) {
	deps, fake, invalidations := appDeps()
	_, output, err := createAppHandler(deps)(context.Background(), callToolRequestFor(adminPrincipal), CreateAppInput{Name: "  My App  "})
	if err != nil {
		t.Fatalf("admin create must succeed, got %v", err)
	}
	if output.AppId != "new-app-id" || output.Name != "My App" {
		t.Fatalf("unexpected output: %+v", output)
	}
	if len(fake.names) != 1 || fake.names[0] != "My App" || fake.modes[0] != config.KeysModeDatabase {
		t.Fatalf("expected one database-keys app named My App, got %v %v", fake.names, fake.modes)
	}
	if *invalidations != 1 {
		t.Errorf("the apps cache must be invalidated once, got %d", *invalidations)
	}
}

// App creation is account-wide, like its dashboard route: members are refused
// whatever their per-app grants.
func TestCreateAppRefusesNonAdmins(t *testing.T) {
	for name, principal := range map[string]*services.DashboardPrincipal{
		"member":    {UserId: "member-1"},
		"anonymous": nil,
	} {
		t.Run(name, func(t *testing.T) {
			deps, fake, invalidations := appDeps()
			if _, _, err := createAppHandler(deps)(context.Background(), callToolRequestFor(principal), CreateAppInput{Name: "My App"}); err == nil {
				t.Fatal("expected a refusal")
			}
			if len(fake.names) != 0 || *invalidations != 0 {
				t.Fatal("a refused call must not create anything")
			}
		})
	}
}

func TestCreateAppRequiresName(t *testing.T) {
	deps, fake, _ := appDeps()
	if _, _, err := createAppHandler(deps)(context.Background(), callToolRequestFor(adminPrincipal), CreateAppInput{Name: "   "}); err == nil {
		t.Fatal("expected a refusal for a blank name")
	}
	if len(fake.names) != 0 {
		t.Fatal("a blank name must not reach the service")
	}
}

// Validation and conflict errors carry what to fix; anything else is masked.
func TestCreateAppErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		err         error
		passThrough bool
	}{
		"validation": {validation.Errorf("name", "too long"), true},
		"conflict":   {&repository.ErrResourceAlreadyExists{}, true},
		"internal":   {errors.New("pq: connection refused"), false},
	} {
		t.Run(name, func(t *testing.T) {
			deps, fake, invalidations := appDeps()
			fake.err = tc.err
			_, _, err := createAppHandler(deps)(context.Background(), callToolRequestFor(adminPrincipal), CreateAppInput{Name: "My App"})
			if err == nil {
				t.Fatal("expected an error")
			}
			if tc.passThrough != (err == tc.err) {
				t.Errorf("passThrough=%v, got %v", tc.passThrough, err)
			}
			if !tc.passThrough && strings.Contains(err.Error(), "pq:") {
				t.Errorf("internal error leaked: %v", err)
			}
			if *invalidations != 0 {
				t.Error("a failed create must not invalidate the cache")
			}
		})
	}
}

// create_app is listed for admins only, and declares itself a non-destructive
// write.
func TestCreateAppRegistration(t *testing.T) {
	for name, tc := range map[string]struct {
		principal *services.DashboardPrincipal
		listed    bool
	}{
		"admin":  {adminPrincipal, true},
		"member": {&services.DashboardPrincipal{UserId: "member-1"}, false},
	} {
		t.Run(name, func(t *testing.T) {
			deps, _, _ := appDeps()
			server := mcpprot.NewServer(&mcpprot.Implementation{Name: "test", Version: "0"}, nil)
			Configurator(deps)(context.Background(), tc.principal, server)
			annotations, listed := listToolAnnotations(t, server)["create_app"]
			if listed != tc.listed {
				t.Fatalf("listed=%v, expected %v", listed, tc.listed)
			}
			if !listed {
				return
			}
			if annotations == nil || annotations.ReadOnlyHint || annotations.DestructiveHint == nil || *annotations.DestructiveHint {
				t.Errorf("create_app must be a non-destructive write, got %+v", annotations)
			}
		})
	}
}
