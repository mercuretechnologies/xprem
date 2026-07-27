package services

import (
	"context"
	"errors"
	"expo-open-ota/internal/auditlog"
	"expo-open-ota/internal/store"
	"expo-open-ota/internal/types"
	"expo-open-ota/internal/validation"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

// ErrRolloutsRequireControlPlane refuses every rollout operation in stateless mode,
// where the rollout repository stays nil. Handlers map it to a 400.
var ErrRolloutsRequireControlPlane = errors.New("progressive rollouts require the database control plane")

// RolloutRequestError rejects a rollout mutation with a client-facing message and the
// HTTP status the handler should surface (400 invalid request, 404 nothing active,
// 409 concurrent modification).
type RolloutRequestError struct {
	Status  int
	Message string
}

func (e *RolloutRequestError) Error() string { return e.Message }

type RolloutRepository interface {
	CreateChannelRollout(ctx context.Context, id string, appId string, channelName string, rolloutBranchName string, percentage int) (int64, error)
	GetChannelRollout(ctx context.Context, appId string, channelName string) (*types.ChannelRollout, error)
	UpdateChannelRolloutPercentage(ctx context.Context, appId string, channelName string, percentage int) (int64, error)
	DeleteChannelRollout(ctx context.Context, appId string, channelName string) (int64, error)
	PromoteChannelRollout(ctx context.Context, appId string, channelName string) (int64, error)
	GetChannelRolloutsByBranch(ctx context.Context, appId string, branchName string) ([]string, error)
	GetActiveRolloutUpdates(ctx context.Context, appId string, branchName string, runtimeVersion string) ([]types.RolloutUpdate, error)
	SetUpdateRolloutPercentage(ctx context.Context, appId string, branchName string, runtimeVersion string, percentage int) (int64, error)
	ClearUpdateRollout(ctx context.Context, appId string, branchName string, runtimeVersion string) (int64, error)
}

// RolloutService drives the dashboard-facing rollout lifecycle for both mechanisms:
// channel rollouts (start / edit percentage / promote / revert) and per-update
// rollouts (progress / finish / revert). It shares the DeploymentService so per-update
// revert can reuse the guard-free republish and rollback internals.
type RolloutService struct {
	rolloutRepo       RolloutRepository
	channelRepo       ChannelRepository
	updateRepo        UpdateRepository
	deploymentService *DeploymentService
	// onAuditEvent is the audit emission seam; nil (community) means rollout
	// changes leave no events.
	onAuditEvent auditlog.RecordFunc
}

// SetOnAuditEvent plugs the audit emission seam (see SetSSOEnforced for the
// pattern). Nil-safe.
func (s *RolloutService) SetOnAuditEvent(record auditlog.RecordFunc) {
	s.onAuditEvent = record
}

func NewRolloutService(rolloutRepo RolloutRepository, channelRepo ChannelRepository, updateRepo UpdateRepository, deploymentService *DeploymentService) *RolloutService {
	return &RolloutService{
		rolloutRepo:       rolloutRepo,
		channelRepo:       channelRepo,
		updateRepo:        updateRepo,
		deploymentService: deploymentService,
	}
}

func (s *RolloutService) StartChannelRollout(ctx context.Context, appId string, channelName string, branchName string, percentage int) (*types.ChannelRollout, error) {
	if s.rolloutRepo == nil {
		return nil, ErrRolloutsRequireControlPlane
	}
	if err := validation.Name("channelName", channelName); err != nil {
		return nil, err
	}
	if err := validation.Name("branchName", branchName); err != nil {
		return nil, err
	}
	if percentage < 1 || percentage > 99 {
		return nil, &RolloutRequestError{Status: http.StatusBadRequest, Message: "the rollout percentage must be between 1 and 99"}
	}
	// The rollout row's UUID doubles as the bucketing salt, fixed for its life.
	rolloutId := uuid.New().String()
	rows, err := s.rolloutRepo.CreateChannelRollout(ctx, rolloutId, appId, channelName, branchName, percentage)
	if err != nil {
		return nil, err
	}
	if rows == 0 {
		return nil, s.disambiguateStartRefusal(ctx, appId, channelName, branchName)
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionChannelRolloutStarted,
		TargetType:    "channel",
		TargetID:      channelName,
		TargetDisplay: channelName,
		AppID:         appId,
		Metadata:      map[string]any{"branch": branchName, "percentage": percentage},
	})
	return s.rolloutRepo.GetChannelRollout(ctx, appId, channelName)
}

