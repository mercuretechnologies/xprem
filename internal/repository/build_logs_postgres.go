package repository

import (
	"context"
	"errors"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/types"

	"github.com/jackc/pgx/v5"
)

var ErrBuildLogOffset = errors.New("build log offset conflicts with stored output")

func (s *PostgresBuildRepository) AppendLogs(ctx context.Context, appID, id string, offset int32, content string) error {
	// Serialize appends for one build, including retries after an uncertain response.
	return s.engine.WithTx(ctx, func(q *pgdb.Queries) error {
		if _, err := q.LockBuild(ctx, pgdb.LockBuildParams{AppID: ToPgUUID(appID), ID: ToPgUUID(id)}); err != nil {
			return buildNotFound(err)
		}
		end, err := q.BuildLogEndOffset(ctx, ToPgUUID(id))
		if err != nil {
			return err
		}
		if offset < end {
			stored, err := q.GetBuildLogChunk(ctx, pgdb.GetBuildLogChunkParams{BuildID: ToPgUUID(id), ByteOffset: offset})
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrBuildLogOffset
			}
			if err != nil {
				return err
			}
			if stored != content {
				return ErrBuildLogOffset
			}
			return nil
		}
		if offset != end {
			return ErrBuildLogOffset
		}
		return q.InsertBuildLogChunk(ctx, pgdb.InsertBuildLogChunkParams{BuildID: ToPgUUID(id), ByteOffset: offset, Content: content})
	})
}

func (s *PostgresBuildRepository) ListLogs(ctx context.Context, appID, id string, after int32) ([]types.BuildLogChunk, error) {
	rows, err := s.engine.Queries.ListBuildLogChunks(ctx, pgdb.ListBuildLogChunksParams{AppID: ToPgUUID(appID), BuildID: ToPgUUID(id), ByteOffset: after})
	if err != nil {
		return nil, err
	}
	chunks := make([]types.BuildLogChunk, 0, len(rows))
	for _, row := range rows {
		chunks = append(chunks, types.BuildLogChunk{Offset: row.ByteOffset, Content: row.Content, CreatedAt: row.CreatedAt.Time})
	}
	return chunks, nil
}
