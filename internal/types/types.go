package types

import (
	"encoding/json"
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

type FileMetadata struct {
	Android PlatformMetadata `json:"android"`
	IOS     PlatformMetadata `json:"ios"`
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
	UpdateUUID string `json:"updateUUID"`
	UpdateId   string `json:"updateId"`
	CreatedAt  string `json:"createdAt"`
	CommitHash string `json:"commitHash"`
	Platform   string `json:"platform"`
	Message    string `json:"message,omitempty"`
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
	Platform        string
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
	Platform   string
	CommitHash string
}

type UpdateStoredMetadata struct {
	Platform   string `json:"platform"`
	CommitHash string `json:"commitHash"`
	UpdateUUID string `json:"updateUUID"`
	Message    string `json:"message,omitempty"`
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
	Platform   string     `json:"platform"`
	Message    string     `json:"message,omitempty"`
	Type       UpdateType `json:"type"`
	ExpoConfig string     `json:"expoConfig"`
	// Progressive rollout state (control-plane mode only); nil in stateless mode and
	// for non-rollout updates.
	RolloutPercentage *int    `json:"rolloutPercentage,omitempty"`
	ControlUpdateId   *string `json:"controlUpdateId,omitempty"`
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

type ExtraManifestData struct {
	ExpoClient json.RawMessage `json:"expoClient"`
	Branch     string          `json:"branch"`
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
	UpdateId        string  `json:"updateId"`
	Platform        string  `json:"platform"`
	Percentage      int     `json:"percentage"`
	ControlUpdateId *string `json:"controlUpdateId,omitempty"`
	CreatedAt       string  `json:"createdAt"`
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
}

type BranchMapping struct {
	BranchName     string  `json:"branchName"`
	BranchId       *string `json:"branchId"`
	ReleaseChannel *string `json:"releaseChannel"`
	CreatedAt      *string `json:"createdAt"`
	// Enterprise branch protection; always false in stateless mode.
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
