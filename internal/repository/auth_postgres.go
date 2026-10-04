package repository

import (
	"context"
	"fmt"
	"strconv"
	"time"
	"xprem/internal/crypto"
	"xprem/internal/database"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/types"
)

type PostgresAuthRepository struct {
	engine *database.Engine
}

func NewPostgresAuthRepository(engine *database.Engine) *PostgresAuthRepository {
	return &PostgresAuthRepository{
		engine: engine,
	}
}

func (s *PostgresAuthRepository) ValidateCliCredential(ctx context.Context, appId string, auth types.Auth) (int64, error) {
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

func (s *PostgresAuthRepository) InsertApiKey(ctx context.Context, appId string, name string, hint string, hashedKey string) (int64, error) {
	pgAppID := ToPgUUID(appId)
	return s.engine.Queries.InsertApiKey(ctx, pgdb.InsertApiKeyParams{
		AppID:     pgAppID,
		Name:      name,
		Hint:      hint,
		HashedKey: hashedKey,
	})
}

func (s *PostgresAuthRepository) GetApiKeysMetadataByAppID(ctx context.Context, appId string) ([]types.ApiKeyMetadata, error) {
	rows, err := s.engine.Queries.GetApiKeysMetadataByAppID(ctx, ToPgUUID(appId))
	if err != nil {
		return nil, err
	}
	apiKeysMetadata := make([]types.ApiKeyMetadata, len(rows))
	for i, row := range rows {
		var lastUsedAt *string
		if row.LastUsedAt.Valid {
			formatted := row.LastUsedAt.Time.Format(time.RFC3339)
			lastUsedAt = &formatted
		}
		apiKeysMetadata[i] = types.ApiKeyMetadata{
			ID:         strconv.FormatInt(row.ID, 10),
			Name:       row.Name,
			Hint:       row.Hint,
			CreatedAt:  row.CreatedAt.Time.Format(time.RFC3339),
			LastUsedAt: lastUsedAt,
		}
	}
	return apiKeysMetadata, nil
}

func (s *PostgresAuthRepository) RevokeApiKeyByID(ctx context.Context, apiKeyId int64, appId string) (string, error) {
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

func (s *PostgresAuthRepository) GetApiKeyNameByID(ctx context.Context, appId string, apiKeyId int64) (string, error) {
	return s.engine.Queries.GetApiKeyNameByID(ctx, pgdb.GetApiKeyNameByIDParams{
		ID:    apiKeyId,
		AppID: ToPgUUID(appId),
	})
}
