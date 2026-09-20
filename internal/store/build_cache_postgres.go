package store

import (
	"context"
	"errors"
	"xprem/internal/database"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/types"

	"github.com/jackc/pgx/v5"
)

var ErrBuildCacheFull = errors.New("build cache storage limit reached")

type PostgresBuildCacheStore struct{ engine *database.Engine }

func NewPostgresBuildCacheStore(engine *database.Engine) *PostgresBuildCacheStore {
	return &PostgresBuildCacheStore{engine: engine}
}

func cacheObject(row pgdb.BuildCacheObject, err error) (*types.BuildCacheObject, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, &ErrResourceNotFound{Resource: "build cache", Identifier: "requested object"}
	}
	if err != nil {
		return nil, err
	}
	object := &types.BuildCacheObject{ID: row.ID.String(), AppID: row.AppID.String(), AppIdentifierID: row.AppIdentifierID.String(), Namespace: row.Namespace, CacheKey: row.CacheKey, Size: row.Size, SHA256: row.Sha256, CreatedAt: row.CreatedAt.Time, ExpiresAt: row.ExpiresAt.Time}
	if row.PublishedAt.Valid {
		object.PublishedAt = &row.PublishedAt.Time
	}
	return object, nil
}

func (s *PostgresBuildCacheStore) Reserve(ctx context.Context, object types.BuildCacheObject) (*types.BuildCacheObject, error) {
	var saved *types.BuildCacheObject
	err := s.engine.WithTx(ctx, func(q *pgdb.Queries) error {
		if _, err := q.LockBuildCacheOwner(ctx, pgdb.LockBuildCacheOwnerParams{AppID: ToPgUUID(object.AppID), ID: ToPgUUID(object.AppIdentifierID)}); err != nil {
			return err
		}
		used, err := q.BuildCacheUsage(ctx, pgdb.BuildCacheUsageParams{AppID: ToPgUUID(object.AppID), AppIdentifierID: ToPgUUID(object.AppIdentifierID)})
		if err != nil {
			return err
		}
		if used.Bytes+object.Size > types.MaxBuildCacheBytes || used.Objects >= types.MaxBuildCacheObjects {
			return ErrBuildCacheFull
		}
		saved, err = cacheObject(q.InsertBuildCacheObject(ctx, pgdb.InsertBuildCacheObjectParams{ID: ToPgUUID(object.ID), AppID: ToPgUUID(object.AppID), AppIdentifierID: ToPgUUID(object.AppIdentifierID), Namespace: object.Namespace, CacheKey: object.CacheKey, Size: object.Size, Sha256: object.SHA256}))
		return err
	})
	return saved, err
}

func (s *PostgresBuildCacheStore) Get(ctx context.Context, appID, identifierID, id string) (*types.BuildCacheObject, error) {
	return cacheObject(s.engine.GetBuildCacheUpload(ctx, pgdb.GetBuildCacheUploadParams{AppID: ToPgUUID(appID), AppIdentifierID: ToPgUUID(identifierID), ID: ToPgUUID(id)}))
}

func (s *PostgresBuildCacheStore) Find(ctx context.Context, appID, identifierID string, namespace types.BuildCacheNamespace, key string) (*types.BuildCacheObject, error) {
	return cacheObject(s.engine.FindBuildCacheObject(ctx, pgdb.FindBuildCacheObjectParams{AppID: ToPgUUID(appID), AppIdentifierID: ToPgUUID(identifierID), Namespace: namespace, CacheKey: key}))
}

func (s *PostgresBuildCacheStore) Publish(ctx context.Context, object types.BuildCacheObject) (*types.BuildCacheObject, error) {
	var saved *types.BuildCacheObject
	err := s.engine.WithTx(ctx, func(q *pgdb.Queries) error {
		app, identifier, id := ToPgUUID(object.AppID), ToPgUUID(object.AppIdentifierID), ToPgUUID(object.ID)
		if _, err := q.LockBuildCacheOwner(ctx, pgdb.LockBuildCacheOwnerParams{AppID: app, ID: identifier}); err != nil {
			return err
		}
		var err error
		saved, err = cacheObject(q.GetBuildCacheUpload(ctx, pgdb.GetBuildCacheUploadParams{AppID: app, AppIdentifierID: identifier, ID: id}))
		if err != nil || saved.PublishedAt != nil {
			return err
		}
		// Replace the previous object atomically; its bytes remain available
		// for in-flight downloads until the cleanup outbox deletes them.
		if err = q.RemovePreviousBuildCacheObject(ctx, pgdb.RemovePreviousBuildCacheObjectParams{AppID: app, AppIdentifierID: identifier, Namespace: saved.Namespace, CacheKey: saved.CacheKey, ID: id}); err != nil {
			return err
		}
		saved, err = cacheObject(q.PublishBuildCacheObject(ctx, pgdb.PublishBuildCacheObjectParams{AppID: app, AppIdentifierID: identifier, ID: id}))
		return err
	})
	return saved, err
}

func (s *PostgresBuildCacheStore) Delete(ctx context.Context, object types.BuildCacheObject) error {
	return s.engine.DeleteBuildCacheObject(ctx, pgdb.DeleteBuildCacheObjectParams{AppID: ToPgUUID(object.AppID), AppIdentifierID: ToPgUUID(object.AppIdentifierID), ID: ToPgUUID(object.ID)})
}
