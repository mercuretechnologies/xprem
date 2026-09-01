package expo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
	"xprem/config"
	"xprem/internal/types"
)

type HistoryAsset struct {
	Key string `json:"key"`
	// Hash is the base64url SHA-256 the Expo protocol uses.
	Hash          string `json:"hash"`
	ContentType   string `json:"contentType"`
	FileExtension string `json:"fileExtension"`
	Url           string `json:"url"`
}

// HistoryManifest is the parsed manifestFragment of one platform update.
type HistoryManifest struct {
	LaunchAsset HistoryAsset           `json:"launchAsset"`
	Assets      []HistoryAsset         `json:"assets"`
	Extra       map[string]interface{} `json:"extra"`
}

func (m *HistoryManifest) ExpoClientConfig() map[string]interface{} {
	if m.Extra == nil {
		return nil
	}
	expoClient, ok := m.Extra["expoClient"].(map[string]interface{})
	if !ok {
		return nil
	}
	return expoClient
}

type HistoryUpdate struct {
	Id                string
	Group             string
	BranchName        string
	RuntimeVersion    string
	Platform          string
	Message           string
	GitCommitHash     string
	CreatedAt         string
	IsRollBack        bool
	CodeSigned        bool
	ManifestPermalink string
}

type ServedManifest struct {
	Manifest HistoryManifest
	// AssetRequestHeaders is keyed by asset key.
	AssetRequestHeaders map[string]map[string]string
}

// FetchServedManifest reads a permalink; permalinks are capability URLs, no
// auth travels with the request.
func FetchServedManifest(ctx context.Context, permalink string) (*ServedManifest, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, permalink, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "multipart/mixed")
	resp, err := assetHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not fetch the update manifest: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("could not fetch the update manifest: the server answered %s", resp.Status)
	}
	mediaType, mediaParams, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil {
		return nil, fmt.Errorf("could not fetch the update manifest: unreadable content type: %w", err)
	}
	body := io.LimitReader(resp.Body, maxHistoryAssetBytes)
	served := &ServedManifest{}
	if !strings.HasPrefix(mediaType, "multipart/") {
		if err := json.NewDecoder(body).Decode(&served.Manifest); err != nil {
			return nil, fmt.Errorf("malformed update manifest: %w", err)
		}
		return served, nil
	}
	reader := multipart.NewReader(body, mediaParams["boundary"])
	seenManifest := false
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("malformed multipart manifest response: %w", err)
		}
		_, dispositionParams, err := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		if err != nil {
			continue
		}
		switch dispositionParams["name"] {
		case "manifest":
			if err := json.NewDecoder(part).Decode(&served.Manifest); err != nil {
				return nil, fmt.Errorf("malformed update manifest: %w", err)
			}
			seenManifest = true
		case "extensions":
			var extensions struct {
				AssetRequestHeaders map[string]map[string]string `json:"assetRequestHeaders"`
			}
			if err := json.NewDecoder(part).Decode(&extensions); err != nil {
				return nil, fmt.Errorf("malformed update manifest extensions: %w", err)
			}
			served.AssetRequestHeaders = extensions.AssetRequestHeaders
		}
	}
	if !seenManifest {
		return nil, fmt.Errorf("the update manifest response carries no manifest part")
	}
	return served, nil
}

const maxHistoryAssetBytes = 256 << 20

// The largest updateGroups(limit:) the Expo API accepts.
const maxUpdateGroupsPage = 50

// Deliberately rides http.DefaultTransport so the test mocks intercept it.
var assetHTTPClient = &http.Client{Timeout: 5 * time.Minute}

