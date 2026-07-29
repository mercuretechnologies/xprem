package branch

import (
	"xprem/internal/helpers"
	"xprem/internal/providers/expo"
)

func UpsertBranch(appId, branch string) error {
	branches, err := expo.FetchBranches(appId)
	if err != nil {
		return err
	}
	if !helpers.StringInSlice(branch, branches) {
		return expo.CreateBranch(appId, branch)
	}
	return nil
}
