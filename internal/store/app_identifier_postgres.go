package store

import (
	"context"
	"errors"
	"fmt"
	"time"
	"xprem/internal/database"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/types"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// AppIdentifierRow is one store identity of an app with its credential state,
// as listed in the dashboard.
type AppIdentifierRow struct {
	Id                    string
	Platform              types.Platform
	Identifier            string
	BuildNumber           string
	HasAndroidCredentials bool
	HasIosCredentials     bool
	CreatedAt             time.Time
}

// AppIdentifierRef is the resolution of an identifier id within an app.
type AppIdentifierRef struct {
	// PreviousBuildNumber is populated only by an allocation, under the row lock.
	PreviousBuildNumber string
	Id                  string
	Platform            types.Platform
	Identifier          string
	BuildNumber         string
}

type PostgresAppIdentifierStore struct {
	engine *database.Engine
}

func NewPostgresAppIdentifierStore(engine *database.Engine) *PostgresAppIdentifierStore {
	return &PostgresAppIdentifierStore{
		engine: engine,
	}
}

func (s *PostgresAppIdentifierStore) InsertAppIdentifier(ctx context.Context, appId string, platform types.Platform, identifier string) (string, error) {
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

// GetAppIdentifiers lists app identifiers with their platform signing-credential readiness.
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
			HasIosCredentials:     row.HasIosCredentials,
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

func (s *PostgresAppIdentifierStore) GetAppIdentifierByPlatformAndIdentifier(ctx context.Context, appId string, platform types.Platform, identifier string) (*AppIdentifierRef, error) {
	row, err := s.engine.Queries.GetAppIdentifierByPlatformAndIdentifier(ctx, pgdb.GetAppIdentifierByPlatformAndIdentifierParams{
		AppID:      ToPgUUID(appId),
		Platform:   platform,
		Identifier: identifier,
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

func (s *PostgresAppIdentifierStore) SetBuildNumber(ctx context.Context, appId string, identifierId string, buildNumber string) error {
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
		_, err := q.LockAppIdentifierByID(ctx, pgdb.LockAppIdentifierByIDParams{
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
			return &ErrResourceNotFound{Resource: "app identifier", Identifier: identifierId}
		}
		return nil
	})
}

// AllocateBuildNumber holds the identifier row lock through the read and write,
// serializing both allocations and manual counter corrections.
func (s *PostgresAppIdentifierStore) AllocateBuildNumber(ctx context.Context, appId string, identifierId string, next func(types.Platform, string) (string, error)) (*AppIdentifierRef, error) {
	var allocated *AppIdentifierRef
	err := s.engine.WithTx(ctx, func(q *pgdb.Queries) error {
		row, err := q.LockAppIdentifierByID(ctx, pgdb.LockAppIdentifierByIDParams{AppID: ToPgUUID(appId), ID: ToPgUUID(identifierId)})
		if errors.Is(err, pgx.ErrNoRows) {
			return &ErrResourceNotFound{Resource: "app identifier", Identifier: identifierId}
		}
		if err != nil {
			return fmt.Errorf("failed to lock app identifier: %w", err)
		}
		platform := row.Platform
		buildNumber, err := next(platform, row.BuildNumber)
		if err != nil {
			return err
		}
		_, err = q.SetAppIdentifierBuildNumber(ctx, pgdb.SetAppIdentifierBuildNumberParams{AppID: ToPgUUID(appId), ID: ToPgUUID(identifierId), BuildNumber: buildNumber})
		if err != nil {
			return fmt.Errorf("failed to allocate build number: %w", err)
		}
		allocated = &AppIdentifierRef{Id: row.ID.String(), Platform: platform, Identifier: row.Identifier, BuildNumber: buildNumber, PreviousBuildNumber: row.BuildNumber}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return allocated, nil
}
