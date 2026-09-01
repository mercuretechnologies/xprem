package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"xprem/config"
	"xprem/internal/bucket"
	"xprem/internal/jobs"
	"xprem/internal/providers/expo"
	"xprem/internal/store"
	"xprem/internal/types"
	"xprem/internal/validation"

	"github.com/google/uuid"
)

// ExpoImportService creates a local app from an existing Expo project, under
// the same project UUID. The Expo credential is never stored.
type ExpoImportService struct {
	apps       *AppService
	branches   *BranchService
	channels   *ChannelService
	updateRepo UpdateRepository
	jobs       *jobs.Client
	bucket     bucket.Bucket
}

func NewExpoImportService(apps *AppService, branches *BranchService, channels *ChannelService, updateRepo UpdateRepository, jobsClient *jobs.Client, bucket bucket.Bucket) *ExpoImportService {
	return &ExpoImportService{
		apps:       apps,
		branches:   branches,
		channels:   channels,
		updateRepo: updateRepo,
		jobs:       jobsClient,
		bucket:     bucket,
	}
}

type ExpoImportResult struct {
	AppId        string   `json:"appId"`
	Name         string   `json:"name"`
	BranchCount  int      `json:"branchCount"`
	ChannelCount int      `json:"channelCount"`
	Skipped      []string `json:"skipped,omitempty"`
	Warnings     []string `json:"warnings,omitempty"`
	HistoryJobId string   `json:"historyJobId,omitempty"`
}

// ExpoImportPlan is the dry run shown before an import; ImportApp executes
// this exact plan.
type ExpoImportPlan struct {
	AppId string `json:"appId"`
	// Expo's name, or the UUID when that name fails validation.
	Name     string `json:"name"`
	ExpoName string `json:"expoName"`
	// Conflict is why the import cannot run at all; empty when it can.
	Conflict string               `json:"conflict,omitempty"`
	Branches []ExpoImportPlanItem `json:"branches"`
	Channels []ExpoImportPlanItem `json:"channels"`
}

// SkipReason set means the entry will not be created; Warning means created,
// with a caveat.
type ExpoImportPlanItem struct {
	Name         string `json:"name"`
	MappedBranch string `json:"mappedBranch,omitempty"`
	SkipReason   string `json:"skipReason,omitempty"`
	Warning      string `json:"warning,omitempty"`
}

func requireExpoAuth(auth types.Auth) error {
	if !expo.HasCredential(auth) {
		return validation.Errorf("accessToken", "provide an Expo access token")
	}
	return nil
}

func (s *ExpoImportService) ListImportableApps(ctx context.Context, auth types.Auth) ([]expo.AccountApps, error) {
	if !config.IsDBMode() {
		return nil, store.ErrNotSupportedInStatelessMode
	}
	if err := requireExpoAuth(auth); err != nil {
		return nil, err
	}
	return expo.FetchAccountApps(ctx, auth)
}

func (s *ExpoImportService) fetchImportStructure(ctx context.Context, auth types.Auth, expoAppId string) (uuid.UUID, *expo.ProjectStructure, error) {
	if !config.IsDBMode() {
		return uuid.UUID{}, nil, store.ErrNotSupportedInStatelessMode
	}
	if err := requireExpoAuth(auth); err != nil {
		return uuid.UUID{}, nil, err
	}
	parsedId, err := uuid.Parse(strings.TrimSpace(expoAppId))
	if err != nil {
		return uuid.UUID{}, nil, validation.Errorf("expoAppId", "must be the Expo project UUID (app.json, extra.eas.projectId)")
	}
	structure, err := expo.FetchProjectStructure(ctx, auth, parsedId.String())
	if err != nil {
		return uuid.UUID{}, nil, err
	}
	return parsedId, structure, nil
}

