package store

import (
	"context"
	"fmt"
	"time"
	"xprem/config"
	"xprem/internal/database"
	"xprem/internal/database/postgres/pgdb"
)

type PostgresAppStore struct {
	engine *database.Engine
}

func NewPostgresAppStore(engine *database.Engine) *PostgresAppStore {
	return &PostgresAppStore{
		engine: engine,
	}
}

type InsertAppParameters struct {
	ID                 string
	Name               string
	KeysMode           *string
	SealedPublicKey    *string
	SealedPrivateKey   *string
	PathPublicKey      *string
	PathPrivateKey     *string
	AwsSecretIDPublic  *string
	AwsSecretIDPrivate *string
}

func (s *PostgresAppStore) InsertApp(ctx context.Context, app InsertAppParameters) (string, error) {
	appID := ToPgUUID(app.ID)
	params := pgdb.InsertAppParams{
		ID:                 appID,
		Name:               app.Name,
		KeysMode:           app.KeysMode,
		SealedPublicKey:    app.SealedPublicKey,
		SealedPrivateKey:   app.SealedPrivateKey,
		PathPublicKey:      app.PathPublicKey,
		PathPrivateKey:     app.PathPrivateKey,
		AwsSecretIDPublic:  app.AwsSecretIDPublic,
		AwsSecretIDPrivate: app.AwsSecretIDPrivate,
	}
	insertedAppId, err := s.engine.Queries.InsertApp(ctx, params)
	if err != nil {
		if database.IsUniqueViolation(err) {
			return "", &ErrResourceAlreadyExists{Resource: "app", Identifier: app.ID}
		}
		return "", err
	}
	return insertedAppId.String(), nil
}

func (s *PostgresAppStore) DeleteAppByID(ctx context.Context, id string) error {
	pgAppID := ToPgUUID(id)
	commandTag, err := s.engine.Queries.DeleteAppByID(ctx, pgAppID)
	if err != nil {
		return fmt.Errorf("failed to delete app from database: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		return &ErrResourceNotFound{Resource: "app", Identifier: id}
	}
	return nil
}

func (s *PostgresAppStore) GetApps(ctx context.Context) ([]config.AppDescriptor, error) {
	rows, err := s.engine.Queries.GetApps(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve apps from database: %w", err)
	}
	apps := make([]config.AppDescriptor, len(rows))
	for i, row := range rows {
		apps[i] = config.AppDescriptor{
			Id:   row.ID.String(),
			Name: row.Name,
		}
	}
	return apps, nil
}

func (s *PostgresAppStore) UpdateAppNameByID(ctx context.Context, id string, newName string) error {
	pgAppID := ToPgUUID(id)
	commandTag, err := s.engine.Queries.UpdateAppNameByID(ctx, pgdb.UpdateAppNameByIDParams{
		Name: newName,
		ID:   pgAppID,
	})
	if err != nil {
		return fmt.Errorf("failed to update app name in database: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		return &ErrResourceNotFound{Resource: "app", Identifier: id}
	}
	return nil
}

func (s *PostgresAppStore) UpdateAppGitURLByID(ctx context.Context, id string, gitURL string) error {
	pgAppID := ToPgUUID(id)
	commandTag, err := s.engine.Queries.UpdateAppGitURLByID(ctx, pgdb.UpdateAppGitURLByIDParams{
		GitUrl: gitURL,
		ID:     pgAppID,
	})
	if err != nil {
		return fmt.Errorf("failed to update app git URL in database: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		return &ErrResourceNotFound{Resource: "app", Identifier: id}
	}
	return nil
}

func safeStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (s *PostgresAppStore) GetAppByID(ctx context.Context, id string) (config.AppConfig, error) {
	pgAppID := ToPgUUID(id)
	row, err := s.engine.Queries.GetAppByID(ctx, pgAppID)
	if err != nil {
		return config.AppConfig{}, fmt.Errorf("failed to retrieve app from database: %w", err)
	}
	return config.AppConfig{
		Id:     row.ID.String(),
		Name:   row.Name,
		GitURL: safeStr(row.GitUrl),
		Keys: config.KeysConfig{
			Mode: func() config.KeysMode {
				if row.KeysMode != nil {
					return config.KeysMode(*row.KeysMode)
				}
				return "" // or your default fallback string mode
			}(),
			SealedPublicKey:  safeStr(row.SealedPublicKey),
			SealedPrivateKey: safeStr(row.SealedPrivateKey),
			PublicPath:       safeStr(row.PathPublicKey),
			PrivatePath:      safeStr(row.PathPrivateKey),
			PublicSecretId:   safeStr(row.AwsSecretIDPublic),
			PrivateSecretId:  safeStr(row.AwsSecretIDPrivate),
		},
		CreatedAt: time.Duration(row.CreatedAt.Time.UnixMilli()),
	}, nil
}
