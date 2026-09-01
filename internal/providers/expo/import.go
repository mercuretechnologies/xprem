package expo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"xprem/config"
	"xprem/internal/types"
)

// APIError is a failed Expo API call; StatusHint is the HTTP status the
// dashboard should answer with.
type APIError struct {
	StatusHint int
	Message    string
}

func (e *APIError) Error() string { return e.Message }

type graphQLErrors []struct {
	Message string `json:"message"`
}

func (errs graphQLErrors) toAPIError() *APIError {
	if len(errs) == 0 {
		return nil
	}
	return &APIError{StatusHint: http.StatusBadRequest, Message: "the Expo API answered: " + errs[0].Message}
}

type ImportableApp struct {
	Id       string `json:"id"`
	Name     string `json:"name"`
	FullName string `json:"fullName"`
}

type AccountApps struct {
	AccountId   string          `json:"accountId"`
	AccountName string          `json:"accountName"`
	Apps        []ImportableApp `json:"apps"`
}

type ImportChannel struct {
	Name string
	// BranchName is nil when the channel is unmapped.
	BranchName *string
	// UnresolvedBranchID is a plain mapping whose branch was not in the
	// project's branch list; the channel is still imported, unmapped.
	UnresolvedBranchID string
}

type ProjectStructure struct {
	Name     string
	Branches []string
	Channels []ImportChannel
}

// The Expo API's own maximum for the apps field.
const accountAppsPageSize = 50

const accountAppsMaxPages = 20

// FetchAccountApps lists the apps of every account the token can act for.
func FetchAccountApps(ctx context.Context, auth types.Auth) ([]AccountApps, error) {
	query := `
		query FetchAccountApps($offset: Int!, $limit: Int!) {
			me {
				id
				accounts {
					id
					name
					apps(offset: $offset, limit: $limit) {
						id
						name
						fullName
					}
				}
			}
		}
	`
	headers := map[string]string{}
	if config.IsTestMode() {
		headers["operationName"] = "FetchExpoAccountApps"
	}

	accountsById := map[string]*AccountApps{}
	var ordered []*AccountApps
	for page := 0; page < accountAppsMaxPages; page++ {
		var resp struct {
			Errors graphQLErrors `json:"errors"`
			Data   struct {
				Me struct {
					Id       string `json:"id"`
					Accounts []struct {
						Id   string          `json:"id"`
						Name string          `json:"name"`
						Apps []ImportableApp `json:"apps"`
					} `json:"accounts"`
				} `json:"me"`
			} `json:"data"`
		}
		variables := map[string]interface{}{
			"offset": page * accountAppsPageSize,
			"limit":  accountAppsPageSize,
		}
		if err := MakeGraphQLRequest(ctx, query, variables, auth, &resp, headers); err != nil {
			return nil, &APIError{StatusHint: http.StatusBadGateway, Message: fmt.Sprintf("could not reach the Expo API: %v", err)}
		}
		if apiErr := resp.Errors.toAPIError(); apiErr != nil {
			return nil, apiErr
		}
		if resp.Data.Me.Id == "" {
			return nil, &APIError{StatusHint: http.StatusBadRequest, Message: "the Expo API did not recognize this access token"}
		}
		anyPageFull := false
		for _, account := range resp.Data.Me.Accounts {
			entry, ok := accountsById[account.Id]
			if !ok {
				// Apps starts non-nil so an app-less account serializes as [].
				entry = &AccountApps{AccountId: account.Id, AccountName: account.Name, Apps: []ImportableApp{}}
				accountsById[account.Id] = entry
				ordered = append(ordered, entry)
			}
			entry.Apps = append(entry.Apps, account.Apps...)
			if len(account.Apps) == accountAppsPageSize {
				anyPageFull = true
			}
		}
		if !anyPageFull {
			break
		}
	}

	accounts := make([]AccountApps, 0, len(ordered))
	for _, entry := range ordered {
		accounts = append(accounts, *entry)
	}
	return accounts, nil
}

func FetchProjectStructure(ctx context.Context, auth types.Auth, expoAppId string) (*ProjectStructure, error) {
	query := `
		query FetchProjectStructure($appId: String!) {
			app {
				byId(appId: $appId) {
					id
					name
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
	variables := map[string]interface{}{"appId": expoAppId}
	var resp struct {
		Errors graphQLErrors `json:"errors"`
		Data   struct {
			App struct {
				ById struct {
					Id             string `json:"id"`
					Name           string `json:"name"`
					UpdateBranches []struct {
						ID   string `json:"id"`
						Name string `json:"name"`
					} `json:"updateBranches"`
					UpdateChannels []struct {
						Name          string `json:"name"`
						BranchMapping string `json:"branchMapping"`
					} `json:"updateChannels"`
				} `json:"byId"`
			} `json:"app"`
		} `json:"data"`
	}
	headers := map[string]string{}
	if config.IsTestMode() {
		headers["operationName"] = "FetchExpoProjectStructure"
	}
	if err := MakeGraphQLRequest(ctx, query, variables, auth, &resp, headers); err != nil {
		return nil, &APIError{StatusHint: http.StatusBadGateway, Message: fmt.Sprintf("could not reach the Expo API: %v", err)}
	}
	if apiErr := resp.Errors.toAPIError(); apiErr != nil {
		return nil, apiErr
	}
	byId := resp.Data.App.ById
	if byId.Id == "" {
		return nil, &APIError{StatusHint: http.StatusNotFound, Message: "this Expo project does not exist or is not accessible with this access token"}
	}
	branchNameById := make(map[string]string, len(byId.UpdateBranches))
	structure := &ProjectStructure{Name: byId.Name}
	for _, branch := range byId.UpdateBranches {
		branchNameById[branch.ID] = branch.Name
		structure.Branches = append(structure.Branches, branch.Name)
	}
	for _, channel := range byId.UpdateChannels {
		imported := ImportChannel{Name: channel.Name}
		var mapping RawBranchMapping
		if err := json.Unmarshal([]byte(channel.BranchMapping), &mapping); err == nil {
			for _, m := range mapping.Data {
				var logic string
				// The logic is the JSON string "true" for a plain mapping and
				// an object for rollouts; only plain mappings are importable.
				if json.Unmarshal(m.BranchMappingLogic, &logic) == nil && logic == "true" {
					if name, ok := branchNameById[m.BranchId]; ok {
						branchName := name
						imported.BranchName = &branchName
					} else {
						imported.UnresolvedBranchID = m.BranchId
					}
					break
				}
			}
		}
		structure.Channels = append(structure.Channels, imported)
	}
	return structure, nil
}
