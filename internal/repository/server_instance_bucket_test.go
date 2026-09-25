package repository_test

import (
	"context"
	"testing"
	"xprem/internal/bucket"
	"xprem/internal/cache"
	"xprem/internal/objectstore"
	"xprem/internal/repository"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestBucketInstanceIDMintedOnceAndStable(t *testing.T) {
	b := bucket.Open(objectstore.ModeLocal, t.TempDir(), "")
	s := repository.NewBucketServerInstanceRepository(b.InstanceStore, cache.NewLocalCache())

	first, err := s.GetOrCreateInstanceID(context.Background())
	require.NoError(t, err)
	_, err = uuid.Parse(first)
	require.NoError(t, err)

	second, err := s.GetOrCreateInstanceID(context.Background())
	require.NoError(t, err)
	require.Equal(t, first, second)
}

func TestBucketInstanceIDAdoptsExistingFile(t *testing.T) {
	b := bucket.Open(objectstore.ModeLocal, t.TempDir(), "")
	existing := uuid.New().String()
	require.NoError(t, b.InstanceStore.Persist(context.Background(), existing))

	s := repository.NewBucketServerInstanceRepository(b.InstanceStore, cache.NewLocalCache())
	got, err := s.GetOrCreateInstanceID(context.Background())
	require.NoError(t, err)
	require.Equal(t, existing, got)
}
