package services

import (
	"context"
	"xprem/config"
)

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
// request: which app it may act on and which API key it was. KeyID is 0 and
// KeyName empty in stateless mode, where the credential is the app's Expo
// token, not a named key.
//
// It says nothing about what the credential is allowed to do. That decision is
// the router's, taken once per request against the route's declaration, and it
// is not re-derived downstream: handlers no more re-check a branch scope than
// they re-check the RBAC permission or the dashboard session.
type CliCredential struct {
	AppID   string
	KeyID   int64
	KeyName string
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

// PrincipalExtraKey is where the OAuth verifier stores the request's principal
// in a bearer TokenInfo's Extra map; the MCP tool layer reads it back with
// PrincipalFromExtra. It lives here for the same reason the context keys do:
// the identity is this package's domain, and both sides of the contract must
// share one definition without importing each other.
const PrincipalExtraKey = "principal"

// PrincipalFromExtra retrieves the principal stored under PrincipalExtraKey,
// or nil.
func PrincipalFromExtra(extra map[string]any) *DashboardPrincipal {
	principal, _ := extra[PrincipalExtraKey].(*DashboardPrincipal)
	return principal
}

type appContextKey struct{}

// WithApp stores the app the app-resolver middleware loaded for the route's
// APP_ID.
func WithApp(ctx context.Context, app config.AppConfig) context.Context {
	return context.WithValue(ctx, appContextKey{}, app)
}

// AppFromContext returns the app stored by WithApp, or nil off an app-scoped
// route.
func AppFromContext(ctx context.Context) *config.AppConfig {
	app, ok := ctx.Value(appContextKey{}).(config.AppConfig)
	if !ok {
		return nil
	}
	return &app
}
