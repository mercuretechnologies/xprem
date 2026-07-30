// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package rbac

import (
	"context"
	"errors"
	"expo-open-ota/ee/licensing"
	"expo-open-ota/internal/auditlog"
	"expo-open-ota/internal/services"
	"expo-open-ota/internal/store"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

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
// extra permissions. A grant with neither still matters, it makes the app
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
	for _, p := range AllPermissions { // Ranges over the catalog, not the map, to keep the order deterministic.
		if _, ok := granted[p]; ok {
			effective = append(effective, p)
		}
	}
	return effective
}

// Has reports whether the grant carries perm, via its role or directly.
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
// IsAdmin must come from a fresh users-table read, never the JWT claim alone,
// so a revoked admin loses access immediately.
type Subject struct {
	UserID  string
	IsAdmin bool
}

// RBACRepository persists roles and grants. A nil GetUserAppGrant result
// means the member has no access to the app.
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
	// ErrNoAppAccess is the member-without-a-grant outcome; its message reads
	// like a 404 on purpose.
	ErrNoAppAccess = errors.New("app not found")
)

// ErrPermissionDenied names the permission a granted member is missing.
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
// per-app grants. Mutations are license-gated; reads are not, and enforcement
// is only consulted while Enabled() is true.
type RBACService struct {
	repo RBACRepository
	// userLookup resolves the fresh admin flag and the grants-endpoint target;
	// nil in stateless mode.
	userLookup UserLookup
	// licenseValid reports whether the enterprise license is active.
	licenseValid func() bool
	// onAuditEvent is the audit emission seam; nil means denials leave no events.
	onAuditEvent auditlog.RecordFunc
}

// SetOnAuditEvent plugs the audit emission seam. Nil-safe.
func (s *RBACService) SetOnAuditEvent(record auditlog.RecordFunc) {
	s.onAuditEvent = record
}

// recordManagement reports one roles/grants mutation; the actor is the admin
// principal on the request context.
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
	// Read before the delete since there is no row left afterwards to name it.
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

// GrantCountsByUser backs the Users page warning about members with zero grants.
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
	// Guarded here so the lookup and metadata build cost nothing when unused.
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

// Fallback is what a route grants a non-admin member when roles are not
// enforced. There is no valid zero value; a caller that forgets to choose
// gets a refusal, not a silent grant.
type Fallback int

const (
	// FallbackAdminOnly: without roles, only admins pass.
	FallbackAdminOnly Fallback = iota + 1
	// FallbackAnyMember: without roles, every signed-in member who can see the
	// app passes; the permission still applies once roles are enforced.
	FallbackAnyMember
)

// Authorize decides one dashboard action on one app; admins are allowed
// unconditionally. For members, no grant at all reads as a 404, while a
// missing permission on a granted app reads as a 403 naming the permission.
func (s *RBACService) Authorize(ctx context.Context, subject Subject, appID string, perm Permission, fallback Fallback) error {
	if subject.IsAdmin {
		return nil
	}
	// Unreachable in stateless mode (the only account is admin), but the
	// fallback is deliberately not honored here since this branch must fail closed.
	if s.repo == nil {
		return ErrRequiresControlPlane
	}
	// A control plane without a valid license is the reachable case; the
	// route's own fallback is what decides here.
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

// AppPermissions is the whole authorization truth of one account on one app:
// every catalog permission, held or not.
type AppPermissions struct {
	AppID   string
	Granted []Permission
	Denied  []Permission
}

// AccountPermissions is the account's full permission picture, one entry per
// app it can see. Apps a member holds no grant on are not listed while roles
// are enforced: they are invisible to the account.
type AccountPermissions struct {
	Role          string
	RolesEnforced bool
	Apps          []AppPermissions
}

// DescribeAccountPermissions answers "what may this account do", app by app.
// allAppIDs is the deployment's app list, supplied by the caller because this
// service does not own the app store: it is the scope for admins (everything
// granted) and for members without enforced roles (what the community
// fallbacks concede, identically on every app).
func (s *RBACService) DescribeAccountPermissions(ctx context.Context, subject Subject, allAppIDs []string) (AccountPermissions, error) {
	description := AccountPermissions{Role: "member", RolesEnforced: s.Enabled()}
	if subject.IsAdmin {
		description.Role = "admin"
	}

	if subject.IsAdmin || !description.RolesEnforced {
		var granted, denied []Permission
		if subject.IsAdmin {
			granted = append([]Permission(nil), AllPermissions...)
		} else {
			for _, perm := range AllPermissions {
				if DefaultFallback(perm) == FallbackAnyMember {
					granted = append(granted, perm)
				} else {
					denied = append(denied, perm)
				}
			}
		}
		sorted := append([]string(nil), allAppIDs...)
		sort.Strings(sorted)
		for _, appID := range sorted {
			description.Apps = append(description.Apps, AppPermissions{AppID: appID, Granted: granted, Denied: denied})
		}
		return description, nil
	}

	byApp, err := s.EffectivePermissionsByApp(ctx, subject.UserID)
	if err != nil {
		return AccountPermissions{}, err
	}
	grantedAppIDs := make([]string, 0, len(byApp))
	for appID := range byApp {
		grantedAppIDs = append(grantedAppIDs, appID)
	}
	sort.Strings(grantedAppIDs)
	for _, appID := range grantedAppIDs {
		held := make(map[Permission]bool, len(byApp[appID]))
		for _, perm := range byApp[appID] {
			held[perm] = true
		}
		app := AppPermissions{AppID: appID}
		for _, perm := range AllPermissions {
			if held[perm] {
				app.Granted = append(app.Granted, perm)
			} else {
				app.Denied = append(app.Denied, perm)
			}
		}
		description.Apps = append(description.Apps, app)
	}
	return description, nil
}

// HasPermissionSomewhere reports whether the subject could pass Authorize for
// perm on at least one app; tool surfaces use it to decide visibility. Its
// branches mirror Authorize, answering false wherever Authorize fails closed.
func (s *RBACService) HasPermissionSomewhere(ctx context.Context, subject Subject, perm Permission, fallback Fallback) (bool, error) {
	if subject.IsAdmin {
		return true, nil
	}
	if s.repo == nil {
		return false, nil
	}
	if !s.licenseValid() {
		return fallback == FallbackAnyMember, nil
	}
	byApp, err := s.EffectivePermissionsByApp(ctx, subject.UserID)
	if err != nil {
		return false, err
	}
	for _, permissions := range byApp {
		for _, candidate := range permissions {
			if candidate == perm {
				return true, nil
			}
		}
	}
	return false, nil
}

// CanSeeApp is the read-path sibling of Authorize: any grant on the app makes
// it visible to a member; admins see everything.
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
// restricted=false means every app is visible.
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

// VisibleAppsForPrincipal adapts VisibleApps for callers that know the
// request principal but not the rbac Subject. A deleted account gets an
// empty scope rather than an error.
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
// granted app, the member's effective permissions.
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
