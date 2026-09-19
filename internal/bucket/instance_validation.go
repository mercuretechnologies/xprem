package bucket

import "context"

func (v *validatingBucket) GetInstanceID(ctx context.Context) (string, error) {
	return v.Inner.GetInstanceID(ctx)
}

func (v *validatingBucket) PersistInstanceID(ctx context.Context, id string) error {
	return v.Inner.PersistInstanceID(ctx, id)
}
