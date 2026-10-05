package services

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"xprem/internal/auditlog"
	"xprem/internal/crypto"
	"xprem/internal/repository"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResetPasswordRevokesOnlyTheTargetSessions(t *testing.T) {
	fixture := newRevocationFixture(t)
	ctx := WithPrincipal(context.Background(), &DashboardPrincipal{
		UserId: fixture.admin.Id, Email: fixture.admin.Email, IsAdmin: true,
	})
	otherDevice, err := fixture.auth.LoginWithEmailPassword(ctx, fixture.member.Email, "Sup3rSecret!")
	require.NoError(t, err)
	actorBefore, err := fixture.repo.GetUserByID(ctx, fixture.admin.Id)
	require.NoError(t, err)

	require.NoError(t, fixture.users.ResetPassword(ctx, fixture.admin.Id, fixture.member.Id, "An0therSecret!"))

	assertSessionDead(t, fixture.auth, fixture.memberSession)
	assertSessionDead(t, fixture.auth, otherDevice)
	assertSessionAlive(t, fixture.auth, fixture.adminSession)
	_, err = fixture.auth.RefreshSession(ctx, fixture.adminSession.RefreshToken)
	assert.NoError(t, err, "the acting admin must retain the refresh token too")
	_, err = fixture.auth.LoginWithEmailPassword(ctx, fixture.member.Email, "Sup3rSecret!")
	assert.Error(t, err, "the old target password must stop working")
	fresh, err := fixture.auth.LoginWithEmailPassword(ctx, fixture.member.Email, "An0therSecret!")
	require.NoError(t, err)
	assertSessionAlive(t, fixture.auth, fresh)

	target, err := fixture.repo.GetUserByID(ctx, fixture.member.Id)
	require.NoError(t, err)
	assert.EqualValues(t, 1, target.SessionVersion)
	assert.True(t, crypto.VerifyPassword(target.PasswordHash, "An0therSecret!"))
	actorAfter, err := fixture.repo.GetUserByID(ctx, fixture.admin.Id)
	require.NoError(t, err)
	assert.Equal(t, actorBefore.PasswordHash, actorAfter.PasswordHash)
	assert.Equal(t, actorBefore.SessionVersion, actorAfter.SessionVersion)
}

func TestResetPasswordRefusalsLeavePasswordSessionsAndAuditUntouched(t *testing.T) {
	service, repo, admin, member := seedUserService(t)
	recorder := &fakeAuditRecorder{}
	service.SetOnAuditEvent(recorder.Record)
	ctx := WithPrincipal(context.Background(), &DashboardPrincipal{
		UserId: admin.Id, Email: admin.Email, IsAdmin: true,
	})

	t.Run("self reset requires the current-password pathway", func(t *testing.T) {
		err := service.ResetPassword(ctx, admin.Id, admin.Id, "An0therSecret!")
		assert.ErrorIs(t, err, ErrCannotResetOwnPassword)
	})
	t.Run("unknown account", func(t *testing.T) {
		err := service.ResetPassword(ctx, admin.Id, "unknown", "An0therSecret!")
		var notFound *repository.ErrResourceNotFound
		assert.ErrorAs(t, err, &notFound)
	})
	for _, password := range []string{"", "weak", "Short1!", "alllower1!", "ALLUPPER1!", "NoDigits!", "NoSpecial1"} {
		t.Run("policy/"+password, func(t *testing.T) {
			err := service.ResetPassword(ctx, admin.Id, member.Id, password)
			var validation *ValidationError
			assert.ErrorAs(t, err, &validation)
		})
	}
	t.Run("hashing fails", func(t *testing.T) {
		assert.Error(t, service.ResetPassword(ctx, admin.Id, member.Id, strings.Repeat("An0therSecret!", 7)))
	})
	t.Run("database write fails", func(t *testing.T) {
		failing := NewUserService(&passwordWriteFailingUserRepo{fakeUserRepo: repo})
		failing.SetOnAuditEvent(recorder.Record)
		assert.Error(t, failing.ResetPassword(ctx, admin.Id, member.Id, "An0therSecret!"))
	})
	t.Run("target lookup fails", func(t *testing.T) {
		failing := NewUserService(&failingLookupRepo{fakeUserRepo: repo, failID: member.Id})
		failing.SetOnAuditEvent(recorder.Record)
		assert.Error(t, failing.ResetPassword(ctx, admin.Id, member.Id, "An0therSecret!"))
	})

	for _, before := range []repository.User{admin, member} {
		after, err := repo.GetUserByID(ctx, before.Id)
		require.NoError(t, err)
		assert.Equal(t, before.PasswordHash, after.PasswordHash)
		assert.Equal(t, before.SessionVersion, after.SessionVersion)
	}
	assert.Empty(t, recorder.events, "failed rotations must never look like successful password changes")
}

