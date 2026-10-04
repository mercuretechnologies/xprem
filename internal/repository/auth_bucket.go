package repository

import (
	"context"
	"fmt"
	"xprem/internal/providers/expo"
	"xprem/internal/types"
)

type BucketAuthRepository struct{}

func NewBucketAuthRepository() *BucketAuthRepository {
	return &BucketAuthRepository{}
}

// ValidateCliCredential returns 0 as the key id: stateless mode has no API
// key rows, so there is no per-key identity to enforce restrictions on.
func (s *BucketAuthRepository) ValidateCliCredential(ctx context.Context, appId string, auth types.Auth) (int64, error) {
	// expo.ValidateAuth checks the caller's Expo session against appId; without
	// it, any authenticated Expo user could act on any app.
	expoAccount, err := expo.ValidateAuth(appId, auth)
	if err != nil || expoAccount == nil {
		return 0, fmt.Errorf("Error validating expo auth: %w", err)
	}
	return 0, nil
}

func (s *BucketAuthRepository) InsertApiKey(ctx context.Context, appId string, name string, hint string, hashedKey string) (int64, error) {
	return 0, ErrNotSupportedInStatelessMode
}

func (s *BucketAuthRepository) GetApiKeysMetadataByAppID(ctx context.Context, appId string) ([]types.ApiKeyMetadata, error) {
	return []types.ApiKeyMetadata{}, ErrNotSupportedInStatelessMode
}

func (s *BucketAuthRepository) GetApiKeyNameByID(ctx context.Context, appId string, apiKeyId int64) (string, error) {
	return "", ErrNotSupportedInStatelessMode
}

func (s *BucketAuthRepository) RevokeApiKeyByID(ctx context.Context, apiKeyId int64, appId string) (string, error) {
	return "", ErrNotSupportedInStatelessMode
}
