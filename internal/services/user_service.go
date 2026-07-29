package services

import (
	"context"
	"errors"
	"net/mail"
	"xprem/internal/auditlog"
	"xprem/internal/crypto"
	"xprem/internal/store"

	"github.com/google/uuid"
)

// UserRepository is the users table. It has no bucket implementation on
// purpose: user accounts only exist on the control plane, stateless mode
// authenticates against ADMIN_EMAIL/ADMIN_PASSWORD and never touches a store.
type UserRepository interface {
	InsertUser(ctx context.Context, params store.InsertUserParameters) (store.User, error)
	GetUserByEmail(ctx context.Context, email string) (store.User, error)
	GetUserByID(ctx context.Context, id string) (store.User, error)
	GetUsers(ctx context.Context) ([]store.User, error)
	// DeleteUserByID, UpdateUserIsAdmin and UpdateUserEnabled enforce the "at
	// least one enabled admin" invariant atomically
	// (store.ErrWouldLeaveNoAdmin): a check-then-write at this level would race
	// with concurrent demotions/deletions/disables.
	DeleteUserByID(ctx context.Context, id string) error
	// The three writes below also retire the account's sessions, in the same
	// statement, whenever the change is one its live sessions must not survive:
	// always for a new password, and on the losing direction only for the two
	// flags. Doing it in the statement is what makes the revocation impossible
	// to skip on a stale read or lose to a failure between two calls.
	UpdateUserPassword(ctx context.Context, id string, passwordHash string) error
	UpdateUserIsAdmin(ctx context.Context, id string, isAdmin bool) error
	UpdateUserEnabled(ctx context.Context, id string, enabled bool) error
	// BumpUserSessionVersion retires them on their own, with no other change to
	// the account. Only the replay response needs that: it has proof a
	// credential leaked, and nothing about the row itself is wrong.
	BumpUserSessionVersion(ctx context.Context, id string) error
	TouchUserLastConnected(ctx context.Context, id string) error
}

// Business-rule violations, mapped to explicit 4xx responses by the handlers.
var (
	ErrUsersRequireControlPlane  = errors.New("user accounts are managed in the database: this deployment runs in stateless mode, where the only account comes from ADMIN_EMAIL and ADMIN_PASSWORD")
	ErrCannotChangeOwnAdminFlag  = errors.New("you cannot change your own admin status")
	ErrCannotDeleteOwnAccount    = errors.New("you cannot delete your own account")
	ErrCannotDisableOwnAccount   = errors.New("you cannot disable your own account")
	ErrLastAdmin                 = errors.New("there must always be at least one enabled admin")
	ErrInvalidCurrentPassword    = errors.New("the current password is incorrect")
	ErrUserCreationDisabledBySSO = errors.New("SSO is active: accounts are provisioned automatically on their first SSO sign-in")
)

// ValidationError wraps a user-input validation failure (email format,
// password policy) so handlers answer 400 with the actionable message instead
// of an opaque 500.
type ValidationError struct {
	Reason error
}

func (e *ValidationError) Error() string { return e.Reason.Error() }
func (e *ValidationError) Unwrap() error { return e.Reason }

// UserService owns dashboard user accounts and the two invariants around the
// admin flag: nobody can change their own, and the last admin can neither be
// demoted nor deleted — so the dashboard can never lock itself out.
type UserService struct {
	userRepo UserRepository
	// ssoEnforced reports whether SSO is currently active (configured, enabled
	// and licensed). Injected by the enterprise wiring; nil means never
	// enforced. While enforced, accounts arrive through SSO provisioning and
	// manual creation is refused.
	ssoEnforced func(context.Context) bool
	// onAuditEvent is the audit emission seam; nil (community) means account
	// changes leave no events.
	onAuditEvent auditlog.RecordFunc
}

// SetOnAuditEvent plugs the audit emission seam (see SetSSOEnforced for the
// pattern). Nil-safe.
func (s *UserService) SetOnAuditEvent(record auditlog.RecordFunc) {
	s.onAuditEvent = record
}

