package store

import (
	"errors"
	"fmt"
	"strings"
)

var ErrNotSupportedInStatelessMode = errors.New("operation not supported in stateless mode")

// ErrPublishBlockedByActiveRollout and ErrRolloutSupersededByNewerUpdate are the
// two refusal reasons of the conditional MarkUpdateAsChecked stamp.
var ErrPublishBlockedByActiveRollout = errors.New("update cannot become visible: a progressive rollout is active on its branch and runtime version")
var ErrRolloutSupersededByNewerUpdate = errors.New("rollout activation refused: another update was published on this branch during the upload")

// ErrWouldLeaveNoAdmin is returned by the guarded user delete/demote paths
// when the target is the last remaining admin.
var ErrWouldLeaveNoAdmin = errors.New("operation refused: it would leave the dashboard without any admin account")

type ErrBranchHasActiveChannels struct {
	BranchName   string
	ChannelNames []string
}

func (e *ErrBranchHasActiveChannels) Error() string {
	channelsList := strings.Join(e.ChannelNames, ", ")
	return fmt.Sprintf("cannot delete branch %q because the following channels are still pointed to it: [%s]. Please unbind or delete these channels first.", e.BranchName, channelsList)
}

type ErrBranchInActiveRollout struct {
	BranchName   string
	ChannelNames []string
}

func (e *ErrBranchInActiveRollout) Error() string {
	channelsList := strings.Join(e.ChannelNames, ", ")
	return fmt.Sprintf("cannot delete branch %q because it is serving an active rollout on the following channels: [%s]. Promote or revert these rollouts first.", e.BranchName, channelsList)
}

// ErrBranchProtected refuses a delete; protection has no effect on what an API
// key may publish.
type ErrBranchProtected struct {
	BranchName string
}

func (e *ErrBranchProtected) Error() string {
	return fmt.Sprintf("cannot delete branch %q because it is protected. Remove its protection first.", e.BranchName)
}

type ErrChannelHasActiveRollout struct {
	ChannelName string
}

func (e *ErrChannelHasActiveRollout) Error() string {
	return fmt.Sprintf("cannot change the branch mapping of channel %q while it has an active rollout. Promote or revert the rollout first.", e.ChannelName)
}

// ErrIdentifierHasCredentials refuses deleting an app identifier that still
// holds signing credentials; the keystore must be removed explicitly first.
type ErrIdentifierHasCredentials struct {
	Identifier string
}

func (e *ErrIdentifierHasCredentials) Error() string {
	return fmt.Sprintf("cannot delete identifier %q because it still holds signing credentials. Delete its credentials first.", e.Identifier)
}

type ErrResourceAlreadyExists struct {
	Resource   string
	Identifier string
}

func (e *ErrResourceAlreadyExists) Error() string {
	return fmt.Sprintf("cannot create %s: a resource with identifier %q already exists.", e.Resource, e.Identifier)
}

type ErrResourceNotFound struct {
	Resource   string
	Identifier string
}

func (e *ErrResourceNotFound) Error() string {
	return fmt.Sprintf("%s with identifier %q not found.", e.Resource, e.Identifier)
}

type ErrEnvironmentHasChannels struct {
	EnvironmentName string
}

func (e *ErrEnvironmentHasChannels) Error() string {
	return fmt.Sprintf("cannot delete environment %q because channels still point to it. Unbind or delete these channels first.", e.EnvironmentName)
}
