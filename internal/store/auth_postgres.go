package store

import (
	"context"
	"fmt"
	"xprem/internal/crypto"
	"xprem/internal/database"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/types"
)

type PostgresAuthStore struct {
	engine *database.Engine
}

func NewPostgresAuthStore(engine *database.Engine) *PostgresAuthStore {
	return &PostgresAuthStore{
		engine: engine,
	}
}

func (s *PostgresAuthStore) ValidateCliCredential(ctx context.Context, appId string, auth types.Auth) (int64, error) {
	pgAppID := ToPgUUID(appId)
	token := auth.Token
	if token == nil {
		return 0, fmt.Errorf("no token provided in auth")
	}
	hashedToken, err := crypto.HashPlaintextAPIKey(*token)
	if err != nil {
		return 0, fmt.Errorf("failed to hash API key: %w", err)
	}
	apiKeyID, err := s.engine.Queries.ValidateAndTouchAuth(ctx, pgdb.ValidateAndTouchAuthParams{
		AppID:     pgAppID,
		HashedKey: hashedToken,
	})
	if err != nil {
		if database.IsNoRows(err) {
			return 0, fmt.Errorf("invalid API key")
		}
		return 0, err
	}
	return apiKeyID, nil
}

func (s *PostgresAuthStore) InsertApiKey(ctx context.Context, appId string, name string, hint string, hashedKey string) (int64, error) {
	pgAppID := ToPgUUID(appId)
	return s.engine.Queries.InsertApiKey(ctx, pgdb.InsertApiKeyParams{
		AppID:     pgAppID,
		Name:      name,
		Hint:      hint,
		HashedKey: hashedKey,
	})
}

func (s *PostgresAuthStore) GetApiKeysMetadataByAppID(ctx context.Context, appId string) ([]pgdb.GetApiKeysMetadataByAppIDRow, error) {
	pgAppID := ToPgUUID(appId)
	return s.engine.Queries.GetApiKeysMetadataByAppID(ctx, pgAppID)
}

func (s *PostgresAuthStore) RevokeApiKeyByID(ctx context.Context, apiKeyId int64, appId string) (string, error) {
	pgAppID := ToPgUUID(appId)
	name, err := s.engine.Queries.RevokeApiKeyByID(ctx, pgdb.RevokeApiKeyByIDParams{
		ID:    apiKeyId,
		AppID: pgAppID,
	})
	if err != nil {
		// RETURNING turns the old 0-rows outcome into no-rows: same not-found.
		if database.IsNoRows(err) {
			return "", &ErrResourceNotFound{
				Resource:   "api_key",
				Identifier: fmt.Sprintf("id: %d, appId: %s", apiKeyId, appId),
			}
		}
		return "", fmt.Errorf("failed to execute revoke query: %w", err)
	}
	return name, nil
}

func (s *PostgresAuthStore) GetApiKeyNameByID(ctx context.Context, appId string, apiKeyId int64) (string, error) {
	return s.engine.Queries.GetApiKeyNameByID(ctx, pgdb.GetApiKeyNameByIDParams{
		ID:    apiKeyId,
		AppID: ToPgUUID(appId),
	})
}
