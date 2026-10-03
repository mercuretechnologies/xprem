package mcptools

import (
	"context"
	"errors"
	"log"
	"strings"

	"xprem/internal/types"

	mcpprot "github.com/modelcontextprotocol/go-sdk/mcp"
)

// apiKeysManageAccess gates minting and revoking, like the route twin
// (PermApiKeysManage). Listing is a plain read, as GET /apiKeys is AnyViewer.
var apiKeysManageAccess = Access{Perm: "apikeys:manage", Fallback: FallbackAdminOnly}

type GetApiKeysInput struct {
	AppId string `json:"appId" jsonschema:"the app id, as returned by get_apps"`
}

type GetApiKeysOutput struct {
	ApiKeys []types.ApiKeyMetadata `json:"apiKeys"`
}

func getApiKeysHandler(deps Deps) func(ctx context.Context, req *mcpprot.CallToolRequest, input GetApiKeysInput) (*mcpprot.CallToolResult, GetApiKeysOutput, error) {
	return func(ctx context.Context, req *mcpprot.CallToolRequest, input GetApiKeysInput) (*mcpprot.CallToolResult, GetApiKeysOutput, error) {
		principal := PrincipalFromRequest(req)
		if principal == nil {
			return nil, GetApiKeysOutput{}, errors.New("no authenticated account on this session")
		}
		if err := requireAppVisible(ctx, deps, principal, input.AppId); err != nil {
			return nil, GetApiKeysOutput{}, err
		}
		apiKeys, err := deps.ApiKeys.GetApiKeysMetadata(ctx, input.AppId)
		if err != nil {
			log.Printf("mcp get_api_keys failed for app %s: %v", input.AppId, err)
			return nil, GetApiKeysOutput{}, errors.New("could not list the API keys, try again later")
		}
		output := GetApiKeysOutput{ApiKeys: []types.ApiKeyMetadata{}}
		output.ApiKeys = append(output.ApiKeys, apiKeys...)
		return nil, output, nil
	}
}

func registerGetApiKeys(server *mcpprot.Server, deps Deps) {
	mcpprot.AddTool(server, &mcpprot.Tool{
		Name:        "get_api_keys",
		Description: "The publishing API keys of an app (appId required): id, name, masked hint, creation and last use. Never returns key material.",
		Annotations: &mcpprot.ToolAnnotations{Title: "List API keys", ReadOnlyHint: true},
	}, getApiKeysHandler(deps))
}

type CreateApiKeyInput struct {
	AppId string `json:"appId" jsonschema:"the app id, as returned by get_apps"`
	Name  string `json:"name" jsonschema:"a label for the key, e.g. the CI that will publish with it"`
}

type CreateApiKeyOutput struct {
	// ApiKey is the plaintext key. The server keeps only its hash, so this is
	// the one time it can be read.
	ApiKey string `json:"apiKey"`
	Name   string `json:"name"`
}

func createApiKeyHandler(deps Deps) func(ctx context.Context, req *mcpprot.CallToolRequest, input CreateApiKeyInput) (*mcpprot.CallToolResult, CreateApiKeyOutput, error) {
	return func(ctx context.Context, req *mcpprot.CallToolRequest, input CreateApiKeyInput) (*mcpprot.CallToolResult, CreateApiKeyOutput, error) {
		ctx, principal, err := requireAppPermission(ctx, deps, req, input.AppId, apiKeysManageAccess)
		if err != nil {
			return nil, CreateApiKeyOutput{}, err
		}
		name := strings.TrimSpace(input.Name)
		if name == "" {
			return nil, CreateApiKeyOutput{}, errors.New("name is required")
		}
		apiKey, err := deps.ApiKeys.GenerateAPIKey(ctx, input.AppId, name)
		if err != nil {
			return nil, CreateApiKeyOutput{}, writeError(err, "create the API key", "mcp create_api_key", principal, input.AppId)
		}
		if deps.OnApiKeysChanged != nil {
			deps.OnApiKeysChanged(input.AppId)
		}
		log.Printf("mcp create_api_key: user %s created API key %q on app %s", principal.UserId, name, input.AppId)
		return nil, CreateApiKeyOutput{ApiKey: apiKey, Name: name}, nil
	}
}

func registerCreateApiKey(server *mcpprot.Server, deps Deps) {
	mcpprot.AddTool(server, &mcpprot.Tool{
		Name:        "create_api_key",
		Description: "Create a publishing API key on an app (appId and name required), the token eoas publishes with. The plaintext key is returned once and cannot be read again: hand it to the user or store it as a secret right away. Requires the apikeys:manage permission.",
		Annotations: &mcpprot.ToolAnnotations{
			Title:           "Create API key",
			DestructiveHint: boolPtr(false),
			IdempotentHint:  false,
		},
	}, createApiKeyHandler(deps))
}

type RevokeApiKeyInput struct {
	AppId    string `json:"appId" jsonschema:"the app id, as returned by get_apps"`
	ApiKeyId string `json:"apiKeyId" jsonschema:"the key id, as returned by get_api_keys"`
}

type RevokeApiKeyOutput struct {
	Revoked  bool   `json:"revoked"`
	ApiKeyId string `json:"apiKeyId"`
}

func revokeApiKeyHandler(deps Deps) func(ctx context.Context, req *mcpprot.CallToolRequest, input RevokeApiKeyInput) (*mcpprot.CallToolResult, RevokeApiKeyOutput, error) {
	return func(ctx context.Context, req *mcpprot.CallToolRequest, input RevokeApiKeyInput) (*mcpprot.CallToolResult, RevokeApiKeyOutput, error) {
		ctx, principal, err := requireAppPermission(ctx, deps, req, input.AppId, apiKeysManageAccess)
		if err != nil {
			return nil, RevokeApiKeyOutput{}, err
		}
		if input.ApiKeyId == "" {
			return nil, RevokeApiKeyOutput{}, errors.New("apiKeyId is required; list the keys with get_api_keys")
		}
		if err := deps.ApiKeys.RevokeApiKey(ctx, input.AppId, input.ApiKeyId); err != nil {
			return nil, RevokeApiKeyOutput{}, writeError(err, "revoke the API key", "mcp revoke_api_key", principal, input.AppId)
		}
		if deps.OnApiKeysChanged != nil {
			deps.OnApiKeysChanged(input.AppId)
		}
		log.Printf("mcp revoke_api_key: user %s revoked API key %s on app %s", principal.UserId, input.ApiKeyId, input.AppId)
		return nil, RevokeApiKeyOutput{Revoked: true, ApiKeyId: input.ApiKeyId}, nil
	}
}

func registerRevokeApiKey(server *mcpprot.Server, deps Deps) {
	mcpprot.AddTool(server, &mcpprot.Tool{
		Name:        "revoke_api_key",
		Description: "Revoke a publishing API key of an app (appId and apiKeyId required). Anything publishing with it starts failing at once. Requires the apikeys:manage permission.",
		Annotations: &mcpprot.ToolAnnotations{
			Title:           "Revoke API key",
			DestructiveHint: boolPtr(true),
			IdempotentHint:  false,
		},
	}, revokeApiKeyHandler(deps))
}
