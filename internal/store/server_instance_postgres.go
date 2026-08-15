package store

import (
	"context"
	"errors"
	"fmt"
	"xprem/internal/database"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// PostgresServerInstanceStore is the server_instance singleton: the
// deployment's permanent UUID, minted on first boot and immutable after.
type PostgresServerInstanceStore struct {
	engine *database.Engine
}

func NewPostgresServerInstanceStore(engine *database.Engine) *PostgresServerInstanceStore {
	return &PostgresServerInstanceStore{engine: engine}
}

// GetOrCreateInstanceID returns the persisted id, minting one first if the
// table is empty. A valid UUID seed is adopted instead of minting, so a
// deployment moving from stateless to DB mode keeps its bucket-minted id.
func (s *PostgresServerInstanceStore) GetOrCreateInstanceID(ctx context.Context, seed string) (string, error) {
	existing, err := s.engine.Queries.GetServerInstanceID(ctx)
	if err == nil {
		return existing.String(), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("read server instance id: %w", err)
	}
	candidate := seed
	if _, parseErr := uuid.Parse(candidate); parseErr != nil {
		candidate = uuid.New().String()
	}
	minted, err := s.engine.Queries.InsertServerInstance(ctx, ToPgUUID(candidate))
	if err == nil {
		return minted.String(), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("mint server instance id: %w", err)
	}
	// ErrNoRows here means another replica won the first-boot insert race.
	existing, err = s.engine.Queries.GetServerInstanceID(ctx)
	if err != nil {
		return "", fmt.Errorf("read server instance id after lost race: %w", err)
	}
	return existing.String(), nil
}
