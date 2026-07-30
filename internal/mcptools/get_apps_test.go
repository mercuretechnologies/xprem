package mcptools

import (
	"context"
	"testing"
	"xprem/config"
	"xprem/internal/services"
)

type multiAppLister struct{}

func (multiAppLister) GetApps(context.Context) ([]config.AppDescriptor, error) {
	return []config.AppDescriptor{{Id: "app-1", Name: "One"}, {Id: "app-2", Name: "Two"}}, nil
}

func TestGetApps(t *testing.T) {
	deps := testDeps()
	deps.Apps = multiAppLister{}
	principal := &services.DashboardPrincipal{UserId: "user-1"}

	// Unrestricted: every app.
	_, output, err := getAppsHandler(deps)(context.Background(), callToolRequestFor(principal), struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Apps) != 2 || output.Apps[0].Id != "app-1" || output.Apps[0].Name != "One" {
		t.Fatalf("unexpected apps: %+v", output.Apps)
	}

	// Restricted to app-2: the list only carries it.
	deps.VisibleApps = func(_ context.Context, _ *services.DashboardPrincipal) (bool, map[string]bool, error) {
		return true, map[string]bool{"app-2": true}, nil
	}
	_, output, err = getAppsHandler(deps)(context.Background(), callToolRequestFor(principal), struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Apps) != 1 || output.Apps[0].Id != "app-2" {
		t.Fatalf("expected only app-2, got %+v", output.Apps)
	}
}

func TestGetAppsWithoutPrincipal(t *testing.T) {
	if _, _, err := getAppsHandler(testDeps())(context.Background(), callToolRequestFor(nil), struct{}{}); err == nil {
		t.Fatal("expected an error without a principal")
	}
}
