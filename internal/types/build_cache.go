package types

import "time"

const MaxBuildCacheObjectBytes int64 = 512 << 20
const MaxBuildCacheBytes int64 = 10 << 30
const MaxBuildCacheObjects = 100

// BuildCacheNamespace identifies the cache protocol, independently of the build platform.
type BuildCacheNamespace string

const (
	BuildCacheGradle BuildCacheNamespace = "gradle"
	BuildCacheCcache BuildCacheNamespace = "ccache"
)

// BuildCacheObject stores opaque bytes; each cache protocol defines its keys and payloads.
type BuildCacheObject struct {
	ID              string              `json:"id"`
	AppID           string              `json:"-"`
	AppIdentifierID string              `json:"-"`
	Namespace       BuildCacheNamespace `json:"namespace"`
	CacheKey        string              `json:"key"`
	Size            int64               `json:"size"`
	SHA256          string              `json:"sha256"`
	CreatedAt       time.Time           `json:"-"`
	ExpiresAt       time.Time           `json:"-"`
	PublishedAt     *time.Time          `json:"-"`
}
