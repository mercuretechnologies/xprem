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
	require.NoError(t, b.PersistInstanceID(existing))

	s := store.NewBucketServerInstanceStore(b, cache.NewLocalCache())
	got, err := s.GetOrCreateInstanceID(context.Background())
	require.NoError(t, err)
	require.Equal(t, existing, got)
}