// recordUserEvent reports one account mutation. The actor comes from the
// request context (see auditActorFromContext); the actorUserId parameters
// some methods still take exist for their business rules, never for the
// audit trail.
func (s *UserService) recordUserEvent(ctx context.Context, action auditlog.Action, target store.User, metadata map[string]any) {
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        action,
		TargetType:    "user",
		TargetID:      target.Id,
		TargetDisplay: target.Email,
		Metadata:      metadata,
	})
}

// NewUserService accepts a nil repository (stateless mode); every method then
// answers ErrUsersRequireControlPlane.
func NewUserService(userRepo UserRepository) *UserService {
	return &UserService{
		userRepo: userRepo,
	}
}

// SetSSOEnforced injects the live "SSO is active" signal (see the field doc).
func (s *UserService) SetSSOEnforced(enforced func(context.Context) bool) {
	s.ssoEnforced = enforced
}

func (s *UserService) requireControlPlane() error {
	if s.userRepo == nil {
		return ErrUsersRequireControlPlane
	}
	return nil
}

func (s *UserService) GetUsers(ctx context.Context) ([]store.User, error) {
	if err := s.requireControlPlane(); err != nil {
		return nil, err
	}
	return s.userRepo.GetUsers(ctx)
}

func (s *UserService) GetMe(ctx context.Context, userId string, email string) (store.User, error) {
	if s.userRepo == nil {
		return store.User{Email: email, IsAdmin: true, Enabled: true}, nil
	}
	return s.userRepo.GetUserByID(ctx, userId)
}

func (s *UserService) CreateUser(ctx context.Context, email string, password string, isAdmin bool) (store.User, error) {
	if err := s.requireControlPlane(); err != nil {
		return store.User{}, err
	}
	if s.ssoEnforced != nil && s.ssoEnforced(ctx) {
		return store.User{}, ErrUserCreationDisabledBySSO
	}
	normalizedEmail := store.NormalizeEmail(email)
	// The addr comparison rejects mailbox forms like "Jane <jane@acme.dev>":
	// ParseAddress accepts them, but the stored string would never match a
	// login lookup.
	if addr, err := mail.ParseAddress(normalizedEmail); err != nil || addr.Address != normalizedEmail {
		return store.User{}, &ValidationError{Reason: errors.New("invalid email address")}
	}
	if err := crypto.ValidatePasswordPolicy(password); err != nil {
		return store.User{}, &ValidationError{Reason: err}
	}
	passwordHash, err := crypto.HashPassword(password)
	if err != nil {
		return store.User{}, err
	}
	user, err := s.userRepo.InsertUser(ctx, store.InsertUserParameters{
		ID:           uuid.New().String(),
		Email:        normalizedEmail,
		PasswordHash: passwordHash,
		IsAdmin:      isAdmin,
		// An admin creating an account by hand is the approval: nothing to
		// validate afterwards.
		Enabled: true,
	})
	if err != nil {
		return store.User{}, err
	}
	s.recordUserEvent(ctx, auditlog.ActionUserCreated, user, map[string]any{"is_admin": isAdmin})
	return user, nil
}

func (s *UserService) DeleteUser(ctx context.Context, actorUserId string, targetUserId string) error {
	if err := s.requireControlPlane(); err != nil {
		return err
	}
	if actorUserId == targetUserId {
		return ErrCannotDeleteOwnAccount
	}
	// Read before the delete: afterwards there is no row left to name in the
	// audit entry. Best-effort, like the entry itself.
	target, targetErr := s.userRepo.GetUserByID(ctx, targetUserId)
	if err := s.userRepo.DeleteUserByID(ctx, targetUserId); err != nil {
		if errors.Is(err, store.ErrWouldLeaveNoAdmin) {
			return ErrLastAdmin
		}
		return err
	}
	if targetErr != nil {
		target = store.User{Id: targetUserId, Email: targetUserId}
	}
	s.recordUserEvent(ctx, auditlog.ActionUserDeleted, target, nil)
	return nil
}

