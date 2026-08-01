package cache

import "encoding/json"

// GetJSON reads key from c into a T; ok is false on a miss or an entry that
// no longer decodes, which the caller treats as a miss and refetches.
func GetJSON[T any](c Cache, key string) (T, bool) {
	var value T
	raw := c.Get(key)
	if raw == "" {
		return value, false
	}
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return value, false
	}
	return value, true
}

// SetJSON stores value under key; a nil ttlSeconds means no expiry. A value
// that fails to marshal is skipped, never stored partially.
func SetJSON[T any](c Cache, key string, value T, ttlSeconds *int) {
	payload, err := json.Marshal(value)
	if err != nil {
		return
	}
	_ = c.Set(key, string(payload), ttlSeconds)
}
