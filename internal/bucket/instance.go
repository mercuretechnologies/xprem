package bucket

import "context"

type InstanceStorage interface {
	GetInstanceID(ctx context.Context) (string, error)
	PersistInstanceID(ctx context.Context, id string) error
}
