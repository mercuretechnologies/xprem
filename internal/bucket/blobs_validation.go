package bucket

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"xprem/internal/types"
)

func (v *validatingBucket) BlobExists(ctx context.Context, appId, hash string) (bool, error) {
	if err := validateSegment("appId", appId); err != nil {
		return false, err
	}
	if err := ValidateBlobHash(hash); err != nil {
		return false, err
	}
	return v.Inner.BlobExists(ctx, appId, hash)
}

func (v *validatingBucket) GetBlob(ctx context.Context, appId, hash string) (*types.BucketFile, error) {
	if err := validateSegment("appId", appId); err != nil {
		return nil, err
	}
	if err := ValidateBlobHash(hash); err != nil {
		return nil, err
	}
	return v.Inner.GetBlob(ctx, appId, hash)
}

func (v *validatingBucket) PutBlob(ctx context.Context, appId, hash string, body io.Reader) error {
	if err := validateSegment("appId", appId); err != nil {
		return err
	}
	if err := ValidateBlobHash(hash); err != nil {
		return err
	}
	return v.Inner.PutBlob(ctx, appId, hash, body)
}

func (v *validatingBucket) RequestBlobUploadURL(ctx context.Context, appId, hash, branch string) (*UploadRequest, error) {
	if err := validateSegment("appId", appId); err != nil {
		return nil, err
	}
	if err := ValidateBlobHash(hash); err != nil {
		return nil, err
	}
	if err := validateBranch(branch); err != nil {
		return nil, err
	}
	return v.Inner.RequestBlobUploadURL(ctx, appId, hash, branch)
}

func ValidateBlobHash(hash string) error {
	if len(hash) != blobHashLength {
		return fmt.Errorf("invalid hash: must be %d characters", blobHashLength)
	}
	// Strict rejects spellings with non-zero trailing padding bits, which
	// decode to the same digest but would mint a second CAS key.
	if _, err := base64.RawURLEncoding.Strict().DecodeString(hash); err != nil {
		return fmt.Errorf("invalid hash: must be canonical base64url")
	}
	return nil
}
