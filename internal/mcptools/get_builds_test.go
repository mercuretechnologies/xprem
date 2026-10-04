package mcptools

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"xprem/internal/services"
	"xprem/internal/types"
)

type fakeBuilds struct {
	calls       int
	status      types.BuildStatus
	url         string
	downloadErr error
	chunks      []types.BuildLogChunk
}

func (f *fakeBuilds) List(context.Context, string, int32, int32, string) (types.BuildsPage, error) {
	f.calls++
	return types.BuildsPage{Builds: []types.BuildRecord{{ID: "build-1"}}}, nil
}

func (f *fakeBuilds) Get(_ context.Context, _, id string) (*types.BuildRecord, error) {
	f.calls++
	return &types.BuildRecord{ID: id, Status: f.status}, nil
}

func (f *fakeBuilds) ListLogs(context.Context, string, string, int32) ([]types.BuildLogChunk, error) {
	f.calls++
	return f.chunks, nil
}

func (f *fakeBuilds) DownloadURL(_ context.Context, record types.BuildRecord, _ time.Time) (string, error) {
	f.calls++
	if record.Status != types.BuildStatusReady {
		return "", services.ErrBuildNotReady
	}
	return f.url, f.downloadErr
}

func buildDeps(builds *fakeBuilds, authorize func(Access) error) Deps {
	deps := readDeps()
	deps.Builds = builds
	deps.Authorize = func(_ context.Context, _ *services.DashboardPrincipal, _ string, access Access) error {
		return authorize(access)
	}
	return deps
}

func TestBuildToolsAuthorizeLikeTheirRoutes(t *testing.T) {
	ctx := context.Background()
	req := callToolRequestFor(writePrincipal)
	input := GetBuildInput{AppId: "app-1", BuildId: "build-1"}
	calls := map[string]struct {
		perm string
		call func(Deps) error
	}{
		"get_builds": {"build:read", func(deps Deps) error {
			_, _, err := getBuildsHandler(deps)(ctx, req, GetBuildsInput{AppId: "app-1"})
			return err
		}},
		"get_build": {"build:read", func(deps Deps) error {
			_, _, err := getBuildHandler(deps)(ctx, req, input)
			return err
		}},
		"get_build_logs": {"build:read", func(deps Deps) error {
			_, _, err := getBuildLogsHandler(deps)(ctx, req, GetBuildLogsInput{AppId: "app-1", BuildId: "build-1"})
			return err
		}},
		"get_build_download_url": {"build:download", func(deps Deps) error {
			_, _, err := getBuildDownloadURLHandler(deps)(ctx, req, input)
			return err
		}},
	}
	for name, tc := range calls {
		t.Run(name, func(t *testing.T) {
			builds := &fakeBuilds{status: types.BuildStatusReady, url: "https://bucket.example/signed"}
			var seen Access
			if err := tc.call(buildDeps(builds, func(access Access) error { seen = access; return nil })); err != nil {
				t.Fatalf("allowed call must succeed, got %v", err)
			}
			if seen.Perm != tc.perm || seen.Fallback != FallbackAnyMember {
				t.Errorf("expected %s/any-member, got %+v", tc.perm, seen)
			}

			denied := &fakeBuilds{}
			if err := tc.call(buildDeps(denied, func(Access) error { return errors.New("permission denied") })); err == nil {
				t.Fatal("a denied authorization must refuse the call")
			}
			if denied.calls != 0 {
				t.Error("the build service must not be reached when authorization is denied")
			}
		})
	}
}

func TestGetBuildLogsPagesByBytes(t *testing.T) {
	chunk := strings.Repeat("a", 32<<10)
	builds := &fakeBuilds{chunks: []types.BuildLogChunk{
		{Offset: 0, Content: chunk}, {Offset: 32 << 10, Content: chunk}, {Offset: 64 << 10, Content: chunk},
	}}
	deps := buildDeps(builds, func(Access) error { return nil })
	_, output, err := getBuildLogsHandler(deps)(context.Background(), callToolRequestFor(writePrincipal), GetBuildLogsInput{AppId: "app-1", BuildId: "build-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Chunks) != 2 || output.NextOffset != 64<<10 || !output.HasMore {
		t.Fatalf("expected two chunks up to 64 KiB with more to read, got %d chunks, next %d, hasMore %v", len(output.Chunks), output.NextOffset, output.HasMore)
	}
}

func TestGetBuildDownloadURLExplainsRefusals(t *testing.T) {
	req := callToolRequestFor(writePrincipal)
	input := GetBuildInput{AppId: "app-1", BuildId: "build-1"}
	for name, tc := range map[string]struct {
		builds *fakeBuilds
		want   string
	}{
		"not ready":     {&fakeBuilds{status: types.BuildStatusBuilding}, "only a ready build"},
		"local storage": {&fakeBuilds{status: types.BuildStatusReady}, "cannot sign download links"},
		"builds off":    {&fakeBuilds{status: types.BuildStatusReady, downloadErr: services.ErrBuildStorageUnavailable}, "builds are turned off"},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := getBuildDownloadURLHandler(buildDeps(tc.builds, func(Access) error { return nil }))(context.Background(), req, input)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q, got %v", tc.want, err)
			}
		})
	}
}
