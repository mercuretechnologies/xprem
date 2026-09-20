package bucket

import (
	"fmt"
	"io"
	"slices"
	"xprem/internal/types"
)

func (v *validatingBucket) GetBranches(appId string) ([]string, error) {
	if err := validateSegment("appId", appId); err != nil {
		return nil, err
	}
	branches, err := v.Inner.GetBranches(appId)
	if err != nil {
		return nil, err
	}
	// The backends list the children of {appId}/, and the reserved
	// directories are among them.
	return slices.DeleteFunc(branches, ReservedBranchName), nil
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

func (v *validatingBucket) RequestUploadUrlForFileUpdate(appId, branch, runtimeVersion, updateId, fileName string) (*UploadRequest, error) {
	if err := validateSegment("appId", appId); err != nil {
		return nil, err
	}
	if err := validateBranch(branch); err != nil {
		return nil, err
	}
	if err := validateSegment("runtimeVersion", runtimeVersion); err != nil {
		return nil, err
	}
	if err := validateSegment("updateId", updateId); err != nil {
		return nil, err
	}
	if err := validateRelativePath("fileName", fileName); err != nil {
		return nil, err
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

func ValidateUploadFile(name, hash string) error {
	if err := validateRelativePath("file name", name); err != nil {
		return err
	}
	return ValidateBlobHash(hash)
}

func validateUpdate(u *types.Update) error {
	if u == nil {
		return fmt.Errorf("update must not be nil")
	}
	if err := validateSegment("appId", u.AppId); err != nil {
		return err
	}
	if err := validateBranch(u.Branch); err != nil {
		return err
	}
	if err := validateSegment("runtimeVersion", u.RuntimeVersion); err != nil {
		return err
	}
	if err := validateSegment("updateId", u.UpdateId); err != nil {
		return err
	}
	return nil
}

func validateBranch(branch string) error {
	if err := validateSegment("branch", branch); err != nil {
		return err
	}
	if ReservedBranchName(branch) {
		return fmt.Errorf("invalid branch: %q is reserved", branch)
	}
	return nil
}
