package types

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

type Asset struct {
	Path string `json:"path"`
	Ext  string `json:"ext"`
}

type PlatformMetadata struct {
	Bundle string  `json:"bundle"`
	Assets []Asset `json:"assets"`
}

// Platform is a client platform an update is built for; a value that went
// through ParsePlatform is one of the two constants.
type Platform string

const (
	PlatformIOS     Platform = "ios"
	PlatformAndroid Platform = "android"
)

// ParsePlatform accepts exactly "ios" or "android".
func ParsePlatform(raw string) (Platform, error) {
	switch Platform(raw) {
	case PlatformIOS, PlatformAndroid:
		return Platform(raw), nil
	}
	return "", fmt.Errorf("invalid platform %q", raw)
}

type FileMetadata struct {
	Android PlatformMetadata `json:"android"`
	IOS     PlatformMetadata `json:"ios"`
}

func (f FileMetadata) PlatformMetadata(platform Platform) (PlatformMetadata, error) {
	switch platform {
	case PlatformIOS:
		return f.IOS, nil
	case PlatformAndroid:
		return f.Android, nil
	default:
		return PlatformMetadata{}, fmt.Errorf("unsupported platform: %s", platform)
	}
}

type MetadataObject struct {
	Version      int          `json:"version"`
	Bundler      string       `json:"bundler"`
	FileMetadata FileMetadata `json:"fileMetadata"`
}

type UpdateMetadata struct {
	MetadataJSON MetadataObject `json:"metadataJSON"`
	CreatedAt    string         `json:"createdAt"`
	ID           string         `json:"id"`
	Fingerprint  string         `json:"fingerprint"`
}

type UpdateItem struct {
	UpdateUUID string   `json:"updateUUID"`
	UpdateId   string   `json:"updateId"`
	CreatedAt  string   `json:"createdAt"`
	CommitHash string   `json:"commitHash"`
	Platform   Platform `json:"platform"`
	Message    string   `json:"message,omitempty"`
	// Progressive rollout state (control-plane mode only). Both stay nil in stateless
	// mode and for non-rollout updates, so listings there serialize byte-identically.
	RolloutPercentage *int    `json:"rolloutPercentage,omitempty"`
	ControlUpdateId   *string `json:"controlUpdateId,omitempty"`
	// CLI-minted UUID shared by the per-platform rows of one eoas run
	// (control-plane mode only); nil for rows created by older CLIs and in
	// stateless mode, which consumers display ungrouped.
	PublishGroup *string `json:"publishGroup,omitempty"`
}

// UpdateFeedItem is the control-plane read model for the dashboard's global
// update feed. Stateless storage cannot produce this efficiently because its
// branch and runtime hierarchy lives in bucket folders.
type UpdateFeedItem struct {
	UpdateItem
	Branch         string    `json:"branch"`
	RuntimeVersion string    `json:"runtimeVersion"`
	BranchID       int64     `json:"-"`
	FeedCreatedAt  time.Time `json:"-"`
}

type UpdateFeedQuery struct {
	Branch          string
	RuntimeVersion  string
	Platform        Platform
	UpdateUUID      string
	PublishGroup    string
	CommitHash      string
	From            *time.Time
	To              *time.Time
	CursorCreatedAt *time.Time
	CursorBranchID  int64
	CursorUpdateID  int64
	Limit           int
}

