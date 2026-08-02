// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package rbac

// Permission is one dashboard action a member can be granted on an app;
// admins bypass the whole model. The strings are stored as-is in
// roles.permissions and user_app_grants.extra_permissions, so renaming one is
// a data migration.
type Permission string

// NoPermission marks a route that no permission guards; any account allowed
// to see the app may call it.
const NoPermission Permission = ""

const (
	PermAppDelete       Permission = "app:delete"
	PermAppRename       Permission = "app:rename"
	PermCertificateRead Permission = "certificate:read"
	PermBranchCreate    Permission = "branch:create"
	PermBranchDelete    Permission = "branch:delete"
	// PermBranchProtect toggles the deletion lock on a branch. Deliberately
	// separate from PermBranchDelete: locking production against an accidental
	// delete is routine, deleting branches is not.
	PermBranchProtect     Permission = "branch:protect"
	PermChannelCreate     Permission = "channel:create"
	PermChannelDelete     Permission = "channel:delete"
	PermChannelEditBranch Permission = "channel:edit-branch"
	// PermChannelBranchSurfing opens or closes a channel to branch surfing and
	// sets which branches it exposes. Separate from PermChannelEditBranch:
	// remapping a channel changes what every device gets, while this decides
	// whether devices may pick a branch themselves.
	PermChannelBranchSurfing Permission = "channel:branch-surfing"
	// PermChannelRolloutManage covers the whole lifecycle of a channel
	// rollout: start, adjust the percentage, promote, or revert.
	PermChannelRolloutManage Permission = "channel-rollout:manage"
	// PermUpdateRolloutManage is the per-update sibling: set the rollout
	// percentage of a single update or revert it.
	PermUpdateRolloutManage Permission = "update-rollout:manage"
	// PermApiKeysManage mints and revokes the app's publishing tokens and
	// edits what they are allowed to do (branch access rules, IP allowlists).
	PermApiKeysManage Permission = "apikeys:manage"
	// PermIdentityManage edits the device-identity metadata allowlist (the
	// dashboard "Identity" section): which metadata keys are accepted and
	// their types.
	PermIdentityManage Permission = "identity:manage"
	// PermIdentityRead browses the device registry: the device list, one
	// device's detail, distinct metadata values, and the live count. It does
	// not gate /identity/update-health, which every app viewer needs.
	PermIdentityRead Permission = "identity:read"
	// PermObserveRead opens the Observe explorer: overview, event and metric
	// series, breakdowns, filter metadata, the live map, and the raw log feed.
	// It also gates the per-device split of /observe/update-health/history;
	// the plain aggregate stays open to any app viewer.
	PermObserveRead Permission = "observe:read"
	// PermUpdatePublish republishes a past update or rolls a branch back to
	// the embedded bundle. It is distinct from PermUpdateRolloutManage, which
	// only reverts a rollout already in progress.
	PermUpdatePublish Permission = "update:publish"
)

// AllPermissions is the catalog, in the order the dashboard displays it.
var AllPermissions = []Permission{
	PermAppDelete,
	PermAppRename,
	PermCertificateRead,
	PermBranchCreate,
	PermBranchDelete,
	PermBranchProtect,
	PermChannelCreate,
	PermChannelDelete,
	PermChannelEditBranch,
	PermChannelBranchSurfing,
	PermChannelRolloutManage,
	PermUpdateRolloutManage,
	PermUpdatePublish,
	PermApiKeysManage,
	PermIdentityManage,
	PermIdentityRead,
	PermObserveRead,
}

var permissionSet = func() map[Permission]struct{} {
	set := make(map[Permission]struct{}, len(AllPermissions))
	for _, p := range AllPermissions {
		set[p] = struct{}{}
	}
	return set
}()

// IsValidPermission reports whether the string is part of the catalog.
func IsValidPermission(p string) bool {
	_, ok := permissionSet[Permission(p)]
	return ok
}

// anyMemberPermissions are the catalog entries whose canonical fallback is
// FallbackAnyMember; everything else falls back to admin-only. The route and
// tool declarations pair the same values.
var anyMemberPermissions = map[Permission]bool{
	PermIdentityRead: true,
	PermObserveRead:  true,
}

// DefaultFallback is what gates a permission's actions when roles are not
// enforced.
func DefaultFallback(p Permission) Fallback {
	if anyMemberPermissions[p] {
		return FallbackAnyMember
	}
	return FallbackAdminOnly
}
