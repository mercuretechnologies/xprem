package handlers

import "xprem/internal/types"

type createAppResponse struct {
	AppId string `json:"appId"`
}

type createBranchResponse struct {
	BranchId string `json:"branchId"`
}

type createChannelResponse struct {
	ChannelId string `json:"channelId"`
}

type createApiKeyResponse struct {
	ApiKey string `json:"apiKey"`
}

type updateRolloutResponse struct {
	Active  bool                  `json:"active"`
	Updates []types.RolloutUpdate `json:"updates"`
}