func (s *UserService) SetUserAdmin(ctx context.Context, actorUserId string, targetUserId string, isAdmin bool) error {
	if err := s.requireControlPlane(); err != nil {
		return err
	}
	// Covers "you cannot remove your own admin flag": granting yourself a flag
	// you already hold is the only other case, and it is a no-op anyway.
	if actorUserId == targetUserId {
		return ErrCannotChangeOwnAdminFlag
	}
	target, targetErr := s.userRepo.GetUserByID(ctx, targetUserId)
	if err := s.userRepo.UpdateUserIsAdmin(ctx, targetUserId, isAdmin); err != nil {
		if errors.Is(err, store.ErrWouldLeaveNoAdmin) {
			return ErrLastAdmin
		}
		return err
	}
	// An idempotent PATCH is not a privilege change: no event. Unknown
	// previous state records anyway — losing an escalation would be worse
	// than a duplicate. The compare races concurrent admins editing the same
	// account: a stale pre-read can mislabel a real transition as a no-op.
	// Accepted: the window is two humans on the same row within milliseconds,
	// and closing it would need the store to return the previous value.
	if targetErr == nil && target.IsAdmin == isAdmin {
		return nil
	}
	if targetErr != nil {
		target = store.User{Id: targetUserId, Email: targetUserId}
	}
	action := auditlog.ActionUserAdminGranted
	if !isAdmin {
		action = auditlog.ActionUserAdminRevoked
	}
	s.recordUserEvent(ctx, action, target, nil)
	return nil
}

// SetUserEnabled approves or revokes an account without deleting it. Disabling
// takes effect on the account's next token refresh at the latest, since a live
// session token is only re-checked against the database there.
func (s *UserService) SetUserEnabled(ctx context.Context, actorUserId string, targetUserId string, enabled bool) error {
	if err := s.requireControlPlane(); err != nil {
		return err
	}
	// Same reasoning as SetUserAdmin: enabling an account you are signed in
	// with is a no-op, so the only meaningful self-target is locking yourself
	// out.
	if actorUserId == targetUserId {
		return ErrCannotDisableOwnAccount
	}
	target, targetErr := s.userRepo.GetUserByID(ctx, targetUserId)
	if err := s.userRepo.UpdateUserEnabled(ctx, targetUserId, enabled); err != nil {
		if errors.Is(err, store.ErrWouldLeaveNoAdmin) {
			return ErrLastAdmin
		}
		return err
	}
	if targetErr == nil && target.Enabled == enabled {
		return nil
	}
	if targetErr != nil {
		target = store.User{Id: targetUserId, Email: targetUserId}
	}
	// Approving a pending account has its own name in the catalog; revoking
	// access is a plain update carrying the new state.
	if enabled {
		s.recordUserEvent(ctx, auditlog.ActionUserApproved, target, nil)
	} else {
		s.recordUserEvent(ctx, auditlog.ActionUserUpdated, target, map[string]any{"enabled": false})
	}
	return nil
}

// ChangePassword re-checks the current password even though the caller holds a
// valid session: a stolen or forgotten-open session must not be enough to take
// the account over by rotating its password.
func (s *UserService) ChangePassword(ctx context.Context, userId string, currentPassword string, newPassword string) error {
	if err := s.requireControlPlane(); err != nil {
		return err
	}
	user, err := s.userRepo.GetUserByID(ctx, userId)
	if err != nil {
		return err
	}
	if !crypto.VerifyPassword(user.PasswordHash, currentPassword) {
		return ErrInvalidCurrentPassword
	}
	if err := crypto.ValidatePasswordPolicy(newPassword); err != nil {
		return &ValidationError{Reason: err}
	}
	passwordHash, err := crypto.HashPassword(newPassword)
	if err != nil {
		return err
	}
	// The store retires every session of the account in the same statement, so
	// the new password and the death of the sessions minted under the old one
	// either both land or neither does.
	if err := s.userRepo.UpdateUserPassword(ctx, userId, passwordHash); err != nil {
		return err
	}
	if s.onAuditEvent != nil {
		s.onAuditEvent(ctx, auditlog.Event{
			ActorType:     auditlog.ActorUser,
			ActorID:       user.Id,
			ActorDisplay:  user.Email,
			Action:        auditlog.ActionUserPasswordChanged,
			TargetType:    "user",
			TargetID:      user.Id,
			TargetDisplay: user.Email,
			Outcome:       auditlog.OutcomeSuccess,
		})
	}
	return nil
}
