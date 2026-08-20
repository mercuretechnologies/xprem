// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"xprem/config"
	"xprem/internal/cache"
	"xprem/internal/cdn"
	"xprem/internal/services"
	"xprem/internal/version"
)

var heartbeatClient = &http.Client{Timeout: 10 * time.Second}

const (
	heartbeatInterval = time.Hour
	heartbeatLockKey  = "telemetry-heartbeat-lock"
	// Shorter than the interval so the next tick can always reclaim the lock.
	heartbeatLockTTLSeconds = 3300
)

// Enum values accepted by the heartbeat API's telemetry schema.
const (
	TelemetryModeStateless    = "stateless"
	TelemetryModeControlPlane = "control_plane"

	TelemetryBucketS3    = "s3"
	TelemetryBucketGCS   = "gcs"
	TelemetryBucketAzure = "azure"
	TelemetryBucketLocal = "local"

	TelemetryCDNCloudfront  = "cloudfront"
	TelemetryCDNAzureDirect = "azure_direct"
	TelemetryCDNGCSDirect   = "gcs_direct"
	TelemetryCDNGeneric     = "generic"
	TelemetryCDNS3Direct    = "s3_direct"

	TelemetryCacheLocal         = "local"
	TelemetryCacheRedis         = "redis"
	TelemetryCacheRedisSentinel = "redis-sentinel"
)

type HeartbeatTelemetry struct {
	AppsCount  *int    `json:"appsCount,omitempty"`
	UsersCount *int    `json:"usersCount,omitempty"`
	Mode       *string `json:"mode,omitempty"`
	Bucket     *string `json:"bucket,omitempty"`
	CDN        *string `json:"cdn,omitempty"`
	Cache      *string `json:"cache,omitempty"`
	UseObserve *bool   `json:"useObserve,omitempty"`
}

type HeartbeatParams struct {
	InstanceId string             `json:"instanceId"`
	BaseUrl    string             `json:"baseUrl"`
	Version    string             `json:"version"`
	Telemetry  HeartbeatTelemetry `json:"telemetry"`
}

type TelemetryService struct {
	userRepo   services.UserRepository
	appRepo    services.AppRepository
	instanceId string
}

// NewTelemetryService accepts nil repos: stateless mode has no user store, and
// the matching count is simply omitted from the payload.
func NewTelemetryService(userRepo services.UserRepository, appRepo services.AppRepository, instanceId string) *TelemetryService {
	return &TelemetryService{
		userRepo:   userRepo,
		appRepo:    appRepo,
		instanceId: instanceId,
	}
}

// Start sends one heartbeat now and one per hour until ctx is cancelled. The
// cache lock keeps a multi-replica deployment at one send per interval.
func (s *TelemetryService) Start(ctx context.Context) {
	if config.IsServerTelemetryDisabled() || config.IsTestMode() {
		return
	}
	go func() {
		s.heartbeat(ctx)
		ticker := time.NewTicker(heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.heartbeat(ctx)
			}
		}
	}()
}

func (s *TelemetryService) sendHeartBeatRequest(ctx context.Context, params HeartbeatParams) error {
	const endpoint = "https://api.xprem.dev/v1/heartbeats"
	body, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal heartbeat payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build heartbeat request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := heartbeatClient.Do(req)
	if err != nil {
		return fmt.Errorf("send heartbeat: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("heartbeat rejected: %s", resp.Status)
	}
	return nil
}

func (s *TelemetryService) buildTelemetryParams(ctx context.Context) *HeartbeatTelemetry {
	response := &HeartbeatTelemetry{}

	mode := TelemetryModeStateless
	if config.IsDBMode() {
		mode = TelemetryModeControlPlane
	}
	response.Mode = &mode

	if bucket := config.GetEnv("STORAGE_MODE"); bucket != "" {
		response.Bucket = &bucket
	}
	if cdnType := telemetryCDNType(); cdnType != "" {
		response.CDN = &cdnType
	}
	cacheMode := string(cache.ResolveCacheType())
	response.Cache = &cacheMode

	useObserve := config.GetClickHouseURL() != ""
	response.UseObserve = &useObserve

	if s.appRepo != nil {
		if apps, err := s.appRepo.GetApps(ctx); err == nil {
			appsCount := len(apps)
			response.AppsCount = &appsCount
		}
	}
	if s.userRepo != nil {
		if users, err := s.userRepo.GetUsers(ctx); err == nil {
			usersCount := len(users)
			response.UsersCount = &usersCount
		}
	}
	return response
}

func telemetryCDNType() string {
	switch cdn.ResolvedType() {
	case "cloudfront":
		return TelemetryCDNCloudfront
	case "azure-direct":
		return TelemetryCDNAzureDirect
	case "gcs-direct":
		return TelemetryCDNGCSDirect
	case "s3-direct":
		return TelemetryCDNS3Direct
	case "generic":
		return TelemetryCDNGeneric
	default:
		return ""
	}
}

func (s *TelemetryService) buildParams(ctx context.Context) *HeartbeatParams {
	telemetry := s.buildTelemetryParams(ctx)
	return &HeartbeatParams{
		BaseUrl:    config.GetEnv("BASE_URL"),
		Version:    version.Version,
		InstanceId: s.instanceId,
		Telemetry:  *telemetry,
	}
}

func (s *TelemetryService) heartbeat(ctx context.Context) {
	locked, err := cache.GetCache().TryLock(heartbeatLockKey, heartbeatLockTTLSeconds)
	if err != nil || !locked {
		return
	}
	params := s.buildParams(ctx)
	s.sendHeartBeatRequest(ctx, *params)
}