// disambiguateStartRefusal turns the 0-rows result of the guarded channel rollout
// INSERT into a precise client error: unknown channel, unmapped channel, rollout
// branch equal to the default, or unknown rollout branch.
func (s *RolloutService) disambiguateStartRefusal(ctx context.Context, appId string, channelName string, branchName string) error {
	channels, err := s.channelRepo.GetChannels(ctx, appId)
	if err != nil {
		return fmt.Errorf("failed to disambiguate rollout start refusal: %w", err)
	}
	for _, channel := range channels {
		if channel.ReleaseChannelName != channelName {
			continue
		}
		if channel.BranchName == nil {
			return &RolloutRequestError{Status: http.StatusBadRequest, Message: fmt.Sprintf("channel %q has no branch mapped; map a default branch before starting a rollout", channelName)}
		}
		if *channel.BranchName == branchName {
			return &RolloutRequestError{Status: http.StatusBadRequest, Message: fmt.Sprintf("the rollout branch must differ from the channel's current branch %q", branchName)}
		}
		return &store.ErrResourceNotFound{Resource: "branch", Identifier: fmt.Sprintf("%s (appId: %s)", branchName, appId)}
	}
	return &store.ErrResourceNotFound{Resource: "channel", Identifier: fmt.Sprintf("%s (appId: %s)", channelName, appId)}
}

func (s *RolloutService) UpdateChannelRolloutPercentage(ctx context.Context, appId string, channelName string, percentage int) (*types.ChannelRollout, error) {
	if s.rolloutRepo == nil {
		return nil, ErrRolloutsRequireControlPlane
	}
	if err := validation.Name("channelName", channelName); err != nil {
		return nil, err
	}
	if percentage == 100 {
		return nil, &RolloutRequestError{Status: http.StatusBadRequest, Message: "a channel rollout cannot be set to 100%; promote it to end it"}
	}
	if percentage < 1 || percentage > 99 {
		return nil, &RolloutRequestError{Status: http.StatusBadRequest, Message: "the rollout percentage must be between 1 and 99"}
	}
	rows, err := s.rolloutRepo.UpdateChannelRolloutPercentage(ctx, appId, channelName, percentage)
	if err != nil {
		return nil, err
	}
	if rows == 0 {
		return nil, &RolloutRequestError{Status: http.StatusNotFound, Message: fmt.Sprintf("channel %q has no active rollout", channelName)}
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionChannelRolloutUpdated,
		TargetType:    "channel",
		TargetID:      channelName,
		TargetDisplay: channelName,
		AppID:         appId,
		Metadata:      map[string]any{"percentage": percentage},
	})
	return s.rolloutRepo.GetChannelRollout(ctx, appId, channelName)
}

const (
	ChannelRolloutOutcomePromote = "promote"
	ChannelRolloutOutcomeRevert  = "revert"
)

func (s *RolloutService) EndChannelRollout(ctx context.Context, appId string, channelName string, outcome string) error {
	if s.rolloutRepo == nil {
		return ErrRolloutsRequireControlPlane
	}
	if err := validation.Name("channelName", channelName); err != nil {
		return err
	}
	var rows int64
	var err error
	switch outcome {
	case ChannelRolloutOutcomePromote:
		rows, err = s.rolloutRepo.PromoteChannelRollout(ctx, appId, channelName)
	case ChannelRolloutOutcomeRevert:
		rows, err = s.rolloutRepo.DeleteChannelRollout(ctx, appId, channelName)
	default:
		return &RolloutRequestError{Status: http.StatusBadRequest, Message: `outcome must be "promote" or "revert"`}
	}
	if err != nil {
		return err
	}
	if rows == 0 {
		return &RolloutRequestError{Status: http.StatusNotFound, Message: fmt.Sprintf("channel %q has no active rollout", channelName)}
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionChannelRolloutEnded,
		TargetType:    "channel",
		TargetID:      channelName,
		TargetDisplay: channelName,
		AppID:         appId,
		Metadata:      map[string]any{"result": outcome},
	})
	return nil
}

