package bucketmigration

import (
	"time"
	"xprem/internal/bucket"
)

type BaseMigration struct {
	Id       string
	Time     time.Time
	UpFunc   func(b bucket.Bucket) error
	DownFunc func(b bucket.Bucket) error
}

func (m BaseMigration) ID() string                 { return m.Id }
func (m BaseMigration) Timestamp() time.Time       { return m.Time }
func (m BaseMigration) Up(b bucket.Bucket) error   { return m.UpFunc(b) }
func (m BaseMigration) Down(b bucket.Bucket) error { return m.DownFunc(b) }
