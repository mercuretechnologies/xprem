// Service-level tests for app identifiers: per-platform format validation,
// audit emission and the stateless refusal. The guarded delete lives in SQL
// and is covered by the store tests.
package services

import (
	"context"
	"testing"
	"xprem/internal/auditlog"
	"xprem/internal/cache"
	"xprem/internal/dashboard"
	"xprem/internal/repository"
	"xprem/internal/types"
	"xprem/internal/validation"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeAppIdentifierRepo struct {
	ios         bool
	inserted    []string
	buildNumber string
}

func (f *fakeAppIdentifierRepo) platform() types.Platform {
	if f.ios {
		return types.PlatformIOS
	}
	return types.PlatformAndroid
}

func (f *fakeAppIdentifierRepo) InsertAppIdentifier(_ context.Context, _ string, platform types.Platform, identifier string) (string, error) {
	f.inserted = append(f.inserted, string(platform)+"/"+identifier)
	return "id-1", nil
}

func (f *fakeAppIdentifierRepo) GetAppIdentifiers(_ context.Context, _ string) ([]repository.AppIdentifierRow, error) {
	return nil, nil
}

func (f *fakeAppIdentifierRepo) GetAppIdentifierByID(_ context.Context, _ string, _ string) (*repository.AppIdentifierRef, error) {
	return &repository.AppIdentifierRef{Id: "id-1", Platform: f.platform(), Identifier: "com.example.app", BuildNumber: f.buildNumber}, nil
}

func (f *fakeAppIdentifierRepo) DeleteAppIdentifier(_ context.Context, _ string, _ string) error {
	return nil
}

func (f *fakeAppIdentifierRepo) SetBuildNumber(_ context.Context, _ string, _ string, buildNumber string) error {
	f.buildNumber = buildNumber
	return nil
}

func TestCreateAppIdentifierValidatesPerPlatform(t *testing.T) {
	service := NewAppIdentifierService(&fakeAppIdentifierRepo{})
	ctx := context.Background()

	var valErr *validation.Error
	_, err := service.CreateAppIdentifier(ctx, "app-1", "windows", "com.example.app")
	assert.ErrorAs(t, err, &valErr)
	_, err = service.CreateAppIdentifier(ctx, "app-1", types.PlatformAndroid, "no-dots")
	assert.ErrorAs(t, err, &valErr)
	_, err = service.CreateAppIdentifier(ctx, "app-1", types.PlatformAndroid, "com.1bad.app")
	assert.ErrorAs(t, err, &valErr)
	_, err = service.CreateAppIdentifier(ctx, "app-1", types.PlatformIOS, "com/bad")
	assert.ErrorAs(t, err, &valErr)

	_, err = service.CreateAppIdentifier(ctx, "app-1", types.PlatformAndroid, "com.example.app")
	assert.NoError(t, err)
	// A single-segment bundle id is legal on ios.
	_, err = service.CreateAppIdentifier(ctx, "app-1", types.PlatformIOS, "com.example-app.ios")
	assert.NoError(t, err)
}

func TestAppIdentifierAuditEventsAndInvalidatesAccessCache(t *testing.T) {
	service := NewAppIdentifierService(&fakeAppIdentifierRepo{})
	var recorded []auditlog.Event
	service.SetOnAuditEvent(func(_ context.Context, event auditlog.Event) {
		recorded = append(recorded, event)
	})

	_, err := service.CreateAppIdentifier(context.Background(), "app-1", types.PlatformAndroid, "com.example.app")
	require.NoError(t, err)
	accessCache := cache.GetCache()
	accessKey := dashboard.ComputeGetApiKeyAccessCacheKey("app-1")
	t.Cleanup(func() { accessCache.Delete(accessKey) })
	require.NoError(t, accessCache.Set(accessKey, "stale", nil))
	require.NoError(t, service.DeleteAppIdentifier(context.Background(), "app-1", "id-1"))
	assert.Empty(t, accessCache.Get(accessKey))

	require.Len(t, recorded, 2)
	assert.Equal(t, auditlog.ActionAppIdentifierCreated, recorded[0].Action)
	assert.Equal(t, "com.example.app", recorded[0].TargetDisplay)
	assert.Equal(t, types.PlatformAndroid, recorded[0].Metadata["platform"])
	assert.Equal(t, auditlog.ActionAppIdentifierDeleted, recorded[1].Action)
	assert.Equal(t, "com.example.app", recorded[1].TargetDisplay)
}

func TestSetBuildNumberValidatesBoundsAndAudits(t *testing.T) {
	repo := &fakeAppIdentifierRepo{}
	service := NewAppIdentifierService(repo)
	var recorded []auditlog.Event
	service.SetOnAuditEvent(func(_ context.Context, event auditlog.Event) {
		recorded = append(recorded, event)
	})
	ctx := context.Background()

	var valErr *validation.Error
	assert.ErrorAs(t, service.SetBuildNumber(ctx, "app-1", "id-1", "-1"), &valErr)
	assert.ErrorAs(t, service.SetBuildNumber(ctx, "app-1", "id-1", "2100000001"), &valErr)
	assert.Empty(t, recorded)

	require.NoError(t, service.SetBuildNumber(ctx, "app-1", "id-1", "87"))
	assert.Equal(t, "87", repo.buildNumber)
	require.Len(t, recorded, 1)
	assert.Equal(t, auditlog.ActionAppIdentifierBuildNumberSet, recorded[0].Action)
	assert.Equal(t, "com.example.app", recorded[0].TargetDisplay)
	assert.Equal(t, "87", recorded[0].Metadata["to"])
}

func TestAppIdentifiersUnsupportedInStatelessMode(t *testing.T) {
	service := NewAppIdentifierService(nil)
	ctx := context.Background()
	_, err := service.CreateAppIdentifier(ctx, "app-1", types.PlatformAndroid, "com.example.app")
	assert.ErrorIs(t, err, repository.ErrNotSupportedInStatelessMode)
	_, err = service.GetAppIdentifiers(ctx, "app-1")
	assert.ErrorIs(t, err, repository.ErrNotSupportedInStatelessMode)
	assert.ErrorIs(t, service.DeleteAppIdentifier(ctx, "app-1", "id-1"), repository.ErrNotSupportedInStatelessMode)
}

func (f *fakeAppIdentifierRepo) AllocateBuildNumber(_ context.Context, _ string, _ string, next func(types.Platform, string) (string, error)) (*repository.AppIdentifierRef, error) {
	previous := f.buildNumber
	if previous == "" {
		previous = "0"
	}
	value, err := next(f.platform(), previous)
	if err != nil {
		return nil, err
	}
	f.buildNumber = value
	return &repository.AppIdentifierRef{Id: "id-1", Platform: f.platform(), Identifier: "com.example.app", BuildNumber: value, PreviousBuildNumber: previous}, nil
}

func TestAllocateBuildNumberPersistsAndAudits(t *testing.T) {
	repo := &fakeAppIdentifierRepo{}
	service := NewAppIdentifierService(repo)
	var events []auditlog.Event
	service.SetOnAuditEvent(func(_ context.Context, e auditlog.Event) { events = append(events, e) })

	got, err := service.AllocateBuildNumber(context.Background(), "app-1", "id-1")
	require.NoError(t, err)
	require.Equal(t, "1", got)
	require.Equal(t, "1", repo.buildNumber)
	require.Len(t, events, 1)
	require.Equal(t, auditlog.ActionAppIdentifierBuildNumberAllocated, events[0].Action)
	require.Equal(t, "1", events[0].Metadata["buildNumber"])
	require.Equal(t, "0", events[0].Metadata["from"])
	require.Equal(t, "1", events[0].Metadata["to"])

	repo.buildNumber = "2100000000"
	_, err = service.AllocateBuildNumber(context.Background(), "app-1", "id-1")
	require.ErrorIs(t, err, repository.ErrBuildNumberExhausted)
	require.Len(t, events, 1)

	_, err = NewAppIdentifierService(nil).AllocateBuildNumber(context.Background(), "app-1", "id-1")
	require.ErrorIs(t, err, repository.ErrNotSupportedInStatelessMode)
}

func TestSetBuildNumberUsesRecordedPlatform(t *testing.T) {
	for _, ios := range []bool{false, true} {
		repo := &fakeAppIdentifierRepo{ios: ios}
		service := NewAppIdentifierService(repo)
		for _, value := range []string{"1.2.0", "2100000001", "9223372036854775808"} {
			err := service.SetBuildNumber(context.Background(), "app-1", "id-1", value)
			if ios {
				require.NoError(t, err)
				require.Equal(t, value, repo.buildNumber)
			} else {
				require.Error(t, err)
				require.Empty(t, repo.buildNumber)
			}
		}
	}
}

func TestAllocateBuildNumberPlatformRules(t *testing.T) {
	for _, tc := range []struct {
		platform      types.Platform
		current, want string
		fails         bool
	}{
		{types.PlatformAndroid, "0", "1", false},
		{types.PlatformAndroid, "2099999999", "2100000000", false},
		{types.PlatformAndroid, "2100000000", "", true},
		{types.PlatformAndroid, "1.2.0", "", true},
		{types.PlatformIOS, "2100000000", "2100000001", false},
		{types.PlatformIOS, "9223372036854775807", "9223372036854775808", false},
		{types.PlatformIOS, "1.2.0", "1.2.1", false},
		{types.PlatformIOS, "1.3.9", "1.3.10", false},
		{types.PlatformIOS, "1.9", "1.10", false},
		{types.PlatformIOS, "42", "43", false},
		{types.PlatformIOS, "1.2.9223372036854775807", "1.2.9223372036854775808", false},
		{types.PlatformIOS, "999999999999999999999999.3.9", "999999999999999999999999.3.10", false},
		{types.PlatformIOS, "invalid", "", true},
	} {
		t.Run(string(tc.platform)+"/"+tc.current, func(t *testing.T) {
			repo := &fakeAppIdentifierRepo{ios: tc.platform == types.PlatformIOS, buildNumber: tc.current}
			service := NewAppIdentifierService(repo)
			audits := 0
			service.SetOnAuditEvent(func(context.Context, auditlog.Event) { audits++ })
			got, err := service.AllocateBuildNumber(context.Background(), "app-1", "id-1")
			if tc.fails {
				require.Error(t, err)
				require.Equal(t, tc.current, repo.buildNumber)
				require.Zero(t, audits)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.want, repo.buildNumber)
				require.Equal(t, 1, audits)
			}
			require.Equal(t, tc.want, got)
		})
	}
}

func (f *fakeAppIdentifierRepo) GetAppIdentifierByPlatformAndIdentifier(context.Context, string, types.Platform, string) (*repository.AppIdentifierRef, error) {
	panic("not used in these tests")
}
