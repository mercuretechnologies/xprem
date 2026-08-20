// Package auditlog is the audit vocabulary: the event shape, the action
// catalog and the emission seam community code records through. It is a leaf
// package (no imports beyond the standard library) on purpose: services,
// handlers and middlewares reference it without depending on the enterprise
// audit machinery in ee/audit, which consumes these types from the other side.
// Nothing here records anything: the only Recorder implementation that
// persists events lives in ee/audit, behind the enterprise license gate.
package auditlog

import (
	"context"
	"time"
)

// ActorType distinguishes who acted: a signed-in dashboard account, an
// app-scoped API credential (CLI publishes), or the server itself (SSO
// provisioning, scheduled jobs).
type ActorType string

const (
	ActorUser   ActorType = "user"
	ActorAPIKey ActorType = "api_key"
	ActorSystem ActorType = "system"
)

// Outcome is the result of the recorded action. Denied comes from the RBAC
// middleware refusing a permission; failure is reserved for actions that ran
// and failed in a security-relevant way (bad login).
type Outcome string

const (
	OutcomeSuccess Outcome = "success"
	OutcomeDenied  Outcome = "denied"
	OutcomeFailure Outcome = "failure"
)

// Action names follow target.action, past tense (update.rollback is the one
// deliberate exception: Expo vocabulary, a noun). This catalog is the
// documented contract of the audit log (mirrored in the dashboard's filter
// dropdown and in the public docs): call sites must use these constants, and
// adding one here means documenting it.
type Action string

const (
	// Auth and account.
	ActionUserLogin           Action = "user.login"
	ActionUserSSOLogin        Action = "user.sso_login"
	ActionUserPasswordChanged Action = "user.password_changed"

	// Users and privileges.
	ActionUserCreated        Action = "user.created"
	ActionUserUpdated        Action = "user.updated"
	ActionUserDeleted        Action = "user.deleted"
	ActionUserSSOProvisioned Action = "user.sso_provisioned"
	ActionUserSSOLinked      Action = "user.sso_linked"
	ActionUserApproved       Action = "user.approved"
	ActionUserAdminGranted   Action = "user.admin_granted"
	ActionUserAdminRevoked   Action = "user.admin_revoked"
	ActionRoleCreated        Action = "role.created"
	ActionRoleUpdated        Action = "role.updated"
	ActionRoleDeleted        Action = "role.deleted"
	ActionUserGrantsUpdated  Action = "user.grants_updated"

	// Enterprise administration.
	ActionLicenseActivated Action = "license.activated"
	ActionLicenseRemoved   Action = "license.removed"
	ActionLicenseSuspended Action = "license.suspended"
	ActionSSOConfigSaved   Action = "sso_config.saved"
	ActionSSOConfigDeleted Action = "sso_config.deleted"

	// App management.
	ActionAppCreated           Action = "app.created"
	ActionAppRenamed           Action = "app.renamed"
	ActionAppDeleted           Action = "app.deleted"
	ActionChannelCreated       Action = "channel.created"
	ActionChannelDeleted       Action = "channel.deleted"
	ActionChannelBranchMapped  Action = "channel_branch.mapped"
	ActionBranchSurfingUpdated Action = "channel_branch_surfing.updated"
	ActionBranchCreated        Action = "branch.created"
	ActionBranchDeleted        Action = "branch.deleted"

	// Delivery.
	ActionUpdatePublished       Action = "update.published"
	ActionUpdateRollback        Action = "update.rollback"
	ActionUpdateRepublished     Action = "update.republished"
	ActionChannelRolloutStarted Action = "channel_rollout.started"
	ActionChannelRolloutUpdated Action = "channel_rollout.updated"
	ActionChannelRolloutEnded   Action = "channel_rollout.ended"
	ActionUpdateRolloutSet      Action = "update_rollout.set"
	ActionUpdateRolloutReverted Action = "update_rollout.reverted"

	// Credentials and key material. The api_key prefix matches ActorAPIKey's
	// value so grouping by resource and by actor type line up.
	ActionAPIKeyCreated             Action = "api_key.created"
	ActionAPIKeyRevoked             Action = "api_key.revoked"
	ActionAPIKeyRestrictionsUpdated Action = "api_key.restrictions_updated"
	ActionBranchProtectionUpdated   Action = "branch_protection.updated"
	ActionCertificateDownloaded     Action = "certificate.downloaded"
	ActionAppIdentifierCreated        Action = "app_identifier.created"
	ActionAppIdentifierDeleted        Action = "app_identifier.deleted"
	ActionAppIdentifierBuildNumberSet Action = "app_identifier.build_number_set"
	ActionAndroidCredentialsSaved   Action = "android_credentials.saved"
	ActionAndroidCredentialsDeleted Action = "android_credentials.deleted"
	ActionEnvVarUpdated             Action = "env_var.updated"
	ActionEnvVarDeleted             Action = "env_var.deleted"
	ActionEnvVarRevealed            Action = "env_var.revealed"

	// Access control. permission.denied is the single event for authorization
	// refusals (the RBAC middleware emits it, with OutcomeDenied); domain
	// actions are only ever recorded once they actually executed, so one
	// refusal is never representable two ways.
	ActionPermissionDenied Action = "permission.denied"
)

// Event is one audit log entry. Actor and target carry denormalized display
// names taken at write time: entries must keep rendering identically after
// the user, app or key they mention is deleted. AppID empty means the event
// is account-level (users, roles, license, SSO). Metadata never carries
// secrets or full request bodies.
type Event struct {
	ID            int64
	OccurredAt    time.Time
	ActorType     ActorType
	ActorID       string
	ActorDisplay  string
	Action        Action
	TargetType    string
	TargetID      string
	TargetDisplay string
	AppID         string
	Outcome       Outcome
	IP            string
	UserAgent     string
	Metadata      map[string]any
}

// RecordFunc is the emission seam: community services and handlers hold one
// (nil means nobody is listening) and the enterprise wiring plugs
// ee/audit's Record method value in. Implementations must be best-effort:
// recording never fails the caller's request.
type RecordFunc func(ctx context.Context, event Event)

// RequestMeta is the network context of the request an event was emitted
// from. It rides the request context so services can emit events without
// taking *http.Request.
type RequestMeta struct {
	IP        string
	UserAgent string
}

type requestMetaKey struct{}

// WithRequestMeta stamps the network context; the HTTP middleware doing it
// for every request lives in internal/middleware.
func WithRequestMeta(ctx context.Context, meta RequestMeta) context.Context {
	return context.WithValue(ctx, requestMetaKey{}, meta)
}

// MetaFromContext returns the zero RequestMeta outside a request (jobs,
// tests): events then simply carry no network context.
func MetaFromContext(ctx context.Context) RequestMeta {
	meta, _ := ctx.Value(requestMetaKey{}).(RequestMeta)
	return meta
}