type UpdateFeedPage struct {
	Items      []UpdateFeedItem `json:"items"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

// PublishGroupMember is one update row of a publish group, as needed to fan
// the group republish out to its per-platform members. No update type here:
// republishing validates the member (normal, valid, right platform) through
// the same path as a single republish.
type PublishGroupMember struct {
	UpdateId   string
	Platform   Platform
	CommitHash string
}

// PublishGroupItem is one logical eoas publish. Its per-platform rows are
// returned together so a page boundary can never split a publish group.
type PublishGroupItem struct {
	PublishGroup string                   `json:"publishGroup"`
	CreatedAt    string                   `json:"createdAt"`
	CommitHash   string                   `json:"commitHash"`
	Message      string                   `json:"message,omitempty"`
	Platforms    []string                 `json:"platforms"`
	Updates      []PublishGroupUpdateItem `json:"updates"`
}

type PublishGroupUpdateItem struct {
	UpdateId   string   `json:"updateId"`
	CreatedAt  string   `json:"createdAt"`
	Platform   Platform `json:"platform"`
	CommitHash string   `json:"commitHash"`
}

type PublishGroupsPage struct {
	Items      []PublishGroupItem `json:"items"`
	NextCursor *string            `json:"nextCursor"`
}

type UpdatesPage struct {
	Items      []UpdateItem `json:"items"`
	NextCursor *string      `json:"nextCursor"`
}

type UpdateStoredMetadata struct {
	Platform   Platform `json:"platform"`
	CommitHash string   `json:"commitHash"`
	UpdateUUID string   `json:"updateUUID"`
	Message    string   `json:"message,omitempty"`
}

type UpdateType int

const (
	NormalUpdate UpdateType = iota
	Rollback
)

type UpdateDetails struct {
	UpdateUUID string     `json:"updateUUID"`
	UpdateId   string     `json:"updateId"`
	CreatedAt  string     `json:"createdAt"`
	CommitHash string     `json:"commitHash"`
	Platform   Platform   `json:"platform"`
	Message    string     `json:"message,omitempty"`
	Type       UpdateType `json:"type"`
	ExpoConfig string     `json:"expoConfig"`
	// Progressive rollout state (control-plane mode only); nil in stateless mode and
	// for non-rollout updates.
	RolloutPercentage *int    `json:"rolloutPercentage,omitempty"`
	ControlUpdateId   *string `json:"controlUpdateId,omitempty"`
}

// UpdateRef is the (update id, runtime version) pair that, with a branch,
// locates an update's folder in the bucket.
type UpdateRef struct {
	ID             int64
	RuntimeVersion string
}

type ApiKeyMetadata struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Hint       string  `json:"hint"`
	CreatedAt  string  `json:"createdAt"`
	LastUsedAt *string `json:"lastUsedAt,omitempty"`
}

type ManifestAsset struct {
	Hash          string `json:"hash"`
	Key           string `json:"key"`
	FileExtension string `json:"fileExtension"`
	ContentType   string `json:"contentType"`
	Url           string `json:"url"`
}

// UpdateAssetMapping is the shaped launch asset and assets persisted on an
// update. Url is rebuilt at serve time.
type UpdateAssetMapping struct {
	LaunchAsset ShapedAsset   `json:"launchAsset"`
	Assets      []ShapedAsset `json:"assets"`
	ConfigFiles []ConfigFile  `json:"configFiles,omitempty"`
}

type ConfigFile struct {
	Path string `json:"path"`
	Hash string `json:"hash"`
}

func (m *UpdateAssetMapping) Scan(src any) error {
	if src == nil {
		return nil
	}
	var data []byte
	switch v := src.(type) {
	case []byte:
		data = v
	case string:
		data = []byte(v)
	default:
		return fmt.Errorf("cannot scan %T into UpdateAssetMapping", src)
	}
	return json.Unmarshal(data, m)
}

func (m UpdateAssetMapping) Value() (driver.Value, error) {
	return json.Marshal(m)
}

// ShapedAsset is a ManifestAsset without Url.
type ShapedAsset struct {
	Hash          string `json:"hash"`
	Key           string `json:"key"`
	FileExtension string `json:"fileExtension"`
	ContentType   string `json:"contentType"`
}

type ExtraManifestData struct {
	ExpoClient           json.RawMessage `json:"expoClient"`
	Branch               string          `json:"branch"`
	BranchSurfingRefused string          `json:"branchSurfingRefused,omitempty"`
}

type UpdateManifest struct {
	Id             string            `json:"id"`
	CreatedAt      string            `json:"createdAt"`
	RunTimeVersion string            `json:"runtimeVersion"`
	Metadata       json.RawMessage   `json:"metadata"`
	Assets         []ManifestAsset   `json:"assets"`
	LaunchAsset    ManifestAsset     `json:"launchAsset"`
	Extra          ExtraManifestData `json:"extra"`
}

type RollbackDirectiveParameters struct {
	CommitTime string `json:"commitTime"`
}

type RollbackDirective struct {
	Type       string                      `json:"type"`
	Parameters RollbackDirectiveParameters `json:"parameters"`
}

type NoUpdateAvailableDirective struct {
	Type string `json:"type"`
}

type Update struct {
	AppId          string        `json:"appId"`
	Branch         string        `json:"branch"`
	RuntimeVersion string        `json:"runtimeVersion"`
	UpdateId       string        `json:"updateId"`
	CreatedAt      time.Duration `json:"createdAt"`
	// UpdateUUID is the persistent manifest id, filled only on the lastUpdate
	// envelope path so the up-to-date poll can short-circuit without reading
	// the composed manifest. Empty everywhere else.
	UpdateUUID string `json:"updateUuid,omitempty"`
}

// UpdateWithRollout is the flat lastUpdate envelope: an update plus its per-update
// rollout state. RolloutPercentage and Control are nil for a plain (non-rollout) update.
// The control is embedded so out-of-bucket resolution needs no second read.
type UpdateWithRollout struct {
	Update
	RolloutPercentage *int    `json:"rolloutPercentage,omitempty"`
	Control           *Update `json:"control,omitempty"`
}

// ChannelRollout is the full channel-rollout summary returned by the dashboard rollout
// routes. DefaultBranchName is the channel's currently mapped branch (served to the
// out-of-rollout cohort); RolloutBranchName is served to Percentage% of devices.
type ChannelRollout struct {
	ID                string `json:"id"`
	ChannelName       string `json:"channelName"`
	DefaultBranchName string `json:"defaultBranchName"`
	RolloutBranchName string `json:"rolloutBranchName"`
	Percentage        int    `json:"percentage"`
	CreatedAt         string `json:"createdAt"`
	UpdatedAt         string `json:"updatedAt"`
}

// RolloutUpdate is one active per-update rollout row (one per platform) as returned by
// the per-update rollout route.
type RolloutUpdate struct {
	UpdateId        string   `json:"updateId"`
	Platform        Platform `json:"platform"`
	Percentage      int      `json:"percentage"`
	ControlUpdateId *string  `json:"controlUpdateId,omitempty"`
	CreatedAt       string   `json:"createdAt"`
}

type BranchUpdateState struct {
	RuntimeVersion    string `json:"runtimeVersion"`
	CommitHash        string `json:"commitHash"`
	CreatedAt         string `json:"createdAt"`
	RolloutPercentage *int   `json:"rolloutPercentage,omitempty"`
}

type ChannelMapping struct {
	ReleaseChannelName string  `json:"releaseChannelName"`
	ReleaseChannelId   string  `json:"releaseChannelId"`
	BranchName         *string `json:"branchName"`
	BranchId           *string `json:"branchId"`
	CreatedAt          *string `json:"createdAt"`
	// Current update state for the default branch and, during a channel rollout,
	// its rollout branch. Populated in control-plane mode only.
	BranchCurrentUpdate        *BranchUpdateState `json:"branchCurrentUpdate,omitempty"`
	RolloutBranchCurrentUpdate *BranchUpdateState `json:"rolloutBranchCurrentUpdate,omitempty"`
	// Active channel rollout, if any (control-plane mode only); nil otherwise.
	Rollout *ChannelRollout `json:"rollout,omitempty"`
	// Branch-surfing setting of the channel; nil in stateless mode, where the
	// setting does not exist.
	BranchSurfing *BranchSurfing `json:"branchSurfing,omitempty"`
	// Environment the channel points to by default; nil when unbound or in
	// stateless mode.
	EnvironmentName *string `json:"environmentName,omitempty"`
}

// BranchSurfing is a channel's branch-surfing setting: whether a device polling
// the channel may ask to be served a branch other than the mapped one, and
// which branches it may reach. Pattern uses the "*" wildcard language of
// branch.MatchPattern.
type BranchSurfing struct {
	Enabled bool   `json:"enabled"`
	Pattern string `json:"pattern"`
}

// SurfableBranch is one entry of the branch list a device may surf to.
// SurfableBranchList is the answer to a device's branch list request. Total
// counts every branch that matched, so a client showing a truncated page can say
// so instead of pretending the list is complete.
type SurfableBranchList struct {
	Branches []SurfableBranch `json:"branches"`
	Total    int              `json:"total"`
}

type SurfableBranch struct {
	Name         string `json:"name"`
	LastUpdateAt string `json:"lastUpdateAt"`
}

type BranchMapping struct {
	BranchName     string  `json:"branchName"`
	BranchId       *string `json:"branchId"`
	ReleaseChannel *string `json:"releaseChannel"`
	CreatedAt      *string `json:"createdAt"`
	// Branch protection: a protected branch refuses to be deleted, by anyone.
	// It says nothing about what may be published on it, which is decided per
	// API token. Always false in stateless mode.
	Protected bool `json:"protected"`
	// Latest runtime's active rollout update, or its latest checked update.
	// Populated in control-plane mode only.
	CurrentUpdate *BranchUpdateState `json:"currentUpdate,omitempty"`
}

type RuntimeVersionWithStats struct {
	RuntimeVersion    string `json:"runtimeVersion"`
	LastUpdatedAt     string `json:"lastUpdatedAt"`
	CreatedAt         string `json:"createdAt"`
	NumberOfUpdates   int    `json:"numberOfUpdates"`
	ActiveRollout     bool   `json:"activeRollout,omitempty"`
	RolloutPercentage *int   `json:"rolloutPercentage,omitempty"`
}

type BucketFile struct {
	Reader    io.ReadCloser
	CreatedAt time.Time
}

type Auth struct {
	Token         *string
	SessionSecret *string
}

// ChannelRolloutInfo is the active channel rollout folded into a ChannelResolution in
// control-plane mode. ID doubles as the bucketing salt. The stateless (Expo) provider
// never sets it, so rollouts stay a control-plane-only feature.
type ChannelRolloutInfo struct {
	ID         string `json:"id"`
	BranchName string `json:"branchName"`
	Percentage int    `json:"percentage"`
}

// ChannelResolution is the branch a channel serves to devices, with its active
// rollout; the dashboard listing shape is ChannelMapping.
type ChannelResolution struct {
	Id         string `json:"id"`
	BranchName string `json:"branchName"`
	// Set only by the Postgres channel store when the channel has an active rollout.
	Rollout *ChannelRolloutInfo `json:"rollout,omitempty"`
}

// BundlePatchStatus is where a (target, source) bundle patch stands.
type BundlePatchStatus string

const (
	BundlePatchPending   BundlePatchStatus = "pending"
	BundlePatchRunning   BundlePatchStatus = "running"
	BundlePatchStored    BundlePatchStatus = "stored"
	BundlePatchSkipped   BundlePatchStatus = "skipped"
	BundlePatchFailed    BundlePatchStatus = "failed"
	BundlePatchCancelled BundlePatchStatus = "cancelled"
)

// Reasons a patch job ends without a stored patch.
const (
	BundlePatchReasonLegacyUpdate       = "legacy_update"
	BundlePatchReasonIdenticalBundles   = "identical_bundles"
	BundlePatchReasonNotWorth           = "patch_not_worth"
	BundlePatchReasonBundleTooLarge     = "bundle_too_large"
	BundlePatchReasonBlobMissing        = "blob_missing"
	BundlePatchReasonUpdateNotFound     = "update_not_found"
	BundlePatchReasonDifferentBranch    = "different_branch"
	BundlePatchReasonVerificationFailed = "verification_failed"
)

// BundlePatch is one row of the bundle patches of a target update, as the
// dashboard and the MCP show it.
type BundlePatch struct {
	TargetUpdateId   string            `json:"targetUpdateId"`
	TargetUpdateUUID string            `json:"targetUpdateUUID"`
	SourceUpdateId   string            `json:"sourceUpdateId"`
	SourceUpdateUUID string            `json:"sourceUpdateUUID"`
	SourceCommitHash string            `json:"sourceCommitHash"`
	SourceMessage    string            `json:"sourceMessage,omitempty"`
	SourceCreatedAt  string            `json:"sourceCreatedAt"`
	Status           BundlePatchStatus `json:"status"`
	Reason           string            `json:"reason,omitempty"`
	PatchSize        *int64            `json:"patchSize,omitempty"`
	FullDownloadSize *int64            `json:"fullDownloadSize,omitempty"`
	Attempts         int               `json:"attempts"`
	UpdatedAt        string            `json:"updatedAt"`
}