func (s *RolloutService) GetUpdateRollout(ctx context.Context, appId string, branchName string, runtimeVersion string) ([]types.RolloutUpdate, error) {
	if s.rolloutRepo == nil {
		return nil, ErrRolloutsRequireControlPlane
	}
	if err := validation.Name("branchName", branchName); err != nil {
		return nil, err
	}
	if err := validation.Name("runtimeVersion", runtimeVersion); err != nil {
		return nil, err
	}
	return s.rolloutRepo.GetActiveRolloutUpdates(ctx, appId, branchName, runtimeVersion)
}

// getActiveRolloutForMutation loads the active per-update rollout rows and applies the
// optional stale-tab guard: when the caller pins the rollout update id it saw, a
// mismatch means the rollout it is acting on is gone.
func (s *RolloutService) getActiveRolloutForMutation(ctx context.Context, appId string, branchName string, runtimeVersion string, expectedUpdateId *string) ([]types.RolloutUpdate, error) {
	activeRollouts, err := s.rolloutRepo.GetActiveRolloutUpdates(ctx, appId, branchName, runtimeVersion)
	if err != nil {
		return nil, err
	}
	if len(activeRollouts) == 0 {
		return nil, &RolloutRequestError{Status: http.StatusNotFound, Message: "no active rollout for this branch and runtime version"}
	}
	if expectedUpdateId != nil {
		matched := false
		for _, activeRollout := range activeRollouts {
			if activeRollout.UpdateId == *expectedUpdateId {
				matched = true
				break
			}
		}
		if !matched {
			return nil, &RolloutRequestError{Status: http.StatusConflict, Message: "the rollout changed since this page was loaded; reload and retry"}
		}
	}
	return activeRollouts, nil
}

// SetUpdateRolloutPercentage progresses a per-update rollout. Progression is monotonic
// increase only (devices already on the update keep it regardless, so a decrease would
// only lie about the served share); 100 ends the rollout for every device. Returns the
// rows as they were before the change so the handler can invalidate their caches.
func (s *RolloutService) SetUpdateRolloutPercentage(ctx context.Context, appId string, branchName string, runtimeVersion string, percentage int, expectedUpdateId *string) ([]types.RolloutUpdate, error) {
	if s.rolloutRepo == nil {
		return nil, ErrRolloutsRequireControlPlane
	}
	if err := validation.Name("branchName", branchName); err != nil {
		return nil, err
	}
	if err := validation.Name("runtimeVersion", runtimeVersion); err != nil {
		return nil, err
	}
	if percentage < 1 || percentage > 100 {
		return nil, &RolloutRequestError{Status: http.StatusBadRequest, Message: "the rollout percentage must be between 1 and 100"}
	}
	activeRollouts, err := s.getActiveRolloutForMutation(ctx, appId, branchName, runtimeVersion, expectedUpdateId)
	if err != nil {
		return nil, err
	}
	if percentage == 100 {
		rows, err := s.rolloutRepo.ClearUpdateRollout(ctx, appId, branchName, runtimeVersion)
		if err != nil {
			return nil, err
		}
		if rows == 0 {
			return nil, &RolloutRequestError{Status: http.StatusConflict, Message: "the rollout ended concurrently; reload and retry"}
		}
		return activeRollouts, nil
	}
	currentMax := 0
	for _, activeRollout := range activeRollouts {
		if activeRollout.Percentage > currentMax {
			currentMax = activeRollout.Percentage
		}
	}
	if percentage <= currentMax {
		return nil, &RolloutRequestError{Status: http.StatusBadRequest, Message: fmt.Sprintf("the rollout percentage can only increase (currently %d%%)", currentMax)}
	}
	rows, err := s.rolloutRepo.SetUpdateRolloutPercentage(ctx, appId, branchName, runtimeVersion, percentage)
	if err != nil {
		return nil, err
	}
	if rows == 0 {
		// The SQL guard (rollout_percentage < new value) also enforces monotonic
		// increase under concurrency, so 0 rows covers both a concurrent end and a
		// concurrent progression past the requested value.
		return nil, &RolloutRequestError{Status: http.StatusConflict, Message: "the rollout ended or was progressed concurrently; reload and retry"}
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionUpdateRolloutSet,
		TargetType:    "branch",
		TargetID:      branchName,
		TargetDisplay: branchName,
		AppID:         appId,
		Metadata:      map[string]any{"runtime_version": runtimeVersion, "percentage": percentage},
	})
	return activeRollouts, nil
}