func DownloadAsset(ctx context.Context, url string, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := assetHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not download %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("could not download %s: the CDN answered %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxHistoryAssetBytes+1))
	if err != nil {
		return nil, fmt.Errorf("could not download %s: %w", url, err)
	}
	if len(data) > maxHistoryAssetBytes {
		return nil, fmt.Errorf("could not download %s: asset larger than %d bytes", url, maxHistoryAssetBytes)
	}
	return data, nil
}

// FetchUpdateGroups returns the newest groups first, each holding one update
// per published platform.
func FetchUpdateGroups(ctx context.Context, auth types.Auth, expoAppId string, limit int) ([][]HistoryUpdate, error) {
	query := `
		query FetchUpdateGroups($appId: String!, $limit: Int!) {
			app {
				byId(appId: $appId) {
					id
					updateGroups(offset: 0, limit: $limit) {
						id
						group
						message
						createdAt
						platform
						manifestPermalink
						isRollBackToEmbedded
						gitCommitHash
						codeSigningInfo {
							keyid
						}
						runtime {
							id
							version
						}
						branch {
							id
							name
						}
					}
				}
			}
		}
	`
	if limit < 1 {
		limit = 1
	}
	if limit > maxUpdateGroupsPage {
		limit = maxUpdateGroupsPage
	}
	variables := map[string]interface{}{
		"appId": expoAppId,
		"limit": limit,
	}
	var resp struct {
		Errors graphQLErrors `json:"errors"`
		Data   struct {
			App struct {
				ById struct {
					Id           string `json:"id"`
					UpdateGroups [][]struct {
						Id                   string  `json:"id"`
						Group                string  `json:"group"`
						Message              *string `json:"message"`
						CreatedAt            string  `json:"createdAt"`
						Platform             string  `json:"platform"`
						ManifestPermalink    string  `json:"manifestPermalink"`
						IsRollBackToEmbedded bool    `json:"isRollBackToEmbedded"`
						GitCommitHash        *string `json:"gitCommitHash"`
						CodeSigningInfo      *struct {
							Keyid string `json:"keyid"`
						} `json:"codeSigningInfo"`
						Runtime struct {
							Version string `json:"version"`
						} `json:"runtime"`
						Branch struct {
							Name string `json:"name"`
						} `json:"branch"`
					} `json:"updateGroups"`
				} `json:"byId"`
			} `json:"app"`
		} `json:"data"`
	}
	headers := map[string]string{}
	if config.IsTestMode() {
		headers["operationName"] = "FetchExpoUpdateGroups"
	}
	if err := MakeGraphQLRequest(ctx, query, variables, auth, &resp, headers); err != nil {
		return nil, &APIError{StatusHint: http.StatusBadGateway, Message: fmt.Sprintf("could not reach the Expo API: %v", err)}
	}
	if apiErr := resp.Errors.toAPIError(); apiErr != nil {
		return nil, apiErr
	}
	if resp.Data.App.ById.Id == "" {
		return nil, &APIError{StatusHint: http.StatusNotFound, Message: "this Expo project does not exist or is not accessible with this access token"}
	}

	groups := make([][]HistoryUpdate, 0, len(resp.Data.App.ById.UpdateGroups))
	for _, group := range resp.Data.App.ById.UpdateGroups {
		updates := make([]HistoryUpdate, 0, len(group))
		for _, wire := range group {
			update := HistoryUpdate{
				Id:                wire.Id,
				Group:             wire.Group,
				BranchName:        wire.Branch.Name,
				RuntimeVersion:    wire.Runtime.Version,
				Platform:          wire.Platform,
				CreatedAt:         wire.CreatedAt,
				IsRollBack:        wire.IsRollBackToEmbedded,
				CodeSigned:        wire.CodeSigningInfo != nil,
				ManifestPermalink: wire.ManifestPermalink,
			}
			if wire.Message != nil {
				update.Message = *wire.Message
			}
			if wire.GitCommitHash != nil {
				update.GitCommitHash = *wire.GitCommitHash
			}
			updates = append(updates, update)
		}
		if len(updates) > 0 {
			groups = append(groups, updates)
		}
	}
	return groups, nil
}
