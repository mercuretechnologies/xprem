package repository

import (
	"context"
	"fmt"
	"xprem/internal/database"
	"xprem/internal/database/postgres/pgdb"
)

type PostgresBlobRepository struct {
	engine *database.Engine
}

func NewPostgresBlobRepository(engine *database.Engine) *PostgresBlobRepository {
	return &PostgresBlobRepository{
		engine: engine,
	}
}

func (s *PostgresBlobRepository) FilterExistingHashes(ctx context.Context, appId string, hashes []string) ([]string, error) {
	blobs, err := s.engine.Queries.GetBlobsByHashes(ctx, pgdb.GetBlobsByHashesParams{
		AppID:  ToPgUUID(appId),
		Hashes: hashes,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to look up existing blobs: %w", err)
	}
	var existingHashes []string
	for _, blob := range blobs {
		existingHashes = append(existingHashes, blob.Hash)
	}
	return existingHashes, nil
}
