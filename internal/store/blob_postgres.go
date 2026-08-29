package store

import (
	"context"
	"fmt"
	"xprem/internal/database"
	"xprem/internal/database/postgres/pgdb"
)

type PostgresBlobStore struct {
	engine *database.Engine
}

func NewPostgresBlobStore(engine *database.Engine) *PostgresBlobStore {
	return &PostgresBlobStore{
		engine: engine,
	}
}

func (s *PostgresBlobStore) FilterExistingHashes(ctx context.Context, appId string, hashes []string) ([]string, error) {
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
