package services

import (
	"io"
	"xprem/internal/types"
	"xprem/internal/validation"
)

// Keys and payloads belong to each cache protocol; storage treats them as opaque.
func validateCacheKey(namespace types.BuildCacheNamespace, key string) error {
	switch namespace {
	case types.BuildCacheGradle, types.BuildCacheCcache:
		if !buildCacheArchiveKey.MatchString(key) {
			return validation.Errorf("cache", "invalid archive key")
		}
		return nil
	default:
		return validation.Errorf("cache", "unsupported namespace")
	}
}

func validateCacheUpload(input BuildCacheInput) error {
	switch input.Namespace {
	case types.BuildCacheGradle, types.BuildCacheCcache:
		// Even an empty TAR contains two 512-byte end blocks.
		if input.Size < 1024 {
			return validation.Errorf("cache", "archive must contain at least 1024 bytes")
		}
	}
	return validateCacheKey(input.Namespace, input.Key)
}

func validateCacheContent(namespace types.BuildCacheNamespace, reader io.Reader) error {
	switch namespace {
	case types.BuildCacheGradle, types.BuildCacheCcache:
		return validateBuildCacheArchive(reader)
	default:
		return validation.Errorf("cache", "unsupported namespace")
	}
}
