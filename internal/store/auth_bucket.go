package store

import (
	"context"
	"fmt"
	"xprem/internal/bucket"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/providers/expo"
	"xprem/internal/types"
)

type BucketAuthStore struct {
	bucket bucket.Bucket
}

func NewBucketAuthStore(bucket bucket.Bucket) *BucketAuthStore {
	return &BucketAuthStore{
		bucket: bucket,
	}
}

// ValidateCliCredential returns 0 as the key id: stateless mode has no API
// key rows, so there is no per-key identity to enforce restrictions on.
func (s *BucketAuthStore) ValidateCliCredential(ctx context.Context, appId string, auth types.Auth) (int64, error) {
	// expo.ValidateAuth checks the caller's Expo session against appId; without
	// it, any authenticated Expo user could act on any app.
	expoAccount, err := expo.ValidateAuth(appId, auth)
	if err != nil || expoAccount == nil {
		return 0, fmt.Errorf("Error validating expo auth: %w", err)
	}
	return 0, nil
}

func (s *BucketAuthStore) InsertApiKey(ctx context.Context, appId string, name string, hint string, hashedKey string) (int64, error) {
	return 0, ErrNotSupportedInStatelessMode
}

func (s *BucketAuthStore) GetApiKeysMetadataByAppID(ctx context.Context, appId string) ([]pgdb.GetApiKeysMetadataByAppIDRow, error) {
	return []pgdb.GetApiKeysMetadataByAppIDRow{}, ErrNotSupportedInStatelessMode
}

func (s *BucketAuthStore) GetApiKeyNameByID(ctx context.Context, appId string, apiKeyId int64) (string, error) {
	return "", ErrNotSupportedInStatelessMode
}

func (s *BucketAuthStore) RevokeApiKeyByID(ctx context.Context, apiKeyId int64, appId string) (string, error) {
	return "", ErrNotSupportedInStatelessMode
}
