package services

import (
	"context"
	"errors"
	"expo-open-ota/internal/store"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeUserRepo is an in-memory UserRepository, enough to exercise the
// service's business rules without a database.
type fakeUserRepo struct {
	users map[string]store.User
}

func newFakeUserRepo() *fakeUserRepo {
	return &fakeUserRepo{users: map[string]store.User{}}
}

func (r *fakeUserRepo) InsertUser(_ context.Context, params store.InsertUserParameters) (store.User, error) {
	email := store.NormalizeEmail(params.Email)
	for _, user := range r.users {
		if user.Email == email {
			return store.User{}, &store.ErrResourceAlreadyExists{Resource: "user", Identifier: email}
		}
	}
	user := store.User{Id: params.ID, Email: email, PasswordHash: params.PasswordHash, IsAdmin: params.IsAdmin, Enabled: params.Enabled}
	r.users[params.ID] = user
	return user, nil
}

func (r *fakeUserRepo) GetUserByEmail(_ context.Context, email string) (store.User, error) {
	normalizedEmail := store.NormalizeEmail(email)
	for _, user := range r.users {
		if user.Email == normalizedEmail {
			return user, nil
		}
	}
	return store.User{}, &store.ErrResourceNotFound{Resource: "user", Identifier: normalizedEmail}
}

func (r *fakeUserRepo) GetUserByID(_ context.Context, id string) (store.User, error) {
	user, ok := r.users[id]
	if !ok {
		return store.User{}, &store.ErrResourceNotFound{Resource: "user", Identifier: id}
	}
	return user, nil
}

func (r *fakeUserRepo) GetUsers(_ context.Context) ([]store.User, error) {
	users := make([]store.User, 0, len(r.users))
	for _, user := range r.users {
		users = append(users, user)
	}
	return users, nil
}

func (r *fakeUserRepo) DeleteUserByID(_ context.Context, id string) error {
	user, ok := r.users[id]
	if !ok {
		return &store.ErrResourceNotFound{Resource: "user", Identifier: id}
	}
	if user.IsAdmin && r.adminCount() <= 1 {
		return store.ErrWouldLeaveNoAdmin
	}
	delete(r.users, id)
	return nil
}

// The three writes below mirror what the SQL does in one statement: they
// retire the account's sessions along with the change, on the losing direction
// only for the two flags. A fake that skipped the bump would let every
// revocation test pass on the enabled/is_admin re-read alone.
func (r *fakeUserRepo) UpdateUserPassword(_ context.Context, id string, passwordHash string) error {
	user, ok := r.users[id]
	if !ok {
		return &store.ErrResourceNotFound{Resource: "user", Identifier: id}
	}
	user.PasswordHash = passwordHash
	user.SessionVersion++
	r.users[id] = user
	return nil
}

func (r *fakeUserRepo) UpdateUserIsAdmin(_ context.Context, id string, isAdmin bool) error {
	user, ok := r.users[id]
	if !ok {
		return &store.ErrResourceNotFound{Resource: "user", Identifier: id}
	}
	if user.IsAdmin && !isAdmin && r.adminCount() <= 1 {
		return store.ErrWouldLeaveNoAdmin
	}
	if user.IsAdmin && !isAdmin {
		user.SessionVersion++
	}
	user.IsAdmin = isAdmin
	r.users[id] = user
	return nil
}

func (r *fakeUserRepo) UpdateUserEnabled(_ context.Context, id string, enabled bool) error {
	user, ok := r.users[id]
	if !ok {
		return &store.ErrResourceNotFound{Resource: "user", Identifier: id}
	}
	if user.IsAdmin && user.Enabled && !enabled && r.adminCount() <= 1 {
		return store.ErrWouldLeaveNoAdmin
	}
	if user.Enabled && !enabled {
		user.SessionVersion++
	}
	user.Enabled = enabled
	r.users[id] = user
	return nil
}

func (r *fakeUserRepo) BumpUserSessionVersion(_ context.Context, id string) error {
	user, ok := r.users[id]
	if !ok {
		return &store.ErrResourceNotFound{Resource: "user", Identifier: id}
	}
	user.SessionVersion++
	r.users[id] = user
	return nil
}

func (r *fakeUserRepo) TouchUserLastConnected(_ context.Context, id string) error {
	user, ok := r.users[id]
	if !ok {
		return &store.ErrResourceNotFound{Resource: "user", Identifier: id}
	}
	now := time.Now()
	user.LastConnectedAt = &now
	r.users[id] = user
	return nil
}

// adminCount backs the same "at least one enabled admin" guard the guarded SQL
// queries enforce in the real store. A disabled admin cannot sign in, so it is
// no safety net and does not count.
func (r *fakeUserRepo) adminCount() int64 {
	var count int64
	for _, user := range r.users {
		if user.IsAdmin && user.Enabled {
			count++
		}
	}
	return count
}

func seedUserService(t *testing.T) (*UserService, *fakeUserRepo, store.User, store.User) {
	t.Helper()
	repo := newFakeUserRepo()
	service := NewUserService(repo)
	admin, err := service.CreateUser(context.Background(), "admin@example.com", "Sup3rSecret!", true)
	require.NoError(t, err)
	member, err := service.CreateUser(context.Background(), "member@example.com", "Sup3rSecret!", false)
	require.NoError(t, err)
	return service, repo, admin, member
}

func TestCreateUserValidations(t *testing.T) {
	service := NewUserService(newFakeUserRepo())

	_, err := service.CreateUser(context.Background(), "not-an-email", "Sup3rSecret!", false)
	assert.ErrorContains(t, err, "invalid email address")

	// Mailbox forms parse, but would be stored verbatim and never match a
	// login lookup — refused.
	_, err = service.CreateUser(context.Background(), "Jane <jane@example.com>", "Sup3rSecret!", false)
	assert.ErrorContains(t, err, "invalid email address")

	_, err = service.CreateUser(context.Background(), "user@example.com", "weak", false)
	assert.ErrorContains(t, err, "password does not meet the policy")

	created, err := service.CreateUser(context.Background(), "  User@Example.COM ", "Sup3rSecret!", false)
	require.NoError(t, err)
	assert.Equal(t, "user@example.com", created.Email)

	// Same address in another casing is the same account.
	_, err = service.CreateUser(context.Background(), "USER@example.com", "Sup3rSecret!", false)
	alreadyExistsErr := (*store.ErrResourceAlreadyExists)(nil)
	assert.ErrorAs(t, err, &alreadyExistsErr)
}

func TestSetUserAdminGuardsOwnFlagAndLastAdmin(t *testing.T) {
	service, _, admin, member := seedUserService(t)
	ctx := context.Background()

	// Nobody can touch their own flag — not even to "remove their admin".
	assert.ErrorIs(t, service.SetUserAdmin(ctx, admin.Id, admin.Id, false), ErrCannotChangeOwnAdminFlag)

	// Promoting the member works, and demoting the original admin then does
	// too, because another admin remains.
	require.NoError(t, service.SetUserAdmin(ctx, admin.Id, member.Id, true))
	require.NoError(t, service.SetUserAdmin(ctx, member.Id, admin.Id, false))

	// member is now the last admin: demoting them must be refused.
	assert.ErrorIs(t, service.SetUserAdmin(ctx, admin.Id, member.Id, false), ErrLastAdmin)
}

func TestCreateUserLandsEnabled(t *testing.T) {
	// An admin creating the account by hand is the approval; nothing should be
	// left pending behind them.
	_, _, admin, member := seedUserService(t)
	assert.True(t, admin.Enabled)
	assert.True(t, member.Enabled)
}

func TestSetUserEnabledGuardsSelfAndLastAdmin(t *testing.T) {
	service, repo, admin, member := seedUserService(t)
	ctx := context.Background()

	// Locking yourself out of the dashboard is never a legitimate move.
	assert.ErrorIs(t, service.SetUserEnabled(ctx, admin.Id, admin.Id, false), ErrCannotDisableOwnAccount)

	// admin is the only enabled admin: disabling them (by anyone) is refused,
	// exactly like deleting or demoting them.
	assert.ErrorIs(t, service.SetUserEnabled(ctx, member.Id, admin.Id, false), ErrLastAdmin)

	require.NoError(t, service.SetUserEnabled(ctx, admin.Id, member.Id, false))
	disabled, err := repo.GetUserByID(ctx, member.Id)
	require.NoError(t, err)
	assert.False(t, disabled.Enabled)

	require.NoError(t, service.SetUserEnabled(ctx, admin.Id, member.Id, true))
	reEnabled, err := repo.GetUserByID(ctx, member.Id)
	require.NoError(t, err)
	assert.True(t, reEnabled.Enabled)
}

// A disabled admin cannot sign in, so it must not count as the admin that
// keeps the dashboard reachable: without this, disabling one admin and then
// deleting the other would leave nobody able to get back in.
func TestDisabledAdminIsNoLongerASafetyNet(t *testing.T) {
	service, _, admin, member := seedUserService(t)
	ctx := context.Background()

	require.NoError(t, service.SetUserAdmin(ctx, admin.Id, member.Id, true))
	require.NoError(t, service.SetUserEnabled(ctx, admin.Id, member.Id, false))

	// member is an admin again on paper, but a disabled one: admin is back to
	// being the last usable admin and is protected as such.
	assert.ErrorIs(t, service.SetUserAdmin(ctx, member.Id, admin.Id, false), ErrLastAdmin)
	assert.ErrorIs(t, service.DeleteUser(ctx, member.Id, admin.Id), ErrLastAdmin)
}

func TestDeleteUserGuardsSelfAndLastAdmin(t *testing.T) {
	service, repo, admin, member := seedUserService(t)
	ctx := context.Background()

	assert.ErrorIs(t, service.DeleteUser(ctx, admin.Id, admin.Id), ErrCannotDeleteOwnAccount)

	// admin is the only admin: deleting them (by anyone) must be refused.
	assert.ErrorIs(t, service.DeleteUser(ctx, member.Id, admin.Id), ErrLastAdmin)

	require.NoError(t, service.DeleteUser(ctx, admin.Id, member.Id))
	_, err := repo.GetUserByID(ctx, member.Id)
	notFoundErr := (*store.ErrResourceNotFound)(nil)
	assert.ErrorAs(t, err, &notFoundErr)
}

func TestChangePassword(t *testing.T) {
	service, _, admin, _ := seedUserService(t)
	ctx := context.Background()

	assert.ErrorIs(t, service.ChangePassword(ctx, admin.Id, "wrong-current", "N3wSecret!"), ErrInvalidCurrentPassword)
	assert.ErrorContains(t, service.ChangePassword(ctx, admin.Id, "Sup3rSecret!", "weak"), "password does not meet the policy")

	require.NoError(t, service.ChangePassword(ctx, admin.Id, "Sup3rSecret!", "N3wSecret!"))

	// The old password no longer verifies, the new one does.
	assert.ErrorIs(t, service.ChangePassword(ctx, admin.Id, "Sup3rSecret!", "0therSecret!"), ErrInvalidCurrentPassword)
	require.NoError(t, service.ChangePassword(ctx, admin.Id, "N3wSecret!", "0therSecret!"))
}

// While SSO is enforced, accounts arrive through JIT provisioning: manual
// creation is refused with an explicit error, and reopens when SSO goes away.
func TestCreateUserBlockedWhileSSOEnforced(t *testing.T) {
	service := NewUserService(newFakeUserRepo())
	ctx := context.Background()

	ssoActive := true
	service.SetSSOEnforced(func(context.Context) bool { return ssoActive })

	_, err := service.CreateUser(ctx, "user@example.com", "Sup3rSecret!", false)
	assert.ErrorIs(t, err, ErrUserCreationDisabledBySSO)

	ssoActive = false
	_, err = service.CreateUser(ctx, "user@example.com", "Sup3rSecret!", false)
	assert.NoError(t, err)
}

func TestUserServiceRequiresControlPlane(t *testing.T) {
	service := NewUserService(nil)
	ctx := context.Background()

	_, err := service.GetUsers(ctx)
	assert.ErrorIs(t, err, ErrUsersRequireControlPlane)
	_, err = service.CreateUser(ctx, "user@example.com", "Sup3rSecret!", false)
	assert.ErrorIs(t, err, ErrUsersRequireControlPlane)
	assert.ErrorIs(t, service.DeleteUser(ctx, "a", "b"), ErrUsersRequireControlPlane)
	assert.ErrorIs(t, service.SetUserAdmin(ctx, "a", "b", true), ErrUsersRequireControlPlane)
	assert.ErrorIs(t, service.ChangePassword(ctx, "a", "x", "y"), ErrUsersRequireControlPlane)
}

// DB-mode login checks the bcrypt hash from the users table and mints a
// session whose refresh dies with the account.
func TestDashboardAuthServiceWithUserRepo(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	repo := newFakeUserRepo()
	userService := NewUserService(repo)
	authService := NewDashboardAuthService(repo, newFakeRefreshTokenRepo())
	ctx := context.Background()

	user, err := userService.CreateUser(ctx, "admin@example.com", "Sup3rSecret!", true)
	require.NoError(t, err)

	_, err = authService.LoginWithEmailPassword(ctx, "admin@example.com", "wrong")
	assert.Error(t, err)
	_, err = authService.LoginWithEmailPassword(ctx, "unknown@example.com", "Sup3rSecret!")
	assert.Error(t, err)

	// Never signed in yet: no last connection recorded.
	stored, err := repo.GetUserByID(ctx, user.Id)
	require.NoError(t, err)
	assert.Nil(t, stored.LastConnectedAt)

	session, err := authService.LoginWithEmailPassword(ctx, "Admin@Example.com", "Sup3rSecret!")
	require.NoError(t, err)

	// A successful login records the connection.
	stored, err = repo.GetUserByID(ctx, user.Id)
	require.NoError(t, err)
	assert.NotNil(t, stored.LastConnectedAt)

	principal, err := authService.ValidateSession(session.Token)
	require.NoError(t, err)
	assert.Equal(t, user.Id, principal.UserId)
	assert.Equal(t, "admin@example.com", principal.Email)
	assert.True(t, principal.IsAdmin)

	// A refresh token is not a session token.
	_, err = authService.ValidateSession(session.RefreshToken)
	assert.Error(t, err)

	_, err = authService.RefreshSession(ctx, session.RefreshToken)
	require.NoError(t, err)

	// A deleted user cannot refresh its way back in. The repo-level guard
	// refuses to delete the last admin, so hand it a replacement first.
	_, err = userService.CreateUser(ctx, "second-admin@example.com", "Sup3rSecret!", true)
	require.NoError(t, err)
	require.NoError(t, repo.DeleteUserByID(ctx, user.Id))
	_, err = authService.RefreshSession(ctx, session.RefreshToken)
	assert.Error(t, err)

	// Even if the address is later reused by a new account, the old refresh
	// token stays dead: it is bound to the deleted user's id, not the email.
	_, err = userService.CreateUser(ctx, "admin@example.com", "Sup3rSecret!", true)
	require.NoError(t, err)
	_, err = authService.RefreshSession(ctx, session.RefreshToken)
	assert.Error(t, err)
}

func TestStatelessLoginRequiresAdminEmail(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	t.Setenv("ADMIN_EMAIL", "")
	t.Setenv("ADMIN_PASSWORD", "admin")
	authService := NewDashboardAuthService(nil, nil)

	_, err := authService.LoginWithEmailPassword(context.Background(), "admin@example.com", "admin")
	assert.True(t, errors.Is(err, ErrAdminEmailNotSet))
}
