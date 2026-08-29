package bucket

import (
	"context"
	"io"
	"xprem/internal/types"
)

// validatingBucket is a decorator around any Bucket implementation that
// validates every user-supplied identifier (branch, runtimeVersion, updateId,
// fileName, assetPath, migrationId) before delegating, rejecting values that
// contain path separators, "..", or that are empty. Mounted once in
// GetBucket(); concrete backends still assume inputs are clean but this layer
// guarantees that assumption even if a handler forgets to sanitize.
type validatingBucket struct {
	Inner Bucket
}

func (v *validatingBucket) GetBranches(appId string) ([]string, error) {
	if err := validateSegment("appId", appId); err != nil {
		return nil, err
	}
	branches, err := v.Inner.GetBranches(appId)
	if err != nil {
		return nil, err
	}
	return omitReservedAppChildren(branches), nil
}

func (v *validatingBucket) GetRuntimeVersions(appId, branch string) ([]types.RuntimeVersionWithStats, error) {
	if err := validateSegment("appId", appId); err != nil {
		return nil, err
	}
	if err := validateBranch(branch); err != nil {
		return nil, err
	}
	return v.Inner.GetRuntimeVersions(appId, branch)
}

func (v *validatingBucket) GetUpdates(appId, branch, runtimeVersion string) ([]types.Update, error) {
	if err := validateSegment("appId", appId); err != nil {
		return nil, err
	}
	if err := validateBranch(branch); err != nil {
		return nil, err
	}
	if err := validateSegment("runtimeVersion", runtimeVersion); err != nil {
		return nil, err
	}
	return v.Inner.GetUpdates(appId, branch, runtimeVersion)
}

func (v *validatingBucket) GetFile(update types.Update, assetPath string) (*types.BucketFile, error) {
	if err := validateUpdate(&update); err != nil {
		return nil, err
	}
	if err := validateRelativePath("assetPath", assetPath); err != nil {
		return nil, err
	}
	return v.Inner.GetFile(update, assetPath)
}

func (v *validatingBucket) RequestUploadUrlForFileUpdate(appId, branch, runtimeVersion, updateId, fileName string) (string, error) {
	if err := validateSegment("appId", appId); err != nil {
		return "", err
	}
	if err := validateBranch(branch); err != nil {
		return "", err
	}
	if err := validateSegment("runtimeVersion", runtimeVersion); err != nil {
		return "", err
	}
	if err := validateSegment("updateId", updateId); err != nil {
		return "", err
	}
	if err := validateRelativePath("fileName", fileName); err != nil {
		return "", err
	}
	return v.Inner.RequestUploadUrlForFileUpdate(appId, branch, runtimeVersion, updateId, fileName)
}

func (v *validatingBucket) UploadFileIntoUpdate(update types.Update, fileName string, file io.Reader) error {
	if err := validateUpdate(&update); err != nil {
		return err
	}
	if err := validateRelativePath("fileName", fileName); err != nil {
		return err
	}
	return v.Inner.UploadFileIntoUpdate(update, fileName, file)
}

func (v *validatingBucket) CopyFileIntoUpdate(source types.Update, target types.Update, fileName string) error {
	if err := validateUpdate(&source); err != nil {
		return err
	}
	if err := validateUpdate(&target); err != nil {
		return err
	}
	if err := validateRelativePath("fileName", fileName); err != nil {
		return err
	}
	return v.Inner.CopyFileIntoUpdate(source, target, fileName)
}

func (v *validatingBucket) DeleteUpdateFolder(appId, branch, runtimeVersion, updateId string) error {
	if err := validateSegment("appId", appId); err != nil {
		return err
	}
	if err := validateBranch(branch); err != nil {
		return err
	}
	if err := validateSegment("runtimeVersion", runtimeVersion); err != nil {
		return err
	}
	if err := validateSegment("updateId", updateId); err != nil {
		return err
	}
	return v.Inner.DeleteUpdateFolder(appId, branch, runtimeVersion, updateId)
}

func (v *validatingBucket) CreateUpdateFrom(previousUpdate *types.Update, newUpdateId string) (*types.Update, error) {
	if err := validateUpdate(previousUpdate); err != nil {
		return nil, err
	}
	if err := validateSegment("newUpdateId", newUpdateId); err != nil {
		return nil, err
	}
	return v.Inner.CreateUpdateFrom(previousUpdate, newUpdateId)
}

func (v *validatingBucket) GetInstanceID() (string, error) {
	return v.Inner.GetInstanceID()
}

func (v *validatingBucket) PersistInstanceID(id string) error {
	return v.Inner.PersistInstanceID(id)
}

func (v *validatingBucket) RetrieveMigrationHistory() ([]string, error) {
	return v.Inner.RetrieveMigrationHistory()
}

func (v *validatingBucket) ApplyMigration(migrationId string) error {
	if err := validateSegment("migrationId", migrationId); err != nil {
		return err
	}
	return v.Inner.ApplyMigration(migrationId)
}

func (v *validatingBucket) RemoveMigrationFromHistory(migrationId string) error {
	if err := validateSegment("migrationId", migrationId); err != nil {
		return err
	}
	return v.Inner.RemoveMigrationFromHistory(migrationId)
}

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

func (v *validatingBucket) RequestBlobUploadURL(appId, hash, branch string) (string, error) {
	if err := validateSegment("appId", appId); err != nil {
		return "", err
	}
	if err := ValidateBlobHash(hash); err != nil {
		return "", err
	}
	if err := validateBranch(branch); err != nil {
		return "", err
	}
	return v.Inner.RequestBlobUploadURL(appId, hash, branch)
}

func omitReservedAppChildren(names []string) []string {
	if len(names) == 0 {
		return names
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		if name != casDir {
			out = append(out, name)
		}
	}
	return out
}
