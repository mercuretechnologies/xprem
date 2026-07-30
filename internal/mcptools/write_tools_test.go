package mcptools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"expo-open-ota/internal/services"
	"expo-open-ota/internal/store"
	"expo-open-ota/internal/types"

	mcpprot "github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeWriteServices struct {
	createdBranches  []string
	deletedBranches  []string
	createdChannels  []string
	deletedChannels  []string
	rollbacks        []string
	republishedIDs   []string
	republishedGroup string
	deleteBranchErr  error
	rollbackErr      error
}

func (f *fakeWriteServices) CreateBranch(_ context.Context, _ string, branchName string) (int64, error) {
	f.createdBranches = append(f.createdBranches, branchName)
	return 42, nil
}

func (f *fakeWriteServices) DeleteBranch(_ context.Context, branchName string, _ string) error {
	if f.deleteBranchErr != nil {
		return f.deleteBranchErr
	}
	f.deletedBranches = append(f.deletedBranches, branchName)
	return nil
}

func (f *fakeWriteServices) CreateChannel(_ context.Context, _ string, _ *string, channelName string) (int64, error) {
	f.createdChannels = append(f.createdChannels, channelName)
	return 7, nil
}

func (f *fakeWriteServices) DeleteChannel(_ context.Context, channelName string, _ string) error {
	f.deletedChannels = append(f.deletedChannels, channelName)
	return nil
}

func (f *fakeWriteServices) CreateRollback(_ context.Context, _, platform, _, runtimeVersion, branchName, _ string) (*types.Update, error) {
	if f.rollbackErr != nil {
		return nil, f.rollbackErr
	}
	f.rollbacks = append(f.rollbacks, platform)
	return &types.Update{Branch: branchName, RuntimeVersion: runtimeVersion, UpdateId: "new-" + platform}, nil
}

func (f *fakeWriteServices) RepublishUpdateByID(_ context.Context, _, branchName, runtimeVersion, updateId string) (*types.Update, error) {
	f.republishedIDs = append(f.republishedIDs, updateId)
	return &types.Update{Branch: branchName, RuntimeVersion: runtimeVersion, UpdateId: "republished"}, nil
}

func (f *fakeWriteServices) RepublishPublishGroup(_ context.Context, _, _, _, publishGroup string) (*services.GroupOperationResult, error) {
	f.republishedGroup = publishGroup
	return &services.GroupOperationResult{PublishGroup: "new-group", Updates: []types.Update{{UpdateId: "a"}, {UpdateId: "b"}}}, nil
}

// writeDeps allows every authorization by default; each test tightens what it
// needs to prove.
func writeDeps() (Deps, *fakeWriteServices) {
	deps := readDeps()
	fake := &fakeWriteServices{}
	deps.BranchWriter = fake
	deps.ChannelWriter = fake
	deps.Deployments = fake
	deps.Authorize = func(_ context.Context, _ *services.DashboardPrincipal, _ string, _ Access) error { return nil }
	return deps, fake
}

var writePrincipal = &services.DashboardPrincipal{UserId: "user-1"}

