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
		principal := PrincipalFromRequest(req)
		if principal == nil {
			return nil, GetCertificateOutput{}, errors.New("no authenticated account on this session")
		}
		if input.AppId == "" {
			return nil, GetCertificateOutput{}, errors.New("appId is required; list the apps with get_apps")
		}
		if err := deps.Authorize(ctx, principal, input.AppId, certificateAccess); err != nil {
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
	}, getCertificateHandler(deps))
}
