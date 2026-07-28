// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package rbac

// Permission is one dashboard action a member can be granted on an app.
// Admins never need one: the admin flag bypasses the whole permission model.
// The strings are stored as-is in roles.permissions and
// user_app_grants.extra_permissions, so renaming one is a data migration.
type Permission string

// NoPermission marks a route that no permission guards: any account allowed to see
// the app may call it. It is a value rather than an absence so a route
// declaration has to name it, which keeps "open on purpose" apart from
// "nobody decided".
const NoPermission Permission = ""

const (
	PermAppDelete       Permission = "app:delete"
	PermAppRename       Permission = "app:rename"
	PermCertificateRead Permission = "certificate:read"
	PermBranchCreate    Permission = "branch:create"
	PermBranchDelete    Permission = "branch:delete"
	// PermBranchProtect toggles branch protection on and off. Deliberately
	// separate from PermBranchDelete: protecting production is routine,
	// deleting branches is not.
	PermBranchProtect     Permission = "branch:protect"
	PermChannelCreate     Permission = "channel:create"
	PermChannelDelete     Permission = "channel:delete"
	PermChannelEditBranch Permission = "channel:edit-branch"
	// PermChannelRolloutManage covers the whole lifecycle of a channel
	// rollout (start, adjust the percentage, promote or revert): being able
	// to move a rollout forward and being able to back it out are the same
	// level of trust.
	PermChannelRolloutManage Permission = "channel-rollout:manage"
	// PermUpdateRolloutManage is the per-update sibling: set the rollout
	// percentage of a single update or revert it.
	PermUpdateRolloutManage Permission = "update-rollout:manage"
	// PermApiKeysManage mints and revokes the app's publishing tokens and
	// edits their enterprise restrictions (IP allowlists, protected-branch
	// access).
	PermApiKeysManage Permission = "apikeys:manage"
	// PermIdentityManage edits the device-identity metadata allowlist (the
	// dashboard "Identity" section): which metadata keys are accepted and
	// their types.
	PermIdentityManage Permission = "identity:manage"
	// PermIdentityRead browses the device registry: the device list, one
	// device's detail, the distinct values of a metadata key, and the live
	// count. That is per-device data (the client id, the metadata the app
	// chose to attach, the city and coordinates), so seeing the app is no
	// longer enough to read it.
	//
	// Deliberately NOT covering /identity/update-health: that one is an
	// aggregate per update with no device named, and it feeds the updates
	// table and the rollout card, which every member needs.
	PermIdentityRead Permission = "identity:read"
	// PermObserveRead opens the Observe explorer: the overview, the event and
	// metric series, the breakdowns, the filter metadata and the live map.
	// It also covers the raw log feed, which is the reason this permission
	// exists at all: a log record carries the client id, the session id and a
	// body the application wrote, so it can hold anything the app logged.
	//
	// It also covers the SEGMENTED mode of /observe/update-health/history,
	// which is not obvious from the routing table: that route is open to any
	// app viewer because its plain series is a per-update aggregate the
	// updates table and the rollout card both need, but asking it to split by
	// a device dimension reads device_model, os_version and country_code, so
	// the handler asks for this permission before answering.
	PermObserveRead Permission = "observe:read"
	// PermUpdatePublish republishes a past update and rolls a branch back to
	// the embedded bundle from the dashboard. Both change what every device on
	// the branch runs at the next update check, which is why one permission
	// covers them: being able to put an update back in front of the fleet and
	// being able to take one away are the same level of trust.
	//
	// Deliberately NOT covered by PermUpdateRolloutManage: reverting a rollout
	// acts on a publish someone is already watching, while this acts on any
	// update in the history.
	PermUpdatePublish Permission = "update:publish"
	// PermUpdatePublishProtected is PermUpdatePublish on a PROTECTED branch,
	// and it is required on top of it rather than instead of it. Protection
	// marks the branches a real fleet is on, so this is the dashboard
	// counterpart of an API key's can_access_protected_branches: a member can
	// be trusted to roll staging back without being trusted to roll production
	// back.
	//
	// It only ever applies while protection is enforced, which means with a
	// valid enterprise license. Without one no branch is protected, and this
	// permission gates nothing.
	PermUpdatePublishProtected Permission = "update:publish-protected"
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
	PermChannelRolloutManage,
	PermUpdateRolloutManage,
	PermUpdatePublish,
	PermUpdatePublishProtected,
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

// IsValidPermission reports whether the string is part of the catalog. Every
// write path validates through it so an unknown string can never reach the
// database, where it would silently grant nothing.
func IsValidPermission(p string) bool {
	_, ok := permissionSet[Permission(p)]
	return ok
}
