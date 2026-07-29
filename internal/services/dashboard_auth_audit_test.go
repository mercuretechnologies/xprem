package services

import (
	"context"
	"testing"
	"xprem/internal/auditlog"
	"xprem/internal/crypto"
	"xprem/internal/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeAuditRecorder struct{ events []auditlog.Event }

func (f *fakeAuditRecorder) Record(_ context.Context, event auditlog.Event) {
	f.events = append(f.events, event)
}

func seededPasswordUser(t *testing.T, repo *fakeUserRepo, email string, password string, enabled bool) store.User {
	t.Helper()
	hash, err := crypto.HashPassword(password)
	require.NoError(t, err)
	user, err := repo.InsertUser(context.Background(), store.InsertUserParameters{
		ID: "pw-user-1", Email: email, PasswordHash: hash, Enabled: enabled,
	})
	require.NoError(t, err)
	return user
}

func TestLoginSuccessEmitsAuditEvent(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	repo := newFakeUserRepo()
	recorder := &fakeAuditRecorder{}
	authService := NewDashboardAuthService(repo, newFakeRefreshTokenRepo())
	authService.SetOnAuditEvent(recorder.Record)
	user := seededPasswordUser(t, repo, "axel@example.com", "Sup3rSecret!", true)

	_, err := authService.LoginWithEmailPassword(context.Background(), "axel@example.com", "Sup3rSecret!")
	require.NoError(t, err)

	require.Len(t, recorder.events, 1)
	event := recorder.events[0]
	assert.Equal(t, auditlog.ActionUserLogin, event.Action)
	assert.Equal(t, auditlog.OutcomeSuccess, event.Outcome)
	assert.Equal(t, auditlog.ActorUser, event.ActorType)
	assert.Equal(t, user.Id, event.ActorID)
	assert.Equal(t, "axel@example.com", event.ActorDisplay)
	assert.Equal(t, user.Id, event.TargetID)
}

func TestLoginFailuresEmitAuditEvents(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	repo := newFakeUserRepo()
	recorder := &fakeAuditRecorder{}
	authService := NewDashboardAuthService(repo, newFakeRefreshTokenRepo())
	authService.SetOnAuditEvent(recorder.Record)
	seededPasswordUser(t, repo, "pending@example.com", "Sup3rSecret!", false)

	// Unknown account: the attempted email is the only identity the event can
	// carry, the actor id stays empty.
	_, err := authService.LoginWithEmailPassword(context.Background(), "nobody@example.com", "whatever")
	require.Error(t, err)
	require.Len(t, recorder.events, 1)
	assert.Equal(t, auditlog.OutcomeFailure, recorder.events[0].Outcome)
	assert.Empty(t, recorder.events[0].ActorID)
	assert.Equal(t, "nobody@example.com", recorder.events[0].ActorDisplay)
	assert.Equal(t, map[string]any{"reason": "invalid_credentials"}, recorder.events[0].Metadata)

	// Correct password on a not-yet-approved account: its own reason.
	_, err = authService.LoginWithEmailPassword(context.Background(), "pending@example.com", "Sup3rSecret!")
	require.ErrorIs(t, err, ErrAccountPendingApproval)
	require.Len(t, recorder.events, 2)
	assert.Equal(t, map[string]any{"reason": "pending_approval"}, recorder.events[1].Metadata)
}

func TestChangePasswordEmitsAuditEvent(t *testing.T) {
	repo := newFakeUserRepo()
	recorder := &fakeAuditRecorder{}
	userService := NewUserService(repo)
	userService.SetOnAuditEvent(recorder.Record)
	user := seededPasswordUser(t, repo, "axel@example.com", "Sup3rSecret!", true)

	// A rejected attempt (wrong current password) is not a change: no event.
	err := userService.ChangePassword(context.Background(), user.Id, "wrong-current", "N3wSecret!!")
	require.ErrorIs(t, err, ErrInvalidCurrentPassword)
	require.Empty(t, recorder.events)

	require.NoError(t, userService.ChangePassword(context.Background(), user.Id, "Sup3rSecret!", "N3wSecret!!"))
	require.Len(t, recorder.events, 1)
	event := recorder.events[0]
	assert.Equal(t, auditlog.ActionUserPasswordChanged, event.Action)
	assert.Equal(t, auditlog.OutcomeSuccess, event.Outcome)
	assert.Equal(t, user.Id, event.ActorID)
	assert.Equal(t, "axel@example.com", event.ActorDisplay)
	assert.Equal(t, user.Id, event.TargetID)
}

func TestRefreshEmitsNoAuditEvent(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	repo := newFakeUserRepo()
	recorder := &fakeAuditRecorder{}
	authService := NewDashboardAuthService(repo, newFakeRefreshTokenRepo())
	authService.SetOnAuditEvent(recorder.Record)
	seededPasswordUser(t, repo, "axel@example.com", "Sup3rSecret!", true)

	session, err := authService.LoginWithEmailPassword(context.Background(), "axel@example.com", "Sup3rSecret!")
	require.NoError(t, err)
	require.Len(t, recorder.events, 1)

	// The refresh path is session upkeep, not an authentication event.
	_, err = authService.RefreshSession(context.Background(), session.RefreshToken)
	require.NoError(t, err)
	require.Len(t, recorder.events, 1)
}
