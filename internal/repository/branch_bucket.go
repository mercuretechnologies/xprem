package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"
	"xprem/config"
	"xprem/internal/branch"
	"xprem/internal/bucket"
	"xprem/internal/providers/expo"
	"xprem/internal/types"
)

type BucketBranchRepository struct {
	updateStore *bucket.UpdateStore
}

func NewBucketBranchRepository(updateStore *bucket.UpdateStore) *BucketBranchRepository {
	return &BucketBranchRepository{
		updateStore: updateStore,
	}
}

func (s *BucketBranchRepository) InsertBranch(ctx context.Context, appId string, branchName string) (int64, error) {
	return 0, fmt.Errorf("branch creation is only supported in db mode")
}

func (s *BucketBranchRepository) GetUpdateRefsByBranchName(ctx context.Context, appId string, branchName string) ([]types.UpdateRef, error) {
	return nil, fmt.Errorf("getting update refs by branch name is only supported in db mode")
}

func (s *BucketBranchRepository) DeleteBranchByName(ctx context.Context, appId string, branchName string) error {
	return fmt.Errorf("branch deletion is only supported in db mode")
}

func (s *BucketBranchRepository) GetSurfableBranches(ctx context.Context, appId string, runtimeVersion string, platform types.Platform) ([]types.SurfableBranch, error) {
	return nil, ErrNotSupportedInStatelessMode
}

func (s *BucketBranchRepository) GetBranches(ctx context.Context, appId string) ([]types.BranchMapping, error) {
	allBranches, err := s.updateStore.Branches(ctx, appId)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch expo branches: %w", err)
	}
	branchesMapping, err := expo.FetchBranchesMapping(appId)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch expo branches mapping: %w", err)
	}
	var branches []types.BranchMapping
	for _, branch := range allBranches {
		var releaseChannel *string
		var branchId *string
		for _, mapping := range branchesMapping {
			if mapping.BranchName == branch {
				releaseChannel = mapping.ChannelName
				branchId = &mapping.BranchId
				break
			}
		}
		branches = append(branches, types.BranchMapping{
			BranchName:     branch,
			BranchId:       branchId,
			ReleaseChannel: releaseChannel,
		})
	}
	return branches, nil
}

func (s *BucketBranchRepository) GetRuntimeVersionsWithUpdateStats(ctx context.Context, appId string, branchName string) ([]types.RuntimeVersionWithStats, error) {
	runtimeVersions, err := s.updateStore.RuntimeVersions(ctx, appId, branchName)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch runtime versions with update stats: %w", err)
	}
	sort.Slice(runtimeVersions, func(i, j int) bool {
		timeI, _ := time.Parse(time.RFC3339, runtimeVersions[i].CreatedAt)
		timeJ, _ := time.Parse(time.RFC3339, runtimeVersions[j].CreatedAt)
		return timeI.After(timeJ)
	})
	return runtimeVersions, nil
}

func (s *BucketBranchRepository) UpdateChannelBranchMapping(ctx context.Context, appId string, channelId string, branchId string) error {
	fmt.Println("Updating channel branch mapping for channel:", channelId, "to branch:", branchId)
	query := `
		mutation UpdateChannelBranchMapping($channelId: ID!, $branchMapping: String!) {
			updateChannel {
				editUpdateChannel(channelId: $channelId, branchMapping: $branchMapping) {
					id
				}
			}
		}
	`
	branchMapping := expo.RawBranchMapping{
		Version: 0,
		Data: []struct {
			BranchId           string          `json:"branchId"`
			BranchMappingLogic json.RawMessage `json:"branchMappingLogic"`
		}{
			{
				BranchId:           branchId,
				BranchMappingLogic: json.RawMessage(`"true"`),
			},
		},
	}

	branchMappingBytes, err := json.Marshal(branchMapping)
	if err != nil {
		return err
	}

	variables := map[string]interface{}{
		"channelId":     channelId,
		"branchMapping": string(branchMappingBytes),
	}

	token := expo.GetAccessToken(appId)
	headers := map[string]string{}
	if config.IsTestMode() {
		headers["operationName"] = "UpdateChannelBranchMapping"
	}
	resp := struct{}{}
	return expo.MakeGraphQLRequest(ctx, query, variables, types.Auth{
		Token: &token,
	}, &resp, headers)
}

func (s *BucketBranchRepository) UpsertBranchAndRuntimeVersion(ctx context.Context, appId string, branchName string, runtimeVersion string) error {
	// No need to upsert runtime version since it's related to the branch
	return branch.UpsertBranch(appId, branchName)
}

func (s *BucketBranchRepository) CreateRuntimeVersion(ctx context.Context, appId string, version string) (int64, error) {
	return 0, ErrNotSupportedInStatelessMode
}

func (s *BucketBranchRepository) GetBranchByName(ctx context.Context, appId string, branchName string) (int64, error) {
	return 0, ErrNotSupportedInStatelessMode
}
