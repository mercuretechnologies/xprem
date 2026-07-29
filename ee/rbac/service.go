// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package rbac

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"xprem/ee/licensing"
	"xprem/internal/auditlog"
	"xprem/internal/services"
	"xprem/internal/store"

	"github.com/google/uuid"
)

// Role is a named, reusable permission bundle. Roles are global: which apps
// one applies to is decided per user in the grants.
type Role struct {
	ID          string
	Name        string
	Permissions []Permission
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// AppGrant is one member's access to one app: an optional role plus direct
// extra permissions. A grant with neither still matters — it makes the app
// visible to the member (read access).
type AppGrant struct {
	AppID string
	// RoleID/RoleName/RolePermissions are nil/empty when the grant carries no
	// role, only direct permissions.
	RoleID           *string
	RoleName         *string
	RolePermissions  []Permission
	ExtraPermissions []Permission
}

// Effective is the union of the role's permissions and the direct ones,
// deduplicated, in catalog order so every surface lists them identically.
func (g AppGrant) Effective() []Permission {
	granted := make(map[Permission]struct{}, len(g.RolePermissions)+len(g.ExtraPermissions))
	for _, p := range g.RolePermissions {
		granted[p] = struct{}{}
	}
	for _, p := range g.ExtraPermissions {
		granted[p] = struct{}{}
	}
	effective := make([]Permission, 0, len(granted))
	for _, p := range AllPermissions { // Not interating over the map so the order is catalog order, not random.
		if _, ok := granted[p]; ok {
			effective = append(effective, p)
		}
	}
	return effective
}

// Has reports whether the grant carries the permission, through its role or
// directly.
func (g AppGrant) Has(perm Permission) bool {
	return slices.Contains(g.RolePermissions, perm) || slices.Contains(g.ExtraPermissions, perm)
}

// GrantInput is the write shape of one grant, as the admin edits it.
type GrantInput struct {
	AppID            string
	RoleID           *string
	ExtraPermissions []Permission
}

// Subject is the authenticated account an authorization decision is made for.
// IsAdmin must come from a fresh users-table read (the middleware does one on
// every guarded request, exactly like the community admin gate), never from
// the JWT claim alone: a revoked admin loses everything immediately, not at
// token expiry. Every entry point below handles the admin bypass itself so no
// call site can forget it.
type Subject struct {
	UserID  string
	IsAdmin bool
}

// RBACRepository persists roles and grants. GetUserAppGrant and
// ListAccessibleAppIDs are the enforcement reads on the dashboard request
// path; a nil grant means the member has no access to the app at all.
type RBACRepository interface {
	ListRoles(ctx context.Context) ([]Role, error)
	GetRoleByID(ctx context.Context, id string) (Role, error)
	InsertRole(ctx context.Context, role Role) (Role, error)
	UpdateRole(ctx context.Context, id string, name string, permissions []Permission) error
	DeleteRole(ctx context.Context, id string) error
	ListUserGrants(ctx context.Context, userID string) ([]AppGrant, error)
	GetUserAppGrant(ctx context.Context, userID string, appID string) (*AppGrant, error)
	ReplaceUserGrants(ctx context.Context, userID string, grants []GrantInput) error
	ListAccessibleAppIDs(ctx context.Context, userID string) ([]string, error)
	GrantCountsByUser(ctx context.Context) (map[string]int64, error)
}

var (
	ErrRequiresControlPlane = errors.New("user roles are managed in the database: this deployment runs in stateless mode, which is community edition only")
	ErrRequiresValidLicense = errors.New("user roles require an active enterprise license")
	ErrRoleNotFound         = errors.New("role not found")
	// ErrRoleInUse mirrors the ON DELETE RESTRICT on user_app_grants.role_id.
	ErrRoleInUse = errors.New("this role is still assigned to at least one user: unassign it everywhere first")
	// ErrNoAppAccess is the member-without-a-grant outcome. Its message reads
	// like the app resolver's 404 on purpose: an app the member has no grant
	// on must not even appear to exist.
	ErrNoAppAccess = errors.New("app not found")
)

// ErrPermissionDenied names the permission a granted member is missing, so
// the 403 tells them what to ask their admin for.
type ErrPermissionDenied struct {
	Permission Permission
}

func (e *ErrPermissionDenied) Error() string {
	return fmt.Sprintf("this action requires the %q permission on this app", string(e.Permission))
}

// ValidationError marks admin input the service refused (bad role name,
// unknown permission string, duplicate app in a grants payload).
type ValidationError struct {
	Message string
}

func (e *ValidationError) Error() string {
	return e.Message
}

// RBACService owns the management and the enforcement of user roles and
// per-app grants. Mutations are license-gated (no valid license, no changes);
// reads are not, so the dashboard can always show what exists. Enforcement is
// only consulted while Enabled() is true — without a license the community
// rules apply unchanged (members are read-only, every app visible).
type RBACService struct {
	repo RBACRepository
	// userLookup resolves the fresh admin flag behind every authorization
	// decision and the target of the grants endpoints. Nil in stateless mode,
	// where the session claim is authoritative.
	userLookup UserLookup
	// licenseValid is the live licensing state; a field so same-package tests
	// can pin it without minting signed keys.
	licenseValid func() bool
	// onAuditEvent is the audit emission seam the middlewares report refusals
	// to; nil means denials leave no events.
	onAuditEvent auditlog.RecordFunc
}

// SetOnAuditEvent plugs the audit emission seam. Nil-safe.
func (s *RBACService) SetOnAuditEvent(record auditlog.RecordFunc) {
	s.onAuditEvent = record
}

// recordManagement reports one roles/grants mutation. The actor is the admin
// principal on the request context (these routes are admin-gated, so it is
// present on every real request).
func (s *RBACService) recordManagement(ctx context.Context, action auditlog.Action, targetType string, targetID string, targetDisplay string, metadata map[string]any) {
	if s.onAuditEvent == nil {
		return
	}
	actorID, actorDisplay := "", ""
	if principal := services.PrincipalFromContext(ctx); principal != nil {
		actorID, actorDisplay = principal.UserId, principal.Email
		if actorDisplay == "" {
			actorDisplay = principal.UserId
		}
	}
	s.onAuditEvent(ctx, auditlog.Event{
		ActorType:     auditlog.ActorUser,
		ActorID:       actorID,
		ActorDisplay:  actorDisplay,
		Action:        action,
		TargetType:    targetType,
		TargetID:      targetID,
		TargetDisplay: targetDisplay,
		Outcome:       auditlog.OutcomeSuccess,
		Metadata:      metadata,
	})
}

// NewRBACService accepts a nil repository (stateless mode); every method then
// answers ErrRequiresControlPlane and Enabled() stays false.
func NewRBACService(repo RBACRepository, userLookup UserLookup) *RBACService {
	return &RBACService{repo: repo, userLookup: userLookup, licenseValid: licensing.IsEnterprise}
}

// Enabled reports whether fine-grained roles are being enforced right now.
// When false, callers fall back to the community model: is_admin decides
// everything, grants stay dormant in the database.
func (s *RBACService) Enabled() bool {
	return s.repo != nil && s.licenseValid()
}

func (s *RBACService) requireWritable() error {
	if s.repo == nil {
		return ErrRequiresControlPlane
	}
	if !s.licenseValid() {
		return ErrRequiresValidLicense
	}
	return nil
}

func validatePermissions(perms []Permission) error {
	for _, p := range perms {
		if !IsValidPermission(string(p)) {
			return &ValidationError{Message: fmt.Sprintf("unknown permission %q", string(p))}
		}
	}
	return nil
}

func normalizeRoleName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", &ValidationError{Message: "role name cannot be empty"}
	}
	if len(name) > 255 {
		return "", &ValidationError{Message: "role name cannot exceed 255 characters"}
	}
	return name, nil
}

