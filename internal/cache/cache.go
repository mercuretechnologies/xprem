package cache

import (
	"strings"
	"sync"
	"xprem/config"
)

type Cache interface {
	Get(key string) string
	Set(key string, value string, ttl *int) error
	Delete(key string)
	Clear() error
	TryLock(key string, ttl int) (bool, error)
	Sadd(key string, members []string, ttl *int) error
	Scard(key string) (int64, error)
	Incr(key string, ttl int) (int64, error)
}

type CacheType string

const (
	LocalCacheType         CacheType = "local"
	RedisCacheType         CacheType = "redis"
	RedisSentinelCacheType CacheType = "redis-sentinel"
)

const defaultPrefix = "expoopenota"

// withPrefix scopes every key to the deployment (CACHE_KEY_PREFIX). It must
// NOT embed the release version: locks, rate-limit counters and active-user
// sets have to survive deploys and coordinate across mixed-version replicas.
// Serialized payloads get their release-scoping from their key builders.
func withPrefix(key string) string {
	prefix := config.GetEnv("CACHE_KEY_PREFIX")
	if prefix == "" {
		prefix = defaultPrefix
	}
	return prefix + ":" + key
}

// Key joins segments with ":" after escaping any "%" and ":" they contain, so
// a segment holding the separator (a branch named "x:1", an "exposdk:52.0.0"
// runtime version) can never collide two different keys onto one.
func Key(segments ...string) string {
	escaped := make([]string, len(segments))
	for i, segment := range segments {
		segment = strings.ReplaceAll(segment, "%", "%25")
		escaped[i] = strings.ReplaceAll(segment, ":", "%3A")
	}
	return strings.Join(escaped, ":")
}

func ResolveCacheType() CacheType {
	cacheType := config.GetEnv("CACHE_MODE")
	switch cacheType {
	case "redis":
		return RedisCacheType
	case "redis-sentinel":
		return RedisSentinelCacheType
	default:
		return LocalCacheType
	}
}

func parseSentinelAddrs(addrs string) []string {
	parts := strings.Split(addrs, ",")
	sentinelAddrs := make([]string, 0, len(parts))
	for _, part := range parts {
		addr := strings.TrimSpace(part)
		if addr != "" {
			sentinelAddrs = append(sentinelAddrs, addr)
		}
	}
	return sentinelAddrs
}

var (
	cacheInstance Cache
	once          sync.Once
)

func GetCache() Cache {
	once.Do(func() {
		cacheType := ResolveCacheType()
		switch cacheType {
		case LocalCacheType:
			cacheInstance = NewLocalCache()
		case RedisCacheType:
			host := config.GetEnv("REDIS_HOST")
			password := config.GetEnv("REDIS_PASSWORD")
			port := config.GetEnv("REDIS_PORT")
			useTLS := config.GetEnv("REDIS_USE_TLS") == "true"
			username := config.GetEnv("REDIS_USERNAME")
			caCertB64 := config.GetEnv("REDIS_CA_CERT_B64")
			cacheInstance = NewRedisCache(host, password, port, useTLS, username, caCertB64)
		case RedisSentinelCacheType:
			sentinelAddrsStr := config.GetEnv("REDIS_SENTINEL_ADDRS")
			sentinelAddrs := parseSentinelAddrs(sentinelAddrsStr)
			if len(sentinelAddrs) == 0 {
				panic("REDIS_SENTINEL_ADDRS must contain at least one Sentinel address")
			}
			masterName := config.GetEnv("REDIS_SENTINEL_MASTER_NAME")
			if masterName == "" {
				masterName = "mymaster"
			}
			password := config.GetEnv("REDIS_PASSWORD")
			useTLS := config.GetEnv("REDIS_USE_TLS") == "true"
			username := config.GetEnv("REDIS_USERNAME")
			caCertB64 := config.GetEnv("REDIS_CA_CERT_B64")
			cacheInstance = NewRedisSentinelCache(sentinelAddrs, masterName, password, useTLS, username, caCertB64)
		default:
			panic("Unknown cache type")
		}
	})
	return cacheInstance
}
