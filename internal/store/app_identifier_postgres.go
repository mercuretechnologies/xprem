package store

import (
	"context"
	"errors"
	"fmt"
	"time"
	"xprem/internal/database"
	"xprem/internal/database/postgres/pgdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// AppIdentifierRow is one store identity of an app with its credential state,
// as listed in the dashboard.
type AppIdentifierRow struct {
	Id                    string
	Platform              string
	Identifier            string
	BuildNumber           int64
	HasAndroidCredentials bool
	CreatedAt             time.Time
}

// AppIdentifierRef is the resolution of an identifier id within an app.
type AppIdentifierRef struct {
	Id          string
	Platform    string
	Identifier  string
	BuildNumber int64
}

type PostgresAppIdentifierStore struct {
	engine *database.Engine
}

func NewPostgresAppIdentifierStore(engine *database.Engine) *PostgresAppIdentifierStore {
	return &PostgresAppIdentifierStore{
		engine: engine,
	}
}

func (s *PostgresAppIdentifierStore) InsertAppIdentifier(ctx context.Context, appId string, platform string, identifier string) (string, error) {
	id := uuid.NewString()
	_, err := s.engine.Queries.InsertAppIdentifier(ctx, pgdb.InsertAppIdentifierParams{
		ID:         ToPgUUID(id),
		AppID:      ToPgUUID(appId),
		Platform:   platform,
		Identifier: identifier,
	})
	if err != nil {
		if database.IsUniqueViolation(err) {
			return "", &ErrResourceAlreadyExists{Resource: "app identifier", Identifier: fmt.Sprintf("%s/%s (appId: %s)", platform, identifier, appId)}
		}
		return "", fmt.Errorf("failed to create app identifier in database: %w", err)
	}
	return id, nil
}

func (s *PostgresAppIdentifierStore) GetAppIdentifiers(ctx context.Context, appId string) ([]AppIdentifierRow, error) {
	rows, err := s.engine.Queries.GetAppIdentifiersByAppID(ctx, ToPgUUID(appId))
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve app identifiers from database: %w", err)
	}
	identifiers := make([]AppIdentifierRow, len(rows))
	for i, row := range rows {
		identifiers[i] = AppIdentifierRow{
			Id:                    row.ID.String(),
			Platform:              row.Platform,
			Identifier:            row.Identifier,
			BuildNumber:           row.BuildNumber,
			HasAndroidCredentials: row.HasAndroidCredentials,
			CreatedAt:             row.CreatedAt.Time,
		}
	}
	return identifiers, nil
}

func (s *PostgresAppIdentifierStore) GetAppIdentifierByID(ctx context.Context, appId string, identifierId string) (*AppIdentifierRef, error) {
	row, err := s.engine.Queries.GetAppIdentifierByID(ctx, pgdb.GetAppIdentifierByIDParams{
		AppID: ToPgUUID(appId),
		ID:    ToPgUUID(identifierId),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to retrieve app identifier from database: %w", err)
	}
	return &AppIdentifierRef{
		Id:          row.ID.String(),
		Platform:    row.Platform,
		Identifier:  row.Identifier,
		BuildNumber: row.BuildNumber,
	}, nil
}

func (s *PostgresAppIdentifierStore) SetBuildNumber(ctx context.Context, appId string, identifierId string, buildNumber int64) error {
	commandTag, err := s.engine.Queries.SetAppIdentifierBuildNumber(ctx, pgdb.SetAppIdentifierBuildNumberParams{
		AppID:       ToPgUUID(appId),
		ID:          ToPgUUID(identifierId),
		BuildNumber: buildNumber,
	})
	if err != nil {
		return fmt.Errorf("failed to set build number in database: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		return &ErrResourceNotFound{Resource: "app identifier", Identifier: identifierId}
	}
	return nil
}

func (s *PostgresAppIdentifierStore) DeleteAppIdentifier(ctx context.Context, appId string, identifierId string) error {
	return s.engine.WithTx(ctx, func(q *pgdb.Queries) error {
		identifier, err := q.LockAppIdentifierByID(ctx, pgdb.LockAppIdentifierByIDParams{
			AppID: ToPgUUID(appId), ID: ToPgUUID(identifierId),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return &ErrResourceNotFound{Resource: "app identifier", Identifier: identifierId}
		}
		if err != nil {
			return fmt.Errorf("failed to lock app identifier: %w", err)
		}
		commandTag, err := q.DeleteAppIdentifierByID(ctx, pgdb.DeleteAppIdentifierByIDParams{
			AppID: ToPgUUID(appId),
			ID:    ToPgUUID(identifierId),
		})
		if err != nil {
			return fmt.Errorf("failed to delete app identifier from database: %w", err)
		}
		if commandTag.RowsAffected() == 0 {
			// The locked row exists, so only the credentials guard can reject it.
			return &ErrIdentifierHasCredentials{Identifier: identifier}
		}
		return nil
	})
}
