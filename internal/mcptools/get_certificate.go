package mcptools

import (
	"context"
	"errors"
	"log"

	mcpprot "github.com/modelcontextprotocol/go-sdk/mcp"
)

// certificateAccess also gates the tool's visibility in the registration
// table; keep both in sync with the route twin (PermCertificateRead).
var certificateAccess = Access{Perm: "certificate:read", Fallback: FallbackAdminOnly}

type GetCertificateInput struct {
	AppId string `json:"appId" jsonschema:"the app id, as returned by get_apps"`
}

type GetCertificateOutput struct {
	// CertificatePEM is the public code-signing certificate, PEM-encoded.
	CertificatePEM string `json:"certificatePem"`
}

func getCertificateHandler(deps Deps) func(ctx context.Context, req *mcpprot.CallToolRequest, input GetCertificateInput) (*mcpprot.CallToolResult, GetCertificateOutput, error) {
	return func(ctx context.Context, req *mcpprot.CallToolRequest, input GetCertificateInput) (*mcpprot.CallToolResult, GetCertificateOutput, error) {
		// This read is audited by the service, hence the principal-bearing ctx.
		ctx, _, err := requireAppPermission(ctx, deps, req, input.AppId, certificateAccess)
		if err != nil {
			return nil, GetCertificateOutput{}, err
		}
		certificate, err := deps.Certificates.RetrieveAppCertificate(ctx, input.AppId)
		if err != nil {
			log.Printf("mcp get_certificate failed for app %s: %v", input.AppId, err)
			return nil, GetCertificateOutput{}, errors.New("could not retrieve the certificate; the app may not use database-managed keys")
		}
		return nil, GetCertificateOutput{CertificatePEM: certificate}, nil
	}
}

func registerGetCertificate(server *mcpprot.Server, deps Deps) {
	mcpprot.AddTool(server, &mcpprot.Tool{
		Name:        "get_certificate",
		Description: "The public code-signing certificate of an app (appId required), PEM-encoded. Requires the certificate:read permission on the app; the download is recorded in the audit log.",
		Annotations: &mcpprot.ToolAnnotations{Title: "App certificate", ReadOnlyHint: true},
	}, getCertificateHandler(deps))
}