// Every write tool must go through Deps.Authorize with the permission its
// route twin declares, and must refuse when it denies.
func TestWriteToolsAuthorize(t *testing.T) {
	ctx := context.Background()
	req := callToolRequestFor(writePrincipal)

	calls := map[string]struct {
		perm string
		call func(deps Deps) error
	}{
		"create_branch": {"branch:create", func(deps Deps) error {
			_, _, err := createBranchHandler(deps)(ctx, req, CreateBranchInput{AppId: "app-1", Branch: "new"})
			return err
		}},
		"delete_branch": {"branch:delete", func(deps Deps) error {
			_, _, err := deleteBranchHandler(deps)(ctx, req, DeleteBranchInput{AppId: "app-1", Branch: "main"})
			return err
		}},
		"create_channel": {"channel:create", func(deps Deps) error {
			_, _, err := createChannelHandler(deps)(ctx, req, CreateChannelInput{AppId: "app-1", Channel: "prod"})
			return err
		}},
		"delete_channel": {"channel:delete", func(deps Deps) error {
			_, _, err := deleteChannelHandler(deps)(ctx, req, DeleteChannelInput{AppId: "app-1", Channel: "prod"})
			return err
		}},
		"rollback_branch": {"update:publish", func(deps Deps) error {
			_, _, err := rollbackHandler(deps)(ctx, req, RollbackInput{AppId: "app-1", Branch: "main", RuntimeVersion: "1.0.0", Message: "bad update"})
			return err
		}},
		"republish_update": {"update:publish", func(deps Deps) error {
			_, _, err := republishHandler(deps)(ctx, req, RepublishInput{AppId: "app-1", Branch: "main", RuntimeVersion: "1.0.0", UpdateId: "12"})
			return err
		}},
	}
	for name, tc := range calls {
		t.Run(name, func(t *testing.T) {
			deps, _ := writeDeps()
			var seen Access
			deps.Authorize = func(_ context.Context, _ *services.DashboardPrincipal, appID string, access Access) error {
				seen = access
				if appID != "app-1" {
					return errors.New("wrong app")
				}
				return nil
			}
			if err := tc.call(deps); err != nil {
				t.Fatalf("allowed call must succeed, got %v", err)
			}
			if seen.Perm != tc.perm || seen.Fallback != FallbackAdminOnly {
				t.Errorf("expected %s/admin-only, got %+v", tc.perm, seen)
			}

			denied, _ := writeDeps()
			denied.Authorize = func(_ context.Context, _ *services.DashboardPrincipal, _ string, _ Access) error {
				return errors.New("permission denied")
			}
			if err := tc.call(denied); err == nil {
				t.Fatal("a denied authorization must refuse the write")
			}

			// No principal on the session: refused before anything else.
			anonymous, _ := writeDeps()
			anonymous.Authorize = func(_ context.Context, _ *services.DashboardPrincipal, _ string, _ Access) error {
				t.Fatal("authorize must not be reached without a principal")
				return nil
			}
			_ = anonymous
		})
	}
}

// The services resolve their audit actor from the context, so every write
// tool must hand them a context carrying the principal; otherwise the audit
// log records the mutation with an empty actor.
func TestWriteToolsPassPrincipalForAudit(t *testing.T) {
	ctx := context.Background()
	req := callToolRequestFor(writePrincipal)

	assertActor := func(t *testing.T, serviceCtx context.Context) {
		t.Helper()
		principal := services.PrincipalFromContext(serviceCtx)
		if principal == nil || principal.UserId != "user-1" {
			t.Fatalf("the service context must name the actor, got %+v", principal)
		}
	}

	t.Run("delete_branch", func(t *testing.T) {
		deps, _ := writeDeps()
		var seen context.Context
		deps.BranchWriter = contextCapturingWriter{onDeleteBranch: func(c context.Context) { seen = c }}
		if _, _, err := deleteBranchHandler(deps)(ctx, req, DeleteBranchInput{AppId: "app-1", Branch: "main"}); err != nil {
			t.Fatal(err)
		}
		assertActor(t, seen)
	})

	t.Run("rollback_branch", func(t *testing.T) {
		deps, _ := writeDeps()
		var seen context.Context
		deps.Deployments = contextCapturingWriter{onRollback: func(c context.Context) { seen = c }}
		if _, _, err := rollbackHandler(deps)(ctx, req, RollbackInput{
			AppId: "app-1", Branch: "main", RuntimeVersion: "1.0.0", Message: "bad update", Platform: "ios",
		}); err != nil {
			t.Fatal(err)
		}
		assertActor(t, seen)
	})

	t.Run("get_certificate", func(t *testing.T) {
		deps, _ := writeDeps()
		var seen context.Context
		deps.Certificates = contextCapturingWriter{onCertificate: func(c context.Context) { seen = c }}
		if _, _, err := getCertificateHandler(deps)(ctx, req, GetCertificateInput{AppId: "app-1"}); err != nil {
			t.Fatal(err)
		}
		assertActor(t, seen)
	})
}

// contextCapturingWriter records the context each service call receives.
type contextCapturingWriter struct {
	onDeleteBranch func(context.Context)
	onRollback     func(context.Context)
	onCertificate  func(context.Context)
}

func (c contextCapturingWriter) CreateBranch(ctx context.Context, _ string, _ string) (int64, error) {
	return 1, nil
}

func (c contextCapturingWriter) DeleteBranch(ctx context.Context, _ string, _ string) error {
	c.onDeleteBranch(ctx)
	return nil
}

