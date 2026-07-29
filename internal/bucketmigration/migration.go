package bucketmigration

import (
	"time"
	"xprem/internal/bucket"
)

type Migration interface {
	ID() string
	Timestamp() time.Time
	Up(b bucket.Bucket) error
	Down(b bucket.Bucket) error
}
