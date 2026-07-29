package services

import (
	"context"
	"xprem/internal/auditlog"
)

// auditActorFromContext resolves the audit actor of the request: the
// dashboard principal when one is signed in, the app-scoped API credential
// marker on the CLI paths, empty (an honest unknown) otherwise.
func auditActorFromContext(ctx context.Context) (auditlog.ActorType, string, string) {
	if principal := PrincipalFromContext(ctx); principal != nil {
		display := principal.Email
		if display == "" {
			display = principal.UserId
		}
		return auditlog.ActorUser, principal.UserId, display
	}
	if credential := CliAuthFromContext(ctx); credential != nil {
		display := credential.KeyName
		if display == "" {
			// Stateless Expo tokens and legacy keys carry no name: the app
			// scope is the honest identity left.
			display = "api key (app " + credential.AppID + ")"
		}
		return auditlog.ActorAPIKey, credential.KeyID, display
	}
	return "", "", ""
}

// recordManagementEvent is the shared emission shape of the management
// services: actor resolved from the request context, outcome success, because
// domain actions are only ever recorded once they actually executed. record
// is each service's seam; nil means nobody listens and nothing is built.
func recordManagementEvent(ctx context.Context, record auditlog.RecordFunc, event auditlog.Event) {
	if record == nil {
		return
	}
	event.ActorType, event.ActorID, event.ActorDisplay = auditActorFromContext(ctx)
	event.Outcome = auditlog.OutcomeSuccess
	record(ctx, event)
}
