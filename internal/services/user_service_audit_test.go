package services

import (
	"context"
	"errors"
	"testing"
	"xprem/internal/auditlog"
	"xprem/internal/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seededAdmin inserts the acting admin directly: these tests audit what the
// service does for a target, the actor just has to exist and stay enabled.
func seededAdmin(t *testing.T, repo *fakeUserRepo) store.User {
	t.Helper()
	admin, err := repo.InsertUser(context.Background(), store.InsertUserParameters{
		ID: "admin-1", Email: "admin@example.com", PasswordHash: "hash", IsAdmin: true, Enabled: true,
	})
	require.NoError(t, err)
	return admin
}

func TestUserLifecycleEmitsAuditEvents(t *testing.T) {
	repo := newFakeUserRepo()
	recorder := &fakeAuditRecorder{}
	userService := NewUserService(repo)
	userService.SetOnAuditEvent(recorder.Record)
	admin := seededAdmin(t, repo)
	// The actor rides the request context, exactly like on the admin routes.
	ctx := WithPrincipal(context.Background(),
		&DashboardPrincipal{UserId: admin.Id, Email: "admin@example.com"})

	// Creation names the actor by email and carries the granted role level.
	member, err := userService.CreateUser(ctx, "member@example.com", "Sup3rSecret!", false)
	require.NoError(t, err)
	require.Len(t, recorder.events, 1)
	created := recorder.events[0]
	assert.Equal(t, auditlog.ActionUserCreated, created.Action)
	assert.Equal(t, admin.Id, created.ActorID)
	assert.Equal(t, "admin@example.com", created.ActorDisplay)
	assert.Equal(t, member.Id, created.TargetID)
	assert.Equal(t, "member@example.com", created.TargetDisplay)
	assert.Equal(t, map[string]any{"is_admin": false}, created.Metadata)

	// Privilege escalation and its reversal are distinct, filterable events.
	require.NoError(t, userService.SetUserAdmin(ctx, admin.Id, member.Id, true))
	require.Len(t, recorder.events, 2)
	assert.Equal(t, auditlog.ActionUserAdminGranted, recorder.events[1].Action)

	// An idempotent PATCH is not a privilege change: no event.
	require.NoError(t, userService.SetUserAdmin(ctx, admin.Id, member.Id, true))
	require.Len(t, recorder.events, 2)

	require.NoError(t, userService.SetUserAdmin(ctx, admin.Id, member.Id, false))
	require.Len(t, recorder.events, 3)
	assert.Equal(t, auditlog.ActionUserAdminRevoked, recorder.events[2].Action)

	// Revoking access is an update carrying the new state; re-approving has
	// its own catalog name.
	require.NoError(t, userService.SetUserEnabled(ctx, admin.Id, member.Id, false))
	require.Len(t, recorder.events, 4)
	assert.Equal(t, auditlog.ActionUserUpdated, recorder.events[3].Action)
	assert.Equal(t, map[string]any{"enabled": false}, recorder.events[3].Metadata)

	require.NoError(t, userService.SetUserEnabled(ctx, admin.Id, member.Id, true))
	require.Len(t, recorder.events, 5)
	assert.Equal(t, auditlog.ActionUserApproved, recorder.events[4].Action)

	// Re-approving an enabled account is a no-op: no event.
	require.NoError(t, userService.SetUserEnabled(ctx, admin.Id, member.Id, true))
	require.Len(t, recorder.events, 5)

	// The deletion entry still names the account: its email was read before
	// the row disappeared.
	require.NoError(t, userService.DeleteUser(ctx, admin.Id, member.Id))
	require.Len(t, recorder.events, 6)
	deleted := recorder.events[5]
	assert.Equal(t, auditlog.ActionUserDeleted, deleted.Action)
	assert.Equal(t, member.Id, deleted.TargetID)
	assert.Equal(t, "member@example.com", deleted.TargetDisplay)
}

// failingLookupRepo makes the audit pre-read (GetUserByID on one id) fail
// while every write still succeeds, isolating the display-fallback branches.
type failingLookupRepo struct {
	*fakeUserRepo
	failID string
}

func (r *failingLookupRepo) GetUserByID(ctx context.Context, id string) (store.User, error) {
	if id == r.failID {
		return store.User{}, errors.New("lookup unavailable")
	}
	return r.fakeUserRepo.GetUserByID(ctx, id)
}

func TestAuditSurvivesTargetLookupFailure(t *testing.T) {
	base := newFakeUserRepo()
	recorder := &fakeAuditRecorder{}
	admin := seededAdmin(t, base)
	target, err := base.InsertUser(context.Background(), store.InsertUserParameters{
		ID: "target-1", Email: "target@example.com", PasswordHash: "hash", Enabled: true,
	})
	require.NoError(t, err)
	userService := NewUserService(&failingLookupRepo{fakeUserRepo: base, failID: target.Id})
	userService.SetOnAuditEvent(recorder.Record)
	ctx := WithPrincipal(context.Background(),
		&DashboardPrincipal{UserId: admin.Id, Email: "admin@example.com"})

	// The pre-read fails but the mutation succeeds: the escalation must
	// still be recorded, displayed as the id since the email is unreadable.
	require.NoError(t, userService.SetUserAdmin(ctx, admin.Id, target.Id, true))
	require.Len(t, recorder.events, 1)
	assert.Equal(t, auditlog.ActionUserAdminGranted, recorder.events[0].Action)
	assert.Equal(t, target.Id, recorder.events[0].TargetDisplay)

	require.NoError(t, userService.DeleteUser(ctx, admin.Id, target.Id))
	require.Len(t, recorder.events, 2)
	assert.Equal(t, auditlog.ActionUserDeleted, recorder.events[1].Action)
	assert.Equal(t, target.Id, recorder.events[1].TargetDisplay)
}

func TestRefusedUserMutationsEmitNothing(t *testing.T) {
	repo := newFakeUserRepo()
	recorder := &fakeAuditRecorder{}
	userService := NewUserService(repo)
	userService.SetOnAuditEvent(recorder.Record)
	admin := seededAdmin(t, repo)

	// Self-targeting refusals and failed mutations must not read as actions.
	require.Error(t, userService.DeleteUser(context.Background(), admin.Id, admin.Id))
	require.Error(t, userService.SetUserAdmin(context.Background(), admin.Id, admin.Id, false))
	_, err := userService.CreateUser(context.Background(), "not-an-email", "Sup3rSecret!", false)
	require.Error(t, err)
	require.Empty(t, recorder.events)
}
