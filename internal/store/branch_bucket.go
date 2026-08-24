package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"
	"xprem/config"
	"xprem/internal/branch"
	bucket2 "xprem/internal/bucket"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/providers/expo"
	"xprem/internal/types"
)

type BucketBranchStore struct {
	bucket bucket2.Bucket
}

func NewBucketBranchStore(bucket bucket2.Bucket) *BucketBranchStore {
	return &BucketBranchStore{
		bucket: bucket,
	}
}

func (s *BucketBranchStore) InsertBranch(ctx context.Context, branch pgdb.InsertBranchParams) (int64, error) {
	return 0, fmt.Errorf("branch creation is only supported in db mode")
}

func (s *BucketBranchStore) GetUpdatedMetadataByBranchName(ctx context.Context, appId string, branchName string) ([]pgdb.GetUpdatesMetadataByBranchNameRow, error) {
	return nil, fmt.Errorf("getting updated metadata by branch name is only supported in db mode")
}

func (s *BucketBranchStore) DeleteBranchByName(ctx context.Context, appId string, branchName string) error {
	return fmt.Errorf("branch deletion is only supported in db mode")
}

func (s *BucketBranchStore) GetSurfableBranches(ctx context.Context, appId string, runtimeVersion string, platform types.Platform) ([]types.SurfableBranch, error) {
	return nil, ErrNotSupportedInStatelessMode
}

func (s *BucketBranchStore) GetBranches(ctx context.Context, appId string) ([]types.BranchMapping, error) {
	allBranches, err := s.bucket.GetBranches(appId)
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

func (s *BucketBranchStore) GetRuntimeVersionsWithUpdateStats(ctx context.Context, appId string, branchName string) ([]types.RuntimeVersionWithStats, error) {
	runtimeVersions, err := s.bucket.GetRuntimeVersions(appId, branchName)
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

func (s *BucketBranchStore) UpdateChannelBranchMapping(ctx context.Context, appId string, channelId string, branchId string) error {
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

func (s *BucketBranchStore) UpsertBranchAndRuntimeVersion(ctx context.Context, appId string, branchName string, runtimeVersion string) error {
	// No need to upsert runtime version since it's related to the branch
	return branch.UpsertBranch(appId, branchName)
}

func (s *BucketBranchStore) CreateRuntimeVersion(ctx context.Context, appId string, version string) (int64, error) {
	return 0, ErrNotSupportedInStatelessMode
}

func (s *BucketBranchStore) GetBranchByName(ctx context.Context, appId string, branchName string) (int64, error) {
	return 0, ErrNotSupportedInStatelessMode
}
