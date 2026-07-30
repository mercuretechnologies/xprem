package store

import (
	"context"
	"xprem/config"
	"xprem/internal/bucket"
	"xprem/internal/providers/expo"

	"fmt"
)

type BucketAppStore struct {
	bucket bucket.Bucket
}

func NewBucketAppStore(bucket bucket.Bucket) *BucketAppStore {
	return &BucketAppStore{
		bucket: bucket,
	}
}

func (s *BucketAppStore) GetApps(ctx context.Context) ([]config.AppDescriptor, error) {
	apps := config.ListApps()
	// The flat env carries no display name; resolve it from Expo instead,
	// best-effort and cached. "" keeps the id-as-label fallback.
	for i := range apps {
		if apps[i].Name == "" {
			apps[i].Name = expo.FetchAppName(ctx, apps[i].Id)
		}
	}
	return apps, nil
}

func (s *BucketAppStore) GetAppByID(ctx context.Context, id string) (config.AppConfig, error) {
	app, err := config.GetAppConfig(id)
	if err != nil {
		return config.AppConfig{}, fmt.Errorf("app not found: %w", err)
	}
	if app == nil {
		return config.AppConfig{}, fmt.Errorf("app not found")
	}
	// No name enrichment here: this sits on the device-facing OTA hot path and
	// must never block on an Expo round-trip.
	return *app, nil
}

func (s *BucketAppStore) InsertApp(ctx context.Context, app InsertAppParameters) (string, error) {
	return "", ErrNotSupportedInStatelessMode
}

func (s *BucketAppStore) DeleteAppByID(ctx context.Context, id string) error {
	return ErrNotSupportedInStatelessMode
}

func (s *BucketAppStore) UpdateAppNameByID(ctx context.Context, id string, newName string) error {
	return ErrNotSupportedInStatelessMode
}
