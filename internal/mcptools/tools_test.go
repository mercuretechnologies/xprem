package mcptools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"expo-open-ota/internal/services"
	"expo-open-ota/internal/types"
)

type fakeReadServices struct{}

func (fakeReadServices) GetBranches(_ context.Context, _ string) ([]types.BranchMapping, error) {
	mainId, stagingId := "branch-id-1", "branch-id-2"
	return []types.BranchMapping{
		{BranchName: "main", BranchId: &mainId},
		{BranchName: "staging", BranchId: &stagingId},
	}, nil
}

func (fakeReadServices) GetRuntimeVersionsWithUpdateStats(_ context.Context, _ string, _ string) ([]types.RuntimeVersionWithStats, error) {
	return []types.RuntimeVersionWithStats{{RuntimeVersion: "1.0.0", NumberOfUpdates: 3}}, nil
}

func (fakeReadServices) GetChannels(_ context.Context, _ string) ([]types.ChannelMapping, error) {
	return []types.ChannelMapping{
		{ReleaseChannelName: "production", ReleaseChannelId: "channel-id-1", Rollout: &types.ChannelRollout{ChannelName: "production", Percentage: 25}},
		{ReleaseChannelName: "staging", ReleaseChannelId: "channel-id-2"},
	}, nil
}

func (fakeReadServices) GetUpdateFeed(_ context.Context, _ string, query types.UpdateFeedQuery) ([]types.UpdateFeedItem, error) {
	items := make([]types.UpdateFeedItem, 0, query.Limit)
	return items, nil
}

func (fakeReadServices) GetUpdateRollout(_ context.Context, _ string, _ string, _ string) ([]types.RolloutUpdate, error) {
	return []types.RolloutUpdate{{UpdateId: "u1", Platform: "ios", Percentage: 50}}, nil
}

func (fakeReadServices) RetrieveAppCertificate(_ context.Context, _ string) (string, error) {
	return "-----BEGIN CERTIFICATE-----", nil
}

func readDeps() Deps {
	deps := testDeps()
	fake := fakeReadServices{}
	deps.Branches = fake
	deps.Channels = fake
	deps.UpdateFeed = fake
	deps.UpdateRollouts = fake
	deps.Certificates = fake
	deps.SSOEnabled = func(context.Context) bool { return false }
	return deps
}

// Every app-scoped tool must refuse an invisible app with the 404 answer and
// an empty appId with the guidance message.
func TestAppScopedToolsRequireVisibleApp(t *testing.T) {
	deps := readDeps()
	deps.VisibleApps = func(_ context.Context, _ *services.DashboardPrincipal) (bool, map[string]bool, error) {
		return true, map[string]bool{"app-visible": true}, nil
	}
	principal := &services.DashboardPrincipal{UserId: "user-1"}
	req := callToolRequestFor(principal)
	ctx := context.Background()

	calls := map[string]func(appID string) error{
		"get_branches": func(appID string) error {
			_, _, err := getBranchesHandler(deps)(ctx, req, GetBranchesInput{AppId: appID})
			return err
		},
		"get_runtime_versions": func(appID string) error {
			_, _, err := getRuntimeVersionsHandler(deps)(ctx, req, GetRuntimeVersionsInput{AppId: appID, Branch: "main"})
			return err
		},
		"get_channels": func(appID string) error {
			_, _, err := getChannelsHandler(deps)(ctx, req, GetChannelsInput{AppId: appID})
			return err
		},
		"get_channel_rollouts": func(appID string) error {
			_, _, err := getChannelRolloutsHandler(deps)(ctx, req, GetChannelsInput{AppId: appID})
			return err
		},
		"get_updates": func(appID string) error {
			_, _, err := getUpdatesHandler(deps)(ctx, req, GetUpdatesInput{AppId: appID})
			return err
		},
		"get_update_rollout": func(appID string) error {
			_, _, err := getUpdateRolloutHandler(deps)(ctx, req, GetUpdateRolloutInput{AppId: appID, Branch: "main", RuntimeVersion: "1.0.0"})
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			if err := call("app-visible"); err != nil {
				t.Fatalf("visible app must pass, got %v", err)
			}
			err := call("app-hidden")
			if err == nil || err.Error() != "app not found" {
				t.Fatalf("invisible app must read as 404, got %v", err)
			}
			if err := call(""); err == nil || !strings.Contains(err.Error(), "appId is required") {
				t.Fatalf("empty appId must guide to get_apps, got %v", err)
			}
		})
	}
}