// buildImportPlan applies the same name rules CreateBranch and CreateChannel
// enforce, so the plan never promises an entry the import would refuse.
func buildImportPlan(appId uuid.UUID, structure *expo.ProjectStructure) *ExpoImportPlan {
	plan := &ExpoImportPlan{
		AppId:    appId.String(),
		Name:     structure.Name,
		ExpoName: structure.Name,
		Branches: []ExpoImportPlanItem{},
		Channels: []ExpoImportPlanItem{},
	}
	if validation.DisplayName("name", structure.Name) != nil {
		plan.Name = appId.String()
	}
	keptBranches := make(map[string]bool, len(structure.Branches))
	for _, branchName := range structure.Branches {
		item := ExpoImportPlanItem{Name: branchName}
		if err := validation.Name("branchName", branchName); err != nil {
			item.SkipReason = validationMessage(err)
		} else {
			keptBranches[branchName] = true
		}
		plan.Branches = append(plan.Branches, item)
	}
	for _, channel := range structure.Channels {
		item := ExpoImportPlanItem{Name: channel.Name}
		if channel.BranchName != nil {
			item.MappedBranch = *channel.BranchName
		}
		if err := validation.Name("channelName", channel.Name); err != nil {
			item.SkipReason = validationMessage(err)
		} else if item.MappedBranch != "" && !keptBranches[item.MappedBranch] {
			item.Warning = fmt.Sprintf("left unmapped, its branch %q is not imported", item.MappedBranch)
			item.MappedBranch = ""
		} else if channel.UnresolvedBranchID != "" {
			item.Warning = "left unmapped, its Expo branch is not in the project listing"
		}
		plan.Channels = append(plan.Channels, item)
	}
	return plan
}

func validationMessage(err error) string {
	var valErr *validation.Error
	if errors.As(err, &valErr) {
		return valErr.Message
	}
	return err.Error()
}

func (s *ExpoImportService) PreviewImport(ctx context.Context, auth types.Auth, expoAppId string) (*ExpoImportPlan, error) {
	parsedId, structure, err := s.fetchImportStructure(ctx, auth, expoAppId)
	if err != nil {
		return nil, err
	}
	plan := buildImportPlan(parsedId, structure)
	if _, err := s.apps.GetAppByID(ctx, plan.AppId); err == nil {
		plan.Conflict = "an app with this UUID already exists here; delete it first to import again"
	}
	return plan, nil
}

// ImportApp executes the plan PreviewImport shows. Any failure past app
// creation, the job not starting included, rolls the app back.
func (s *ExpoImportService) ImportApp(ctx context.Context, auth types.Auth, expoAppId string, keysConfig config.KeysConfig, historyLimit int) (*ExpoImportResult, error) {
	if historyLimit < 0 || historyLimit > MaxHistoryImportGroups {
		return nil, validation.Errorf("historyLimit", "must be between 0 and %d", MaxHistoryImportGroups)
	}
	parsedId, structure, err := s.fetchImportStructure(ctx, auth, expoAppId)
	if err != nil {
		return nil, err
	}
	plan := buildImportPlan(parsedId, structure)

	appId, err := s.apps.CreateAppWithId(ctx, parsedId, plan.Name, keysConfig)
	if err != nil {
		return nil, err
	}

	result := &ExpoImportResult{AppId: appId, Name: plan.Name}
	for _, branch := range plan.Branches {
		if branch.SkipReason != "" {
			result.Skipped = append(result.Skipped, fmt.Sprintf("branch %q: %s", branch.Name, branch.SkipReason))
			continue
		}
		if _, err := s.branches.CreateBranch(ctx, appId, branch.Name); err != nil {
			s.rollback(ctx, appId, plan.Name)
			return nil, fmt.Errorf("failed to import branch %q: %w", branch.Name, err)
		}
		result.BranchCount++
	}
	for _, channel := range plan.Channels {
		if channel.SkipReason != "" {
			result.Skipped = append(result.Skipped, fmt.Sprintf("channel %q: %s", channel.Name, channel.SkipReason))
			continue
		}
		if channel.Warning != "" {
			result.Warnings = append(result.Warnings, fmt.Sprintf("channel %q: %s", channel.Name, channel.Warning))
		}
		var branchName *string
		if channel.MappedBranch != "" {
			branchName = &channel.MappedBranch
		}
		if _, err := s.channels.CreateChannel(ctx, appId, branchName, channel.Name); err != nil {
			s.rollback(ctx, appId, plan.Name)
			return nil, fmt.Errorf("failed to import channel %q: %w", channel.Name, err)
		}
		result.ChannelCount++
	}

	if historyLimit > 0 {
		jobId, err := s.StartHistoryImport(ctx, auth, appId, historyLimit)
		if err != nil {
			s.rollback(ctx, appId, plan.Name)
			return nil, fmt.Errorf("failed to start the update history import: %w", err)
		}
		result.HistoryJobId = jobId
	}
	return result, nil
}

// App deletion cascades to branches and channels.
func (s *ExpoImportService) rollback(ctx context.Context, appId string, name string) {
	if err := s.apps.DeleteApp(ctx, config.AppConfig{Id: appId, Name: name}); err != nil {
		log.Printf("[expo-import] failed to roll back app %s after a failed import: %v", appId, err)
	}
}
