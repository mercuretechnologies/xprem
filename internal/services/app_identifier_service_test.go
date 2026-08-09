// Service-level tests for app identifiers: per-platform format validation,
// audit emission and the stateless refusal. The guarded delete lives in SQL
// and is covered by the store tests.
package services

import (
	"context"
	"testing"
	"xprem/internal/auditlog"
	"xprem/internal/store"
	"xprem/internal/validation"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeAppIdentifierRepo struct {
	inserted    []string
	buildNumber int64
}

func (f *fakeAppIdentifierRepo) InsertAppIdentifier(_ context.Context, _ string, platform string, identifier string) (string, error) {
	f.inserted = append(f.inserted, platform+"/"+identifier)
	return "id-1", nil
}

func (f *fakeAppIdentifierRepo) GetAppIdentifiers(_ context.Context, _ string) ([]store.AppIdentifierRow, error) {
	return nil, nil
}

func (f *fakeAppIdentifierRepo) GetAppIdentifierByID(_ context.Context, _ string, _ string) (*store.AppIdentifierRef, error) {
	return &store.AppIdentifierRef{Id: "id-1", Platform: PlatformAndroid, Identifier: "com.example.app"}, nil
}

func (f *fakeAppIdentifierRepo) DeleteAppIdentifier(_ context.Context, _ string, _ string) error {
	return nil
}

func (f *fakeAppIdentifierRepo) SetBuildNumber(_ context.Context, _ string, _ string, buildNumber int64) error {
	f.buildNumber = buildNumber
	return nil
}

func TestCreateAppIdentifierValidatesPerPlatform(t *testing.T) {
	service := NewAppIdentifierService(&fakeAppIdentifierRepo{})
	ctx := context.Background()

	var valErr *validation.Error
	_, err := service.CreateAppIdentifier(ctx, "app-1", "windows", "com.example.app")
	assert.ErrorAs(t, err, &valErr)
	_, err = service.CreateAppIdentifier(ctx, "app-1", PlatformAndroid, "no-dots")
	assert.ErrorAs(t, err, &valErr)
	_, err = service.CreateAppIdentifier(ctx, "app-1", PlatformAndroid, "com.1bad.app")
	assert.ErrorAs(t, err, &valErr)
	_, err = service.CreateAppIdentifier(ctx, "app-1", PlatformIOS, "com/bad")
	assert.ErrorAs(t, err, &valErr)

	_, err = service.CreateAppIdentifier(ctx, "app-1", PlatformAndroid, "com.example.app")
	assert.NoError(t, err)
	// A single-segment bundle id is legal on ios.
	_, err = service.CreateAppIdentifier(ctx, "app-1", PlatformIOS, "com.example-app.ios")
	assert.NoError(t, err)
}

func TestAppIdentifierAuditEvents(t *testing.T) {
	service := NewAppIdentifierService(&fakeAppIdentifierRepo{})
	var recorded []auditlog.Event
	service.SetOnAuditEvent(func(_ context.Context, event auditlog.Event) {
		recorded = append(recorded, event)
	})

	_, err := service.CreateAppIdentifier(context.Background(), "app-1", PlatformAndroid, "com.example.app")
	require.NoError(t, err)
	require.NoError(t, service.DeleteAppIdentifier(context.Background(), "app-1", "id-1"))

	require.Len(t, recorded, 2)
	assert.Equal(t, auditlog.ActionAppIdentifierCreated, recorded[0].Action)
	assert.Equal(t, "com.example.app", recorded[0].TargetDisplay)
	assert.Equal(t, PlatformAndroid, recorded[0].Metadata["platform"])
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
	assert.ErrorAs(t, service.SetBuildNumber(ctx, "app-1", "id-1", -1), &valErr)
	assert.ErrorAs(t, service.SetBuildNumber(ctx, "app-1", "id-1", 2_100_000_001), &valErr)
	assert.Empty(t, recorded)

	require.NoError(t, service.SetBuildNumber(ctx, "app-1", "id-1", 87))
	assert.Equal(t, int64(87), repo.buildNumber)
	require.Len(t, recorded, 1)
	assert.Equal(t, auditlog.ActionAppIdentifierBuildNumberSet, recorded[0].Action)
	assert.Equal(t, "com.example.app", recorded[0].TargetDisplay)
	assert.Equal(t, int64(87), recorded[0].Metadata["to"])
}

func TestAppIdentifiersUnsupportedInStatelessMode(t *testing.T) {
	service := NewAppIdentifierService(nil)
	ctx := context.Background()
	_, err := service.CreateAppIdentifier(ctx, "app-1", PlatformAndroid, "com.example.app")
	assert.ErrorIs(t, err, store.ErrNotSupportedInStatelessMode)
	_, err = service.GetAppIdentifiers(ctx, "app-1")
	assert.ErrorIs(t, err, store.ErrNotSupportedInStatelessMode)
	assert.ErrorIs(t, service.DeleteAppIdentifier(ctx, "app-1", "id-1"), store.ErrNotSupportedInStatelessMode)
}
