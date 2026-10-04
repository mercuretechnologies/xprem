package repository

import (
	"context"
	"errors"
	"fmt"
	"time"
	"xprem/internal/database"
	"xprem/internal/database/postgres/pgdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type EnvironmentRow struct {
	Id        string
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// EnvVarRow is one env entry as listed in the dashboard: never the value.
type EnvVarRow struct {
	EnvironmentId string
	Key           string
	IsPublic      bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type PostgresEnvironmentRepository struct {
	engine *database.Engine
}

func NewPostgresEnvironmentRepository(engine *database.Engine) *PostgresEnvironmentRepository {
	return &PostgresEnvironmentRepository{
		engine: engine,
	}
}

func (s *PostgresEnvironmentRepository) InsertEnvironment(ctx context.Context, appId string, name string) (string, error) {
	id := uuid.NewString()
	_, err := s.engine.Queries.InsertEnvironment(ctx, pgdb.InsertEnvironmentParams{
		ID:    ToPgUUID(id),
		AppID: ToPgUUID(appId),
		Name:  name,
	})
	if err != nil {
		if database.IsUniqueViolation(err) {
			return "", &ErrResourceAlreadyExists{Resource: "environment", Identifier: fmt.Sprintf("%s (appId: %s)", name, appId)}
		}
		return "", fmt.Errorf("failed to create environment in database: %w", err)
	}
	return id, nil
}

func (s *PostgresEnvironmentRepository) ListEnvironments(ctx context.Context, appId string) ([]EnvironmentRow, error) {
	rows, err := s.engine.Queries.ListEnvironmentsByAppID(ctx, ToPgUUID(appId))
	if err != nil {
		return nil, fmt.Errorf("failed to list environments from database: %w", err)
	}
	environments := make([]EnvironmentRow, len(rows))
	for i, row := range rows {
		environments[i] = EnvironmentRow{
			Id:        row.ID.String(),
			Name:      row.Name,
			CreatedAt: row.CreatedAt.Time,
			UpdatedAt: row.UpdatedAt.Time,
		}
	}
	return environments, nil
}

// GetEnvironmentIdByName returns ErrResourceNotFound for an unknown name.
func (s *PostgresEnvironmentRepository) GetEnvironmentIdByName(ctx context.Context, appId string, name string) (string, error) {
	id, err := s.engine.Queries.GetEnvironmentIDByName(ctx, pgdb.GetEnvironmentIDByNameParams{
		AppID: ToPgUUID(appId),
		Name:  name,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", &ErrResourceNotFound{Resource: "environment", Identifier: name}
		}
		return "", fmt.Errorf("failed to retrieve environment from database: %w", err)
	}
	return id.String(), nil
}

func (s *PostgresEnvironmentRepository) DeleteEnvironment(ctx context.Context, appId string, name string) error {
	commandTag, err := s.engine.Queries.DeleteEnvironment(ctx, pgdb.DeleteEnvironmentParams{
		AppID: ToPgUUID(appId),
		Name:  name,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.ConstraintName == "fk_channels_environment" {
			return &ErrEnvironmentHasChannels{EnvironmentName: name}
		}
		return fmt.Errorf("failed to delete environment from database: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		return &ErrResourceNotFound{Resource: "environment", Identifier: name}
	}
	return nil
}

func (s *PostgresEnvironmentRepository) UpsertEnvVar(ctx context.Context, environmentId string, key string, isPublic bool, sealedValue string) error {
	_, err := s.engine.Queries.UpsertEnvironmentVar(ctx, pgdb.UpsertEnvironmentVarParams{
		ID:            ToPgUUID(uuid.NewString()),
		EnvironmentID: ToPgUUID(environmentId),
		Key:           key,
		IsPublic:      isPublic,
		SealedValue:   sealedValue,
	})
	if err != nil {
		// The environment was deleted between the name lookup and this write.
		if database.IsForeignKeyViolation(err) {
			return &ErrResourceNotFound{Resource: "environment", Identifier: environmentId}
		}
		return fmt.Errorf("failed to save env var in database: %w", err)
	}
	return nil
}

func (s *PostgresEnvironmentRepository) ListEnvVars(ctx context.Context, appId string) ([]EnvVarRow, error) {
	rows, err := s.engine.Queries.ListEnvironmentVarsByAppID(ctx, ToPgUUID(appId))
	if err != nil {
		return nil, fmt.Errorf("failed to list env vars from database: %w", err)
	}
	envVars := make([]EnvVarRow, len(rows))
	for i, row := range rows {
		envVars[i] = EnvVarRow{
			EnvironmentId: row.EnvironmentID.String(),
			Key:           row.Key,
			IsPublic:      row.IsPublic,
			CreatedAt:     row.CreatedAt.Time,
			UpdatedAt:     row.UpdatedAt.Time,
		}
	}
	return envVars, nil
}

// GetSealedValue returns (nil, nil) when the entry does not exist in the environment.
func (s *PostgresEnvironmentRepository) GetSealedValue(ctx context.Context, environmentId string, key string) (*string, error) {
	sealedValue, err := s.engine.Queries.GetSealedEnvironmentVarValue(ctx, pgdb.GetSealedEnvironmentVarValueParams{
		EnvironmentID: ToPgUUID(environmentId),
		Key:           key,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to retrieve env var from database: %w", err)
	}
	return &sealedValue, nil
}

func (s *PostgresEnvironmentRepository) DeleteEnvVar(ctx context.Context, environmentId string, key string) error {
	commandTag, err := s.engine.Queries.DeleteEnvironmentVar(ctx, pgdb.DeleteEnvironmentVarParams{
		EnvironmentID: ToPgUUID(environmentId),
		Key:           key,
	})
	if err != nil {
		return fmt.Errorf("failed to delete env var from database: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		return &ErrResourceNotFound{Resource: "env var", Identifier: key}
	}
	return nil
}

// SetChannelEnvironment points the channel at environmentId; nil unbinds it.
func (s *PostgresEnvironmentRepository) SetChannelEnvironment(ctx context.Context, appId string, channelName string, environmentId *string) error {
	commandTag, err := s.engine.Queries.UpdateChannelEnvironment(ctx, pgdb.UpdateChannelEnvironmentParams{
		AppID:         ToPgUUID(appId),
		Name:          channelName,
		EnvironmentID: ToPgUUIDPtr(environmentId),
	})
	if err != nil {
		return fmt.Errorf("failed to update channel environment in database: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		return &ErrResourceNotFound{Resource: "channel", Identifier: fmt.Sprintf("%s (appId: %s)", channelName, appId)}
	}
	return nil
}

// SealedEnvVar contains encrypted values for an authorized environment export.
type SealedEnvVar struct {
	Key         string
	IsPublic    bool
	SealedValue string
}

// ResolvedEnvironment distinguishes an unbound channel (nil name) from an
// existing empty environment; unknown selectors return ErrResourceNotFound.
type ResolvedEnvironment struct {
	ID        string
	Name      *string
	Variables []SealedEnvVar
}

func (s *PostgresEnvironmentRepository) ResolveEnvironmentVariables(ctx context.Context, appID, channel, environment string) (*ResolvedEnvironment, error) {
	rows, err := s.engine.Queries.ResolveEnvironmentVariables(ctx, pgdb.ResolveEnvironmentVariablesParams{AppID: ToPgUUID(appID), ChannelName: channel, EnvironmentName: environment})
	if err != nil {
		return nil, fmt.Errorf("failed to resolve environment values from database: %w", err)
	}
	if len(rows) == 0 {
		if channel != "" {
			return nil, &ErrResourceNotFound{Resource: "channel", Identifier: channel}
		}
		return nil, &ErrResourceNotFound{Resource: "environment", Identifier: environment}
	}
	resolved := &ResolvedEnvironment{ID: rows[0].EnvironmentID.String(), Name: rows[0].EnvironmentName, Variables: []SealedEnvVar{}}
	for _, row := range rows {
		if row.Key != nil {
			resolved.Variables = append(resolved.Variables, SealedEnvVar{Key: *row.Key, IsPublic: *row.IsPublic, SealedValue: *row.SealedValue})
		}
	}
	return resolved, nil
}