func (s *RBACService) ListRoles(ctx context.Context) ([]Role, error) {
	if s.repo == nil {
		return nil, ErrRequiresControlPlane
	}
	return s.repo.ListRoles(ctx)
}

func (s *RBACService) CreateRole(ctx context.Context, name string, permissions []Permission) (Role, error) {
	if err := s.requireWritable(); err != nil {
		return Role{}, err
	}
	name, err := normalizeRoleName(name)
	if err != nil {
		return Role{}, err
	}
	if err := validatePermissions(permissions); err != nil {
		return Role{}, err
	}
	role, err := s.repo.InsertRole(ctx, Role{
		ID:          uuid.NewString(),
		Name:        name,
		Permissions: permissions,
	})
	if err != nil {
		return Role{}, err
	}
	s.recordManagement(ctx, auditlog.ActionRoleCreated, "role", role.ID, role.Name,
		map[string]any{"permissions": fromPermissions(role.Permissions)})
	return role, nil
}

func (s *RBACService) UpdateRole(ctx context.Context, id string, name string, permissions []Permission) error {
	if err := s.requireWritable(); err != nil {
		return err
	}
	name, err := normalizeRoleName(name)
	if err != nil {
		return err
	}
	if err := validatePermissions(permissions); err != nil {
		return err
	}
	if err := s.repo.UpdateRole(ctx, id, name, permissions); err != nil {
		return err
	}
	s.recordManagement(ctx, auditlog.ActionRoleUpdated, "role", id, name,
		map[string]any{"permissions": fromPermissions(permissions)})
	return nil
}