func (c contextCapturingWriter) CreateRollback(ctx context.Context, _, _, _, _, _, _ string) (*types.Update, error) {
	c.onRollback(ctx)
	return &types.Update{}, nil
}

func (c contextCapturingWriter) RepublishUpdateByID(_ context.Context, _, _, _, _ string) (*types.Update, error) {
	return &types.Update{}, nil
}

func (c contextCapturingWriter) RepublishPublishGroup(_ context.Context, _, _, _, _ string) (*services.GroupOperationResult, error) {
	return &services.GroupOperationResult{}, nil
}

func (c contextCapturingWriter) RetrieveAppCertificate(ctx context.Context, _ string) (string, error) {
	c.onCertificate(ctx)
	return "-----BEGIN CERTIFICATE-----", nil
}

func TestDeleteBranchSurfacesProtection(t *testing.T) {
	deps, fake := writeDeps()
	fake.deleteBranchErr = &store.ErrBranchProtected{BranchName: "main"}

	_, _, err := deleteBranchHandler(deps)(context.Background(), callToolRequestFor(writePrincipal), DeleteBranchInput{AppId: "app-1", Branch: "main"})
	if err == nil || !strings.Contains(err.Error(), "protected") {
		t.Fatalf("the protection refusal must reach the caller verbatim, got %v", err)
	}
}

func TestDeleteBranchMasksUnexpectedErrors(t *testing.T) {
	deps, fake := writeDeps()
	fake.deleteBranchErr = errors.New("connection refused: 10.0.0.5:5432")

	_, _, err := deleteBranchHandler(deps)(context.Background(), callToolRequestFor(writePrincipal), DeleteBranchInput{AppId: "app-1", Branch: "main"})
	if err == nil || strings.Contains(err.Error(), "10.0.0.5") {
		t.Fatalf("infrastructure details must not leak, got %v", err)
	}
}

func TestRollbackBothPlatforms(t *testing.T) {
	deps, fake := writeDeps()

	_, output, err := rollbackHandler(deps)(context.Background(), callToolRequestFor(writePrincipal), RollbackInput{
		AppId: "app-1", Branch: "main", RuntimeVersion: "1.0.0", Message: "bad update",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.rollbacks) != 2 || len(output.Updates) != 2 {
		t.Fatalf("expected both platforms rolled back, got %v", fake.rollbacks)
	}

	// A rollback needs a reason, and a bogus platform is refused.
	if _, _, err := rollbackHandler(deps)(context.Background(), callToolRequestFor(writePrincipal), RollbackInput{
		AppId: "app-1", Branch: "main", RuntimeVersion: "1.0.0",
	}); err == nil || !strings.Contains(err.Error(), "message is required") {
		t.Fatalf("expected the message requirement, got %v", err)
	}
	if _, _, err := rollbackHandler(deps)(context.Background(), callToolRequestFor(writePrincipal), RollbackInput{
		AppId: "app-1", Branch: "main", RuntimeVersion: "1.0.0", Message: "x", Platform: "windows",
	}); err == nil {
		t.Fatal("an unknown platform must be refused")
	}
}

// An unknown branch or runtime version must be named, not turned into an
// opaque internal error by the not-null constraint on the insert.
func TestPublishToolsRefuseUnknownTarget(t *testing.T) {
	deps, fake := writeDeps()
	ctx := context.Background()
	req := callToolRequestFor(writePrincipal)

	_, _, err := rollbackHandler(deps)(ctx, req, RollbackInput{
		AppId: "app-1", Branch: "branch-id-1", RuntimeVersion: "1.0.0", Message: "oops",
	})
	if err == nil || !strings.Contains(err.Error(), "no branch named") {
		t.Fatalf("a branch id passed as a name must be named as unknown, got %v", err)
	}
	if len(fake.rollbacks) != 0 {
		t.Fatal("nothing must reach the service on an unknown target")
	}

	_, _, err = rollbackHandler(deps)(ctx, req, RollbackInput{
		AppId: "app-1", Branch: "main", RuntimeVersion: "9.9.9", Message: "oops",
	})
	if err == nil || !strings.Contains(err.Error(), "no runtime version") {
		t.Fatalf("an unknown runtime version must be named, got %v", err)
	}

	_, _, err = republishHandler(deps)(ctx, req, RepublishInput{
		AppId: "app-1", Branch: "main", RuntimeVersion: "9.9.9", UpdateId: "12",
	})
	if err == nil || !strings.Contains(err.Error(), "no runtime version") {
		t.Fatalf("republish must check its target too, got %v", err)
	}
}

func TestRollbackRefusesDuringActiveRollout(t *testing.T) {
	deps, fake := writeDeps()
	fake.rollbackErr = services.ErrActiveRolloutBlocksPublish

	_, _, err := rollbackHandler(deps)(context.Background(), callToolRequestFor(writePrincipal), RollbackInput{
		AppId: "app-1", Branch: "main", RuntimeVersion: "1.0.0", Message: "bad update",
	})
	if err == nil || !strings.Contains(err.Error(), "rollout") {
		t.Fatalf("the active-rollout refusal must reach the caller, got %v", err)
	}
}

func TestRepublishModes(t *testing.T) {
	ctx := context.Background()
	req := callToolRequestFor(writePrincipal)

	// Group mode mints a new publish group.
	deps, fake := writeDeps()
	_, output, err := republishHandler(deps)(ctx, req, RepublishInput{
		AppId: "app-1", Branch: "main", RuntimeVersion: "1.0.0",
		PublishGroup: "11111111-1111-1111-1111-111111111111",
	})
	if err != nil {
		t.Fatal(err)
	}
	if output.PublishGroup != "new-group" || len(output.Updates) != 2 || fake.republishedGroup == "" {
		t.Fatalf("unexpected group republish: %+v", output)
	}

	// Both or neither is refused, and a non-UUID group too.
	for name, input := range map[string]RepublishInput{
		"neither": {AppId: "app-1", Branch: "main", RuntimeVersion: "1.0.0"},
		"both":    {AppId: "app-1", Branch: "main", RuntimeVersion: "1.0.0", UpdateId: "12", PublishGroup: "11111111-1111-1111-1111-111111111111"},
		"bad uuid": {AppId: "app-1", Branch: "main", RuntimeVersion: "1.0.0",
			PublishGroup: "not-a-uuid"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := republishHandler(deps)(ctx, req, input); err == nil {
				t.Fatal("expected a refusal")
			}
		})
	}
}