func TestResetPasswordFailedWriteKeepsTheTargetSessionsAlive(t *testing.T) {
	fixture := newRevocationFixture(t)
	failing := NewUserService(&passwordWriteFailingUserRepo{fakeUserRepo: fixture.repo})

	require.Error(t, failing.ResetPassword(context.Background(), fixture.admin.Id, fixture.member.Id, "An0therSecret!"))

	assertSessionAlive(t, fixture.auth, fixture.memberSession)
	_, err := fixture.auth.RefreshSession(context.Background(), fixture.memberSession.RefreshToken)
	assert.NoError(t, err)
	_, err = fixture.auth.LoginWithEmailPassword(context.Background(), fixture.member.Email, "Sup3rSecret!")
	assert.NoError(t, err, "a failed write must preserve the old password")
}

func TestResetPasswordPreservesDisabledAccountAndRole(t *testing.T) {
	for _, tc := range []struct {
		name    string
		isAdmin bool
	}{
		{name: "member"},
		{name: "admin", isAdmin: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, repo, admin, target := seedUserService(t)
			target.Enabled = false
			target.IsAdmin = tc.isAdmin
			repo.users[target.Id] = target

			require.NoError(t, service.ResetPassword(context.Background(), admin.Id, target.Id, "An0therSecret!"))

			stored, err := repo.GetUserByID(context.Background(), target.Id)
			require.NoError(t, err)
			assert.False(t, stored.Enabled, "a password reset must not approve a disabled account")
			assert.Equal(t, tc.isAdmin, stored.IsAdmin, "a password reset must preserve the granted role")
			assert.Equal(t, target.Email, stored.Email)
			assert.Equal(t, target.SessionVersion+1, stored.SessionVersion)
			assert.True(t, crypto.VerifyPassword(stored.PasswordHash, "An0therSecret!"))
		})
	}
}

func TestResetPasswordRequiresTheControlPlane(t *testing.T) {
	assert.ErrorIs(t,
		NewUserService(nil).ResetPassword(context.Background(), "admin", "target", "An0therSecret!"),
		ErrUsersRequireControlPlane)
}

// The database UUID column resolves alternate textual forms to one account.
// Normalize both reads and writes so this fake would actually rotate the
// actor's password if the service compared only the raw URL parameter.
type canonicalPasswordResetUserRepo struct {
	*fakeUserRepo
}

func canonicalPasswordResetID(id string) string {
	if parsed, err := uuid.Parse(id); err == nil {
		return parsed.String()
	}
	return id
}

func (r *canonicalPasswordResetUserRepo) GetUserByID(ctx context.Context, id string) (repository.User, error) {
	return r.fakeUserRepo.GetUserByID(ctx, canonicalPasswordResetID(id))
}

func (r *canonicalPasswordResetUserRepo) UpdateUserPassword(ctx context.Context, id string, passwordHash string) error {
	return r.fakeUserRepo.UpdateUserPassword(ctx, canonicalPasswordResetID(id), passwordHash)
}