func (s *RBACService) DeleteRole(ctx context.Context, id string) error {
	if err := s.requireWritable(); err != nil {
		return err
	}
	// Read before the delete: afterwards there is no row left to name in the
	// audit entry. Best-effort, like the entry itself.
	roleName := id
	if role, err := s.repo.GetRoleByID(ctx, id); err == nil {
		roleName = role.Name
	}
	if err := s.repo.DeleteRole(ctx, id); err != nil {
		return err
	}
	s.recordManagement(ctx, auditlog.ActionRoleDeleted, "role", id, roleName, nil)
	return nil
}

func (s *RBACService) GetUserGrants(ctx context.Context, userID string) ([]AppGrant, error) {
	if s.repo == nil {
		return nil, ErrRequiresControlPlane
	}
	return s.repo.ListUserGrants(ctx, userID)
}

// GrantCountsByUser backs the Users page warning: a member with zero grants
// sees an empty dashboard, and an admin should notice that at a glance.
func (s *RBACService) GrantCountsByUser(ctx context.Context) (map[string]int64, error) {
	if s.repo == nil {
		return nil, ErrRequiresControlPlane
	}
	return s.repo.GrantCountsByUser(ctx)
}

// SetUserGrants replaces every grant of one member in a single transaction.
func (s *RBACService) SetUserGrants(ctx context.Context, userID string, grants []GrantInput) error {
	if err := s.requireWritable(); err != nil {
		return err
	}
	seenApps := make(map[string]struct{}, len(grants))
	for _, grant := range grants {
		if _, dup := seenApps[grant.AppID]; dup {
			return &ValidationError{Message: fmt.Sprintf("app %q appears twice in the grants", grant.AppID)}
		}
		seenApps[grant.AppID] = struct{}{}
		if err := validatePermissions(grant.ExtraPermissions); err != nil {
			return err
		}
	}
	if err := s.repo.ReplaceUserGrants(ctx, userID, grants); err != nil {
		return err
	}
	// Guarded here, not just in recordManagement: the display lookup and the
	// metadata build must cost nothing when nobody listens.
	if s.onAuditEvent != nil {
		targetDisplay := userID
		if s.userLookup != nil {
			if user, err := s.userLookup.GetUserByID(ctx, userID); err == nil {
				targetDisplay = user.Email
			}
		}
		grantsMetadata := make([]map[string]any, len(grants))
		for i, grant := range grants {
			grantMetadata := map[string]any{"app_id": grant.AppID}
			if grant.RoleID != nil {
				grantMetadata["role_id"] = *grant.RoleID
			}
			if len(grant.ExtraPermissions) > 0 {
				grantMetadata["extra_permissions"] = fromPermissions(grant.ExtraPermissions)
			}
			grantsMetadata[i] = grantMetadata
		}
		s.recordManagement(ctx, auditlog.ActionUserGrantsUpdated, "user", userID, targetDisplay,
			map[string]any{"grants": grantsMetadata})
	}
	return nil
}

// Fallback is what a route grants a non-admin member when roles are NOT
// enforced, which happens in stateless mode and on any deployment without a
// valid enterprise license. It is declared per route rather than assumed,
// because the right answer differs: refusing a member the ability to delete a
// branch is the community edition working as intended, refusing them the
// update list they have always read would be a regression.
//
// There is no valid zero value. A caller that forgets to choose gets a
// refusal, not a silent grant.
type Fallback int

const (
	// FallbackAdminOnly: without roles, only admins pass. This is what every
	// permission-gated route did before fallbacks existed.
	FallbackAdminOnly Fallback = iota + 1
	// FallbackAnyMember: without roles, every signed-in member who can see the
	// app passes. The permission still bites once roles are enforced, which is
	// what makes granular control the enterprise feature rather than a
	// restriction community deployments suddenly inherit.
	FallbackAnyMember
)