// RevertUpdateRollout returns every device to the pre-rollout state: per active row,
// the control update is republished as a new update (or, when there is no control or
// the control is itself a rollback, a rollback-to-embedded is created).
// The rollout is cleared FIRST as an atomic claim (0 rows means a concurrent finish
// or revert won the race and this call must not republish anything, which would
// otherwise downgrade the whole fleet). The claim opens a short window where the
// rolled-out update is served unrestricted until the control republish lands; that
// transient beats the alternative, a revert racing a finish and overwriting it.
// If a republish fails mid-way the rollout stays ended: the error then names the
// platforms still serving the rolled-out update and how to finish the revert by hand.
func (s *RolloutService) RevertUpdateRollout(ctx context.Context, appId string, branchName string, runtimeVersion string, expectedUpdateId *string) ([]types.RolloutUpdate, error) {
	if s.rolloutRepo == nil {
		return nil, ErrRolloutsRequireControlPlane
	}
	if err := validation.Name("branchName", branchName); err != nil {
		return nil, err
	}
	if err := validation.Name("runtimeVersion", runtimeVersion); err != nil {
		return nil, err
	}
	activeRollouts, err := s.getActiveRolloutForMutation(ctx, appId, branchName, runtimeVersion, expectedUpdateId)
	if err != nil {
		return nil, err
	}
	rows, err := s.rolloutRepo.ClearUpdateRollout(ctx, appId, branchName, runtimeVersion)
	if err != nil {
		return nil, err
	}
	if rows == 0 {
		return nil, &RolloutRequestError{Status: http.StatusConflict, Message: "the rollout ended concurrently; reload and retry"}
	}
	for i, activeRollout := range activeRollouts {
		if err := s.revertSingleRolloutUpdate(ctx, appId, branchName, runtimeVersion, activeRollout); err != nil {
			// The claim above is already consumed, so a retry gets a 409 and the
			// rolled-out update is served unrestricted on the platforms not yet
			// reverted. Name them and the manual recovery instead of failing generically.
			remaining := make([]string, 0, len(activeRollouts)-i)
			for _, r := range activeRollouts[i:] {
				remaining = append(remaining, r.Platform)
			}
			return nil, &RolloutRequestError{
				Status: http.StatusInternalServerError,
				Message: fmt.Sprintf(
					"the rollout was ended but republishing the previous update failed for %s: %v. Update %s is now served to all devices on: %s. Republish the previous update (or publish a rollback) for those platforms to finish the revert",
					activeRollout.Platform, err, activeRollout.UpdateId, strings.Join(remaining, ", "),
				),
			}
		}
	}
	recordManagementEvent(ctx, s.onAuditEvent, auditlog.Event{
		Action:        auditlog.ActionUpdateRolloutReverted,
		TargetType:    "branch",
		TargetID:      branchName,
		TargetDisplay: branchName,
		AppID:         appId,
		Metadata:      map[string]any{"runtime_version": runtimeVersion},
	})
	return activeRollouts, nil
}

func (s *RolloutService) revertSingleRolloutUpdate(ctx context.Context, appId string, branchName string, runtimeVersion string, activeRollout types.RolloutUpdate) error {
	if activeRollout.ControlUpdateId == nil {
		// First update of the branch: there is nothing to return to, so out-of-bucket
		// devices were on the embedded update. Revert recreates that state.
		_, err := s.deploymentService.createRollbackInternal(ctx, appId, activeRollout.Platform, "", runtimeVersion, branchName)
		return err
	}
	controlUpdate, err := s.updateRepo.GetUpdate(ctx, appId, branchName, runtimeVersion, *activeRollout.ControlUpdateId)
	if err != nil {
		return err
	}
	if controlUpdate == nil {
		return fmt.Errorf("control update %s not found for rollout revert", *activeRollout.ControlUpdateId)
	}
	controlType, err := s.updateRepo.GetUpdateType(ctx, *controlUpdate)
	if err != nil {
		return err
	}
	// A rollback control cannot be republished (it has no files); recreating a
	// rollback-to-embedded restores the same state for every device.
	if controlType == types.Rollback {
		_, err = s.deploymentService.createRollbackInternal(ctx, appId, activeRollout.Platform, "", runtimeVersion, branchName)
		return err
	}
	_, err = s.deploymentService.republishUpdateInternal(ctx, controlUpdate, activeRollout.Platform, "", nil)
	return err
}