func TestGetBranchesNameFilter(t *testing.T) {
	deps := readDeps()
	principal := &services.DashboardPrincipal{UserId: "user-1"}

	_, output, err := getBranchesHandler(deps)(context.Background(), callToolRequestFor(principal), GetBranchesInput{AppId: "app-1", Name: "MAIN"})
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Branches) != 1 || output.Branches[0].BranchName != "main" {
		t.Fatalf("expected the main branch only, got %+v", output.Branches)
	}
}

func TestBranchLookupById(t *testing.T) {
	deps := readDeps()
	principal := &services.DashboardPrincipal{UserId: "user-1"}
	ctx := context.Background()
	req := callToolRequestFor(principal)

	// get_branches by id.
	_, branches, err := getBranchesHandler(deps)(ctx, req, GetBranchesInput{AppId: "app-1", Id: "branch-id-2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(branches.Branches) != 1 || branches.Branches[0].BranchName != "staging" {
		t.Fatalf("expected staging by id, got %+v", branches.Branches)
	}

	// Id resolution feeds the name-keyed services.
	if _, _, err := getRuntimeVersionsHandler(deps)(ctx, req, GetRuntimeVersionsInput{AppId: "app-1", BranchId: "branch-id-1"}); err != nil {
		t.Fatalf("branchId must resolve to the branch name, got %v", err)
	}
	if _, _, err := getRuntimeVersionsHandler(deps)(ctx, req, GetRuntimeVersionsInput{AppId: "app-1", BranchId: "branch-id-unknown"}); err == nil || err.Error() != "branch not found" {
		t.Fatalf("unknown branchId must read as not found, got %v", err)
	}
	if _, _, err := getRuntimeVersionsHandler(deps)(ctx, req, GetRuntimeVersionsInput{AppId: "app-1", Branch: "staging", BranchId: "branch-id-1"}); err == nil {
		t.Fatal("conflicting branch and branchId must be refused")
	}
}

func TestChannelLookupById(t *testing.T) {
	deps := readDeps()
	principal := &services.DashboardPrincipal{UserId: "user-1"}

	_, output, err := getChannelsHandler(deps)(context.Background(), callToolRequestFor(principal), GetChannelsInput{AppId: "app-1", Id: "channel-id-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Channels) != 1 || output.Channels[0].ReleaseChannelName != "production" {
		t.Fatalf("expected production by id, got %+v", output.Channels)
	}
}

func TestGetChannelRolloutsOnlyActive(t *testing.T) {
	deps := readDeps()
	principal := &services.DashboardPrincipal{UserId: "user-1"}

	_, output, err := getChannelRolloutsHandler(deps)(context.Background(), callToolRequestFor(principal), GetChannelsInput{AppId: "app-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Rollouts) != 1 || output.Rollouts[0].ChannelName != "production" || output.Rollouts[0].Rollout.Percentage != 25 {
		t.Fatalf("expected the production rollout only, got %+v", output.Rollouts)
	}
}

func TestGetCertificateAuthorize(t *testing.T) {
	deps := readDeps()
	principal := &services.DashboardPrincipal{UserId: "user-1"}

	// The deps' Authorize fake denies everything: the tool must refuse.
	if _, _, err := getCertificateHandler(deps)(context.Background(), callToolRequestFor(principal), GetCertificateInput{AppId: "app-1"}); err == nil {
		t.Fatal("expected the authorization denial to propagate")
	}

	deps.Authorize = func(_ context.Context, _ *services.DashboardPrincipal, appID string, access Access) error {
		if appID != "app-1" || access.Perm != "certificate:read" {
			return errors.New("wrong authorization request")
		}
		return nil
	}
	_, output, err := getCertificateHandler(deps)(context.Background(), callToolRequestFor(principal), GetCertificateInput{AppId: "app-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(output.CertificatePEM, "-----BEGIN CERTIFICATE-----") {
		t.Fatalf("expected a PEM, got %q", output.CertificatePEM)
	}
}
