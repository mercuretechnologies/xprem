package services

import "context"

// The request-identity context keys live here, next to the types and services
// that produce them, not in the HTTP middleware that stamps them: the
// middleware is plumbing, the identity is this package's domain. It also
// keeps the keys readable from any service (the audit emissions resolve
// their actor this way); middleware imports services, so the reverse import
// these helpers would otherwise force is a cycle.

type principalContextKey struct{}

// PrincipalFromContext returns the dashboard principal stored by the auth
// middleware, or nil when the request was authenticated another way (CLI
// credential) or not at all.
func PrincipalFromContext(ctx context.Context) *DashboardPrincipal {
	principal, _ := ctx.Value(principalContextKey{}).(*DashboardPrincipal)
	return principal
}

// WithPrincipal stores a dashboard principal on the context.
func WithPrincipal(ctx context.Context, principal *DashboardPrincipal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, principal)
}

type cliAuthContextKey struct{}

// CliCredential identifies the validated app-scoped CLI credential of a
// request: which app it may act on, which API key it was, and what the router
// authorized it for. KeyID is 0 and KeyName empty in stateless mode, where the
// credential is the app's Expo token, not a named key.
type CliCredential struct {
	AppID   string
	KeyID   int64
	KeyName string
	// AuthorizedBranch is the branch the router ran the access decision on,
	// empty on a route that names none. A handler acting on a branch asserts
	// against it (RequireAuthorizedBranch) rather than trusting the value it
	// read itself: the two come from the same path variable today, and this
	// is what keeps them the same tomorrow.
	AuthorizedBranch string
}

// RequireAuthorizedBranch reports whether a CLI request may act on branchName.
// A request with no CLI credential is not a CLI request and is left alone, and
// a credential authorized for no branch cannot act on one.
func RequireAuthorizedBranch(ctx context.Context, branchName string) bool {
	credential := CliAuthFromContext(ctx)
	if credential == nil {
		return true
	}
	return credential.AuthorizedBranch == branchName
}

// WithCliAuth marks the request as authenticated by an app-scoped CLI
// credential. The marker exists so downstream gates can assert "validated CLI
// request" as a fact instead of inferring it from the absence of a dashboard
// principal, which would silently fail open on a route someone mounts without
// the auth middleware. It doubles as the audit actor of the CLI paths, which
// is why it names the key and not just the app.
func WithCliAuth(ctx context.Context, credential CliCredential) context.Context {
	return context.WithValue(ctx, cliAuthContextKey{}, credential)
}

// CliAuthFromContext returns the validated CLI credential, or nil when the
// request did not authenticate through the CLI path.
func CliAuthFromContext(ctx context.Context) *CliCredential {
	credential, ok := ctx.Value(cliAuthContextKey{}).(CliCredential)
	if !ok {
		return nil
	}
	return &credential
}

// CliAuthAppFromContext returns the app the CLI credential was validated for,
// or "" when the request did not authenticate through the CLI path.
func CliAuthAppFromContext(ctx context.Context) string {
	if credential := CliAuthFromContext(ctx); credential != nil {
		return credential.AppID
	}
	return ""
}
