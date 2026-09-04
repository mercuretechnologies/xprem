package store

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// ParsePgUUID parses id, refusing a malformed one rather than handing the
// database a NULL.
func ParsePgUUID(id string) (pgtype.UUID, error) {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("invalid uuid %q: %w", id, err)
	}
	return pgtype.UUID{Bytes: parsed, Valid: true}, nil
}

// ParsePgUUIDs parses every id; nil for an empty input.
func ParsePgUUIDs(ids []string) ([]pgtype.UUID, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	parsed := make([]pgtype.UUID, 0, len(ids))
	for _, id := range ids {
		pgID, err := ParsePgUUID(id)
		if err != nil {
			return nil, err
		}
		parsed = append(parsed, pgID)
	}
	return parsed, nil
}

// ToPgUUID maps a malformed id to the SQL NULL uuid; callers whose input is
// not validated upstream should use ParsePgUUID.
func ToPgUUID(id string) pgtype.UUID {
	pgID, err := ParsePgUUID(id)
	if err != nil {
		return pgtype.UUID{}
	}
	return pgID
}

// ToPgUUIDPtr maps a nil pointer to the SQL NULL uuid.
func ToPgUUIDPtr(id *string) pgtype.UUID {
	if id == nil {
		return pgtype.UUID{}
	}
	return ToPgUUID(*id)
}

// ToPgTimestamptz maps a nil pointer to the SQL NULL timestamptz.
func ToPgTimestamptz(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}

// FromPgTimestamptz maps the SQL NULL timestamptz to a nil pointer.
func FromPgTimestamptz(ts pgtype.Timestamptz) *time.Time {
	if !ts.Valid {
		return nil
	}
	t := ts.Time
	return &t
}
