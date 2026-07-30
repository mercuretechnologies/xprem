package mcptools

import (
	"context"
	"errors"
	"expo-open-ota/internal/services"
	"expo-open-ota/internal/store"
	"expo-open-ota/internal/validation"
	"log"

	mcpprot "github.com/modelcontextprotocol/go-sdk/mcp"
)

// PrincipalFromRequest resolves who this tool call acts as; exported for the
// ee tool package. The handler's ctx derives from the session, not from the
// HTTP request, so the per-request identity travels in the request's
// TokenInfo, where the OAuth middleware's verifier stored it.
func PrincipalFromRequest(req *mcpprot.CallToolRequest) *services.DashboardPrincipal {
	if req == nil || req.Extra == nil || req.Extra.TokenInfo == nil {
		return nil
	}
	return services.PrincipalFromExtra(req.Extra.TokenInfo.Extra)
}

// errAppNotFound reads like a 404 on purpose, mirroring the rbac semantics: a
// member without access must not learn the app exists.
var errAppNotFound = errors.New("app not found")

// boolPtr is for the SDK's optional tool hints.
func boolPtr(value bool) *bool {
	return &value
}

// requireAppPermission is the gate of the app-scoped write tools: it resolves
// the caller and authorizes the action on that app, the same decision the
// route twin takes. Visibility of the tool itself is decided at session
// creation; this is the one that protects the action.
// It returns a context carrying the principal: the services resolve their
// audit actor from it (services.PrincipalFromContext), so without this the
// mutations would be recorded with an empty actor.
func requireAppPermission(ctx context.Context, deps Deps, req *mcpprot.CallToolRequest, appID string, access Access) (context.Context, *services.DashboardPrincipal, error) {
	principal := PrincipalFromRequest(req)
	if principal == nil {
		return ctx, nil, errors.New("no authenticated account on this session")
	}
	if appID == "" {
		return ctx, nil, errors.New("appId is required; list the apps with get_apps")
	}
	if err := deps.Authorize(ctx, principal, appID, access); err != nil {
		return ctx, nil, err
	}
	return services.WithPrincipal(ctx, principal), principal, nil
}

// writeError keeps a write failure readable: the service and store messages
// are actionable (protected branch, active rollout, unknown update), so they
// reach the caller as-is, while unexpected failures are logged and masked.
func writeError(err error, action string, logPrefix string, principal *services.DashboardPrincipal, appID string) error {
	if isActionableWriteError(err) {
		return err
	}
	log.Printf("%s failed for user %s on app %s: %v", logPrefix, principal.UserId, appID, err)
	return errors.New("could not " + action + ", try again later")
}

// isActionableWriteError reports whether a write failure tells the caller
// something it can act on, in which case its message is safe and useful to
// return verbatim. Everything else is infrastructure and gets masked.
func isActionableWriteError(err error) bool {
	var (
		validationErr     *validation.Error
		notFound          *store.ErrResourceNotFound
		alreadyExists     *store.ErrResourceAlreadyExists
		branchProtected   *store.ErrBranchProtected
		branchHasChannels *store.ErrBranchHasActiveChannels
		branchInRollout   *store.ErrBranchInActiveRollout
		channelHasRollout *store.ErrChannelHasActiveRollout
		republishErr      *services.RepublishError
	)
	switch {
	case errors.As(err, &validationErr),
		errors.As(err, &notFound),
		errors.As(err, &alreadyExists),
		errors.As(err, &branchProtected),
		errors.As(err, &branchHasChannels),
		errors.As(err, &branchInRollout),
		errors.As(err, &channelHasRollout),
		errors.As(err, &republishErr):
		return true
	case errors.Is(err, services.ErrActiveRolloutBlocksPublish),
		errors.Is(err, services.ErrRolloutSuperseded),
		errors.Is(err, services.ErrPublishGroupNotFound),
		errors.Is(err, services.ErrNoChangesDetected),
		errors.Is(err, store.ErrNotSupportedInStatelessMode):
		return true
	}
	return false
}

// requireAppVisible gates the viewer-level app-scoped tools: any account that
// may see the app passes, others get the 404 answer.
func requireAppVisible(ctx context.Context, deps Deps, principal *services.DashboardPrincipal, appID string) error {
	if appID == "" {
		return errors.New("appId is required; list the apps with get_apps")
	}
	restricted, visible, err := deps.VisibleApps(ctx, principal)
	if err != nil {
		log.Printf("mcp tools could not resolve the visible apps of user %s: %v", principal.UserId, err)
		return errors.New("could not check the app access, try again later")
	}
	if restricted && !visible[appID] {
		return errAppNotFound
	}
	return nil
}