func TestResetPasswordRejectsSelfThroughUUIDAliases(t *testing.T) {
	_, repo, admin, _ := seedUserService(t)
	// Fix the ID to make the uppercase alias reliably distinct from its
	// canonical spelling, regardless of the randomly generated seed UUID.
	delete(repo.users, admin.Id)
	admin.Id = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	repo.users[admin.Id] = admin
	service := NewUserService(&canonicalPasswordResetUserRepo{fakeUserRepo: repo})
	recorder := &fakeAuditRecorder{}
	service.SetOnAuditEvent(recorder.Record)
	ctx := WithPrincipal(context.Background(), &DashboardPrincipal{
		UserId: admin.Id, Email: admin.Email, IsAdmin: true,
	})

	for _, tc := range []struct {
		name  string
		alias string
	}{
		{name: "uppercase", alias: strings.ToUpper(admin.Id)},
		{name: "without hyphens", alias: strings.ReplaceAll(admin.Id, "-", "")},
		{name: "braced", alias: "{" + admin.Id + "}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NotEqual(t, admin.Id, tc.alias)
			err := service.ResetPassword(ctx, admin.Id, tc.alias, "An0therSecret!")
			assert.ErrorIs(t, err, ErrCannotResetOwnPassword)

			stored, err := repo.GetUserByID(ctx, admin.Id)
			require.NoError(t, err)
			assert.Equal(t, admin.PasswordHash, stored.PasswordHash)
			assert.Equal(t, admin.SessionVersion, stored.SessionVersion)
			assert.Empty(t, recorder.events)
		})
	}
}

func TestResetPasswordHonorsLiveSSOEnforcementAndAdminBreakGlass(t *testing.T) {
	service, repo, admin, member := seedUserService(t)
	otherAdmin, err := service.CreateUser(context.Background(), "other-admin@example.com", "Sup3rSecret!", true)
	require.NoError(t, err)
	recorder := &fakeAuditRecorder{}
	service.SetOnAuditEvent(recorder.Record)
	enforced := true
	service.SetSSOEnforced(func(context.Context) bool { return enforced })
	ctx := WithPrincipal(context.Background(), &DashboardPrincipal{
		UserId: admin.Id, Email: admin.Email, IsAdmin: true,
	})

	assert.ErrorIs(t, service.ResetPassword(ctx, admin.Id, member.Id, "An0therSecret!"), ErrPasswordResetDisabledBySSO)
	unchanged, err := repo.GetUserByID(ctx, member.Id)
	require.NoError(t, err)
	assert.Equal(t, member.PasswordHash, unchanged.PasswordHash)
	assert.Equal(t, member.SessionVersion, unchanged.SessionVersion)
	assert.Empty(t, recorder.events)

	// Admin password sign-in remains the SSO recovery route.
	require.NoError(t, service.ResetPassword(ctx, admin.Id, otherAdmin.Id, "An0therSecret!"))
	rotated, err := repo.GetUserByID(ctx, otherAdmin.Id)
	require.NoError(t, err)
	assert.True(t, crypto.VerifyPassword(rotated.PasswordHash, "An0therSecret!"))
	assert.EqualValues(t, 1, rotated.SessionVersion)

	// Reading the callback again allows a member rotation as soon as SSO
	// enforcement is switched off, without restarting the service.
	enforced = false
	require.NoError(t, service.ResetPassword(ctx, admin.Id, member.Id, "An0therSecret!"))
	assert.Len(t, recorder.events, 2)
}

func TestResetPasswordAuditsTheContextActorAndTargetWithoutSecrets(t *testing.T) {
	service, repo, admin, member := seedUserService(t)
	recorder := &fakeAuditRecorder{}
	service.SetOnAuditEvent(recorder.Record)
	ctx := WithPrincipal(context.Background(), &DashboardPrincipal{
		UserId: admin.Id, Email: admin.Email, IsAdmin: true,
	})

	require.NoError(t, service.ResetPassword(ctx, "untrusted-actor-parameter", member.Id, "An0therSecret!"))

	require.Len(t, recorder.events, 1)
	event := recorder.events[0]
	assert.Equal(t, auditlog.ActionUserPasswordChanged, event.Action)
	assert.Equal(t, auditlog.ActorUser, event.ActorType)
	assert.Equal(t, admin.Id, event.ActorID)
	assert.Equal(t, admin.Email, event.ActorDisplay)
	assert.Equal(t, "user", event.TargetType)
	assert.Equal(t, member.Id, event.TargetID)
	assert.Equal(t, member.Email, event.TargetDisplay)
	assert.Equal(t, auditlog.OutcomeSuccess, event.Outcome)
	assert.Equal(t, map[string]any{"reset": true}, event.Metadata)

	stored, err := repo.GetUserByID(ctx, member.Id)
	require.NoError(t, err)
	encoded, err := json.Marshal(event)
	require.NoError(t, err)
	for _, secret := range []string{"Sup3rSecret!", "An0therSecret!", member.PasswordHash, stored.PasswordHash} {
		assert.NotContains(t, string(encoded), secret)
	}
}
