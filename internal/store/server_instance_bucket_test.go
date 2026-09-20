package store_test

import (
	"context"
	"testing"
	"xprem/internal/bucket"
	"xprem/internal/cache"
	"xprem/internal/store"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestBucketInstanceIDMintedOnceAndStable(t *testing.T) {
	b := &bucket.LocalBucket{BasePath: t.TempDir()}
	s := store.NewBucketServerInstanceStore(b, cache.NewLocalCache())

	first, err := s.GetOrCreateInstanceID(context.Background())
	require.NoError(t, err)
	_, err = uuid.Parse(first)
	require.NoError(t, err)

	second, err := s.GetOrCreateInstanceID(context.Background())
	require.NoError(t, err)
	require.Equal(t, first, second)
}

func TestBucketInstanceIDAdoptsExistingFile(t *testing.T) {
	b := &bucket.LocalBucket{BasePath: t.TempDir()}
	existing := uuid.New().String()
	require.NoError(t, b.PersistInstanceID(t.Context(), existing))

	s := store.NewBucketServerInstanceStore(b, cache.NewLocalCache())
	got, err := s.GetOrCreateInstanceID(context.Background())
	require.NoError(t, err)
	require.Equal(t, existing, got)
}

func TestBucketInstanceIDLockWaitRespectsCancellation(t *testing.T) {
	b := &bucket.LocalBucket{BasePath: t.TempDir()}
	locks := cache.NewLocalCache()
	locked, err := locks.TryLock("instance-id-lock", 30)
	require.NoError(t, err)
	require.True(t, locked)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	id, err := store.NewBucketServerInstanceStore(b, locks).GetOrCreateInstanceID(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, id)
	id, err = b.GetInstanceID(t.Context())
	require.NoError(t, err)
	require.Empty(t, id)
}