// Authorize decides one dashboard action on one app. Admins are allowed
// unconditionally. For members, the distinction between the two refusals is
// deliberate: no grant at all reads as a 404 (the app does not exist for this
// member), a missing permission on a granted app reads as a 403 naming the
// permission.
func (s *RBACService) Authorize(ctx context.Context, subject Subject, appID string, perm Permission, fallback Fallback) error {
	if subject.IsAdmin {
		return nil
	}
	// No control plane means stateless mode, where the only account that can
	// exist is ADMIN_EMAIL and it is built with IsAdmin true, so a non-admin
	// subject cannot reach this line. The fallback is deliberately NOT honored
	// here: this branch exists to refuse if that ever stops holding, and a
	// branch whose job is to fail closed must not carry a widening.
	if s.repo == nil {
		return ErrRequiresControlPlane
	}
	// A control plane without a valid license is the reachable case: real
	// accounts, real members, no enforced roles and no grants to consult. The
	// route's own fallback is the only thing left that can decide.
	if !s.licenseValid() {
		if fallback == FallbackAnyMember {
			return nil
		}
		return ErrRequiresValidLicense
	}
	grant, err := s.repo.GetUserAppGrant(ctx, subject.UserID, appID)
	if err != nil {
		return err
	}
	if grant == nil {
		return ErrNoAppAccess
	}
	if !grant.Has(perm) {
		return &ErrPermissionDenied{Permission: perm}
	}
	return nil
}

// CanSeeApp is the read-path sibling of Authorize: any grant on the app,
// whatever its permissions, makes the app visible to a member. Admins see
// everything.
func (s *RBACService) CanSeeApp(ctx context.Context, subject Subject, appID string) (bool, error) {
	if subject.IsAdmin || !s.Enabled() {
		return true, nil
	}
	grant, err := s.repo.GetUserAppGrant(ctx, subject.UserID, appID)
	if err != nil {
		return false, err
	}
	return grant != nil, nil
}

// VisibleApps returns the subject's app scope for list filtering.
// restricted=false means every app is visible (admin, or community fallback);
// when true, only the returned ids (possibly none) are.
func (s *RBACService) VisibleApps(ctx context.Context, subject Subject) (restricted bool, appIDs []string, err error) {
	if subject.IsAdmin || !s.Enabled() {
		return false, nil, nil
	}
	ids, err := s.repo.ListAccessibleAppIDs(ctx, subject.UserID)
	if err != nil {
		return true, nil, err
	}
	return true, ids, nil
}

// VisibleAppsForPrincipal adapts VisibleApps for the community read handlers
// (app list, settings), which know the request principal but not the rbac
// Subject. It resolves the fresh admin flag itself and answers
// restricted=false whenever nothing must be filtered (admin, CLI, community
// fallback). A principal whose account no longer exists gets an empty scope
// rather than an error: a dead session should see nothing, not break the
// endpoint.
func (s *RBACService) VisibleAppsForPrincipal(ctx context.Context, principal *services.DashboardPrincipal) (restricted bool, visible map[string]bool, err error) {
	if principal == nil || !s.Enabled() {
		return false, nil, nil
	}
	subject := Subject{UserID: principal.UserId, IsAdmin: principal.IsAdmin}
	if s.userLookup != nil {
		user, err := s.userLookup.GetUserByID(ctx, principal.UserId)
		if err != nil {
			if notFoundErr := (*store.ErrResourceNotFound)(nil); errors.As(err, &notFoundErr) {
				return true, map[string]bool{}, nil
			}
			return false, nil, err
		}
		subject.IsAdmin = user.IsAdmin
	}
	restricted, ids, err := s.VisibleApps(ctx, subject)
	if err != nil || !restricted {
		return restricted, nil, err
	}
	visible = make(map[string]bool, len(ids))
	for _, id := range ids {
		visible[id] = true
	}
	return true, visible, nil
}

// EffectivePermissionsByApp is the dashboard's permission map: for each
// granted app, the member's effective permissions. Served to the UI so it can
// hide what the server would refuse anyway.
func (s *RBACService) EffectivePermissionsByApp(ctx context.Context, userID string) (map[string][]Permission, error) {
	if s.repo == nil {
		return nil, ErrRequiresControlPlane
	}
	grants, err := s.repo.ListUserGrants(ctx, userID)
	if err != nil {
		return nil, err
	}
	byApp := make(map[string][]Permission, len(grants))
	for _, grant := range grants {
		byApp[grant.AppID] = grant.Effective()
	}
	return byApp, nil
}