// The write tools must not claim to be read-only, and the destructive ones
// must say so: clients build their human-approval prompts on these hints.
func TestWriteToolAnnotations(t *testing.T) {
	deps, _ := writeDeps()
	deps.CanUseSomewhere = func(_ context.Context, _ *services.DashboardPrincipal, _ Access) bool { return true }

	server := mcpprot.NewServer(&mcpprot.Implementation{Name: "test", Version: "0"}, nil)
	Configurator(deps)(context.Background(), writePrincipal, server)

	destructive := map[string]bool{
		"delete_branch":    true,
		"delete_channel":   true,
		"rollback_branch":  true,
		"create_branch":    false,
		"create_channel":   false,
		"republish_update": false,
	}
	tools := listToolAnnotations(t, server)
	for name, wantDestructive := range destructive {
		annotations, ok := tools[name]
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		if annotations == nil {
			t.Fatalf("%s carries no annotations", name)
		}
		if annotations.ReadOnlyHint {
			t.Errorf("%s must not be read-only", name)
		}
		if annotations.DestructiveHint == nil || *annotations.DestructiveHint != wantDestructive {
			t.Errorf("%s destructiveHint: expected %v, got %v", name, wantDestructive, annotations.DestructiveHint)
		}
	}
	// Every read tool declares itself read-only.
	for _, name := range []string{"whoami", "get_apps", "get_branches", "get_runtime_versions", "get_channels", "get_channel_rollouts", "get_updates", "get_update_rollout", "get_certificate", "get_server_config"} {
		annotations, ok := tools[name]
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		if annotations == nil || !annotations.ReadOnlyHint {
			t.Errorf("%s must declare readOnlyHint", name)
		}
	}
}

func listToolAnnotations(t *testing.T, server *mcpprot.Server) map[string]*mcpprot.ToolAnnotations {
	t.Helper()
	ctx := context.Background()
	clientTransport, serverTransport := mcpprot.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcpprot.NewClient(&mcpprot.Implementation{Name: "test-client", Version: "0"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	list, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	annotations := map[string]*mcpprot.ToolAnnotations{}
	for _, tool := range list.Tools {
		annotations[tool.Name] = tool.Annotations
	}
	return annotations
}
