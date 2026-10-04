package repository

import (
	"context"
	"time"
	"xprem/internal/bucket"
	"xprem/internal/cache"

	"github.com/google/uuid"
)

const (
	instanceIDLockKey        = "instance-id-lock"
	instanceIDLockTTLSeconds = 30
	instanceIDWaitPoll       = 2 * time.Second
	instanceIDMaxWaits       = 5
)

// BucketServerInstanceRepository is the stateless-mode counterpart of
// PostgresServerInstanceRepository: the deployment id lives in the bucket's
// .instanceid root file.
type BucketServerInstanceRepository struct {
	instanceStore *bucket.InstanceStore
	cache         cache.Cache
}

func NewBucketServerInstanceRepository(instanceStore *bucket.InstanceStore, c cache.Cache) *BucketServerInstanceRepository {
	return &BucketServerInstanceRepository{instanceStore: instanceStore, cache: c}
}

// GetOrCreateInstanceID mints the id on first boot, under the cache lock so
// concurrent replicas agree on a single one. A replica that cannot get the
// lock within instanceIDMaxWaits polls mints anyway rather than blocking boot.
func (s *BucketServerInstanceRepository) GetOrCreateInstanceID(ctx context.Context) (string, error) {
	locked := false
	for attempt := 0; ; attempt++ {
		id, err := s.instanceStore.ID(ctx)
		if err != nil {
			return "", err
		}
		if id != "" {
			return id, nil
		}
		locked, err = s.cache.TryLock(instanceIDLockKey, instanceIDLockTTLSeconds)
		if err != nil {
			return "", err
		}
		if locked || attempt >= instanceIDMaxWaits {
			break
		}
		time.Sleep(instanceIDWaitPoll)
	}
	if locked {
		defer s.cache.Delete(instanceIDLockKey)
	}
	id, err := s.instanceStore.ID(ctx)
	if err != nil {
		return "", err
	}
	if id != "" {
		return id, nil
	}
	minted := uuid.New().String()
	if err := s.instanceStore.Persist(ctx, minted); err != nil {
		return "", err
	}
	return minted, nil
}
