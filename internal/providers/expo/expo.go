package expo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"xprem/config"
	cache2 "xprem/internal/cache"
	"xprem/internal/types"
)

// The "operationName" header values below are a contract with the test mocks
// (test/helpers.go and friends match on them), not a reflection of the Go
// function names, they intentionally kept their original "FetchExpo…" spelling
// when this package was split out of providers and the prefix was dropped.
// Renaming one to match its function silently breaks the mock that matches it.

type UserAccount struct {
	Id       string `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
}



type BranchMapping struct {
	BranchName  string  `json:"branchName"`
	BranchId    string  `json:"branchId"`
	ChannelName *string `json:"channelName"`
}

type Channel struct {
	Id       string `json:"id"`
	Name     string `json:"name"`
	BranchId string `json:"branchId"`
}

// RawBranchMapping is the wire shape of a channel's branchMapping field as
// returned by the EAS API, before it is resolved into a BranchMapping.
type RawBranchMapping struct {
	Version int `json:"version"`
	Data    []struct {
		BranchId           string          `json:"branchId"`
		BranchMappingLogic json.RawMessage `json:"branchMappingLogic"`
	} `json:"data"`
}

func ValidateAuth(appId string, expoAuth types.Auth) (*UserAccount, error) {
	if expoAuth.Token == nil && expoAuth.SessionSecret == nil {
		return nil, errors.New("no valid Expo auth provided")
	}
	expoAccount, err := FetchUserAccountInformations(expoAuth)
	if err != nil {
		return nil, err
	}
	if expoAccount == nil {
		return nil, errors.New("no expo account found")
	}
	selfExpoUsername := FetchSelfUsername(appId)
	if selfExpoUsername != expoAccount.Username {
		return nil, errors.New("expo account does not match self expo username")
	}
	return expoAccount, nil
}

// GetAccessToken returns the Expo access token configured for the given
// app in the v2 apps config. Returns "" if the app is unknown so callers
// that treat it as "missing token" produce the same auth-failure path.
func GetAccessToken(appId string) string {
	app, err := config.GetAppConfig(appId)
	if err != nil {
		return ""
	}
	return app.AccessToken
}

func SetAuthHeaders(expoAuth types.Auth, req *http.Request) {
	if expoAuth.Token != nil {
		req.Header.Set("Authorization", "Bearer "+*expoAuth.Token)
	}
	if expoAuth.SessionSecret != nil {
		req.Header.Set("expo-session", *expoAuth.SessionSecret)
	}
}

func MakeGraphQLRequest(ctx context.Context, query string, variables map[string]interface{}, expoAuth types.Auth, result interface{}, headers map[string]string) error {
	requestBody := map[string]interface{}{
		"query":     query,
		"variables": variables,
	}

	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.expo.dev/graphql", bytes.NewBuffer(bodyBytes))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	SetAuthHeaders(expoAuth, req)
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Read error message in response body
		responseBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return errors.New("GraphQL request failed with status: " + resp.Status + " and unable to read response body")
		}
		return errors.New("GraphQL request failed with status: " + resp.Status + " message: " + string(responseBody))
	}

	return json.NewDecoder(resp.Body).Decode(result)
}

func FetchChannels(appId string) ([]Channel, error) {
	query := `
		query FetchAppChannel($appId: String!) {
			app {
				byId(appId: $appId) {
					id
					updateChannels(offset: 0, limit: 10000) {
						id
						name
					}
				}
			}
		}
	`
	expoToken := GetAccessToken(appId)
	variables := map[string]interface{}{
		"appId": appId,
	}
	var resp struct {
		Data struct {
			App struct {
				ById struct {
					UpdateChannels []Channel `json:"updateChannels"`
				} `json:"byId"`
			} `json:"app"`
		} `json:"data"`
	}
	headers := map[string]string{}
	if config.IsTestMode() {
		headers["operationName"] = "FetchExpoChannels"
	}
	ctx := context.Background()
	if err := MakeGraphQLRequest(ctx, query, variables, types.Auth{
		Token: &expoToken,
	}, &resp, headers); err != nil {
		return nil, err
	}
	return resp.Data.App.ById.UpdateChannels, nil
}

func FetchBranches(appId string) ([]string, error) {
	query := `
		query FetchAppChannel($appId: String!) {
			app {
				byId(appId: $appId) {
					id
					updateBranches(offset: 0, limit: 10000) {
						id
						name
					}
				}
			}
		}
	`
	expoToken := GetAccessToken(appId)
	variables := map[string]interface{}{
		"appId": appId,
	}
	var resp struct {
		Data struct {
			App struct {
				ById struct {
					UpdateBranches []struct {
						ID   string `json:"id"`
						Name string `json:"name"`
					} `json:"updateBranches"`
				} `json:"byId"`
			} `json:"app"`
		} `json:"data"`
	}
	headers := map[string]string{}
	if config.IsTestMode() {
		headers["operationName"] = "FetchExpoBranches"
	}
	ctx := context.Background()
	if err := MakeGraphQLRequest(ctx, query, variables, types.Auth{
		Token: &expoToken,
	}, &resp, headers); err != nil {
		return nil, err
	}
	var branches []string
	for _, branch := range resp.Data.App.ById.UpdateBranches {
		branches = append(branches, branch.Name)
	}
	return branches, nil
}

func FetchUserAccountInformations(expoAuth types.Auth) (*UserAccount, error) {
	accountCache := cache2.GetCache()
	var cacheKey string
	if expoAuth.Token != nil {
		cacheKey = userAccountTokenCacheKey(*expoAuth.Token)
	} else if expoAuth.SessionSecret != nil {
		cacheKey = userAccountSessionCacheKey(*expoAuth.SessionSecret)
	}
	if cacheKey != "" {
		if account, ok := cache2.GetJSON[UserAccount](accountCache, cacheKey); ok {
			return &account, nil
		}
	}

	query := `
		query GetCurrentUserAccount {
			me {
				id
				username
				email
			}
		}
	`

	var resp struct {
		Data struct {
			Me UserAccount `json:"me"`
		} `json:"data"`
	}

	headers := map[string]string{}
	if config.IsTestMode() {
		headers["operationName"] = "FetchExpoUserAccountInformations"
	}

	ctx := context.Background()
	if err := MakeGraphQLRequest(ctx, query, nil, expoAuth, &resp, headers); err != nil {
		return nil, err
	}

	if cacheKey != "" {
		ttl := userAccountCacheTTLSeconds
		cache2.SetJSON(accountCache, cacheKey, resp.Data.Me, &ttl)
	}

	return &resp.Data.Me, nil
}

func FetchSelfUsername(appId string) string {
	usernameCache := cache2.GetCache()
	token := GetAccessToken(appId)
	cacheKey := selfUsernameCacheKey(token)
	if cachedValue := usernameCache.Get(cacheKey); cachedValue != "" {
		return cachedValue
	}
	expoAccount, err := FetchUserAccountInformations(types.Auth{
		Token: &token,
	})
	if err != nil {
		return ""
	}
	ttl := selfUsernameCacheTTLSeconds
	_ = usernameCache.Set(cacheKey, expoAccount.Username, &ttl)
	return expoAccount.Username
}

// unknownAppName negative-caches a failed or empty name lookup so callers do
// not repeat the GraphQL round-trip on every call while Expo is unreachable.
const unknownAppName = "\x00unknown"

// FetchAppName returns the app's display name as configured on Expo, or ""
// when it cannot be resolved (missing/invalid token, network failure). Used
// as a dashboard display fallback in stateless mode, where the flat env
// carries no name, so best-effort by design: callers fall back to the id.
func FetchAppName(ctx context.Context, appId string) string {
	nameCache := cache2.GetCache()
	token := GetAccessToken(appId)
	cacheKey := appNameCacheKey(appId, token)
	if cachedValue := nameCache.Get(cacheKey); cachedValue != "" {
		if cachedValue == unknownAppName {
			return ""
		}
		return cachedValue
	}
	query := `
		query FetchAppName($appId: String!) {
			app {
				byId(appId: $appId) {
					id
					name
				}
			}
		}
	`
	variables := map[string]interface{}{
		"appId": appId,
	}
	var resp struct {
		Data struct {
			App struct {
				ById App `json:"byId"`
			} `json:"app"`
		} `json:"data"`
	}
	headers := map[string]string{}
	if config.IsTestMode() {
		headers["operationName"] = "FetchExpoAppName"
	}
	if err := MakeGraphQLRequest(ctx, query, variables, types.Auth{
		Token: &token,
	}, &resp, headers); err != nil {
		log.Printf("[Expo] could not resolve app name for %s, falling back to the app id: %v", appId, err)
		ttl := unknownAppNameCacheTTLSeconds
		_ = nameCache.Set(cacheKey, unknownAppName, &ttl)
		return ""
	}
	name := resp.Data.App.ById.Name
	if name != "" {
		ttl := appNameCacheTTLSeconds
		_ = nameCache.Set(cacheKey, name, &ttl)
	} else {
		ttl := unknownAppNameCacheTTLSeconds
		_ = nameCache.Set(cacheKey, unknownAppName, &ttl)
	}
	return name
}

func FetchChannelMapping(appId, channelName string) (*types.ChannelResolution, error) {
	mappingCache := cache2.GetCache()
	cacheKey := channelMappingCacheKey(appId, channelName)
	if mapping, ok := cache2.GetJSON[types.ChannelResolution](mappingCache, cacheKey); ok {
		return &mapping, nil
	}

	query := `
		query FetchAppChannel($appId: String!, $channelName: String!) {
			app {
				byId(appId: $appId) {
					id
					updateBranches(offset: 0, limit: 10000) {
						id
						name
					}
					updateChannelByName(name: $channelName) {
						id
						name
						branchMapping
					}
				}
			}
		}
	`

	expoToken := GetAccessToken(appId)
	variables := map[string]interface{}{
		"appId":       appId,
		"channelName": channelName,
	}

	var resp struct {
		Data struct {
			App struct {
				ById struct {
					UpdateBranches []struct {
						ID   string `json:"id"`
						Name string `json:"name"`
					} `json:"updateBranches"`
					UpdateChannelByName struct {
						ID            string `json:"id"`
						BranchMapping string `json:"branchMapping"`
					} `json:"updateChannelByName"`
				} `json:"byId"`
			} `json:"app"`
		} `json:"data"`
	}

	headers := map[string]string{}
	if config.IsTestMode() {
		headers["operationName"] = "FetchExpoChannelMapping"
	}
	ctx := context.Background()
	if err := MakeGraphQLRequest(ctx, query, variables, types.Auth{Token: &expoToken}, &resp, headers); err != nil {
		return nil, err
	}

	var branchMapping RawBranchMapping
	if err := json.Unmarshal([]byte(resp.Data.App.ById.UpdateChannelByName.BranchMapping), &branchMapping); err != nil {
		return nil, err
	}

	var branchID string
	for _, mapping := range branchMapping.Data {
		var logic string
		if json.Unmarshal(mapping.BranchMappingLogic, &logic) == nil && logic == "true" {
			branchID = mapping.BranchId
			break
		}
	}
	if branchID == "" {
		return nil, nil
	}

	var branchName string
	for _, branch := range resp.Data.App.ById.UpdateBranches {
		if branch.ID == branchID {
			branchName = branch.Name
			break
		}
	}
	if branchName == "" {
		return nil, nil
	}

	result := &types.ChannelResolution{
		Id:         resp.Data.App.ById.UpdateChannelByName.ID,
		BranchName: branchName,
	}
	ttl := channelMappingCacheTTLSeconds
	cache2.SetJSON(mappingCache, cacheKey, result, &ttl)
	return result, nil
}

func FetchBranchesMapping(appId string) ([]BranchMapping, error) {
	query := `
		query FetchAppChannel($appId: String!) {
			app {
				byId(appId: $appId) {
					id
					updateBranches(offset: 0, limit: 10000) {
						id
						name
					}
					updateChannels(offset: 0, limit: 10000) {
						id
						name
						branchMapping
					}
				}
			}
		}
	`

	expoToken := GetAccessToken(appId)
	variables := map[string]interface{}{"appId": appId}

	headers := map[string]string{}
	if config.IsTestMode() {
		headers["operationName"] = "FetchExpoBranches"
	}

	var resp struct {
		Data struct {
			App struct {
				ById struct {
					UpdateBranches []struct {
						ID   string `json:"id"`
						Name string `json:"name"`
					} `json:"updateBranches"`
					UpdateChannels []struct {
						ID            string `json:"id"`
						Name          string `json:"name"`
						BranchMapping string `json:"branchMapping"`
					} `json:"updateChannels"`
				} `json:"byId"`
			} `json:"app"`
		} `json:"data"`
	}

	ctx := context.Background()
	if err := MakeGraphQLRequest(ctx, query, variables, types.Auth{
		Token: &expoToken,
	}, &resp, headers); err != nil {
		return nil, err
	}

	branchIDToChannels := make(map[string][]string)
	for _, channel := range resp.Data.App.ById.UpdateChannels {
		var mapping RawBranchMapping
		if err := json.Unmarshal([]byte(channel.BranchMapping), &mapping); err != nil {
			return nil, err
		}

		for _, m := range mapping.Data {
			var logic string
			if json.Unmarshal(m.BranchMappingLogic, &logic) == nil && logic == "true" {
				branchIDToChannels[m.BranchId] = append(branchIDToChannels[m.BranchId], channel.Name)
			}
		}
	}

	var branchMappings []BranchMapping
	for _, branch := range resp.Data.App.ById.UpdateBranches {
		channelNames, found := branchIDToChannels[branch.ID]
		if !found || len(channelNames) == 0 {
			branchMappings = append(branchMappings, BranchMapping{
				BranchName:  branch.Name,
				BranchId:    branch.ID,
				ChannelName: nil,
			})
			continue
		}

		for _, channelName := range channelNames {
			cn := channelName
			branchMappings = append(branchMappings, BranchMapping{
				BranchName:  branch.Name,
				BranchId:    branch.ID,
				ChannelName: &cn,
			})
		}
	}

	return branchMappings, nil
}

func CreateBranch(appId, branch string) error {
	query := `
		mutation CreateUpdateBranchForAppMutation($appId: ID!, $name: String!) {
		  updateBranch {
			createUpdateBranchForApp(appId: $appId, name: $name) {
			  id
			}
		  }
		}
	`
	variables := map[string]interface{}{
		"appId": appId,
		"name":  branch,
	}
	token := GetAccessToken(appId)
	headers := map[string]string{}
	if config.IsTestMode() {
		headers["operationName"] = "CreateBranch"
	}
	ctx := context.Background()
	resp := struct{}{}
	return MakeGraphQLRequest(ctx, query, variables, types.Auth{
		Token: &token,
	}, &resp, headers)
}

type App struct {
	Id   string `json:"id"`
	Name string `json:"name"`
}
