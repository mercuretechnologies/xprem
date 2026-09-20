package bucket

import (
	"context"
	"io"
	"path/filepath"
	"sync"
	"time"
	"xprem/internal/types"
)

// copyFileTimeout bounds a single CopyFileIntoUpdate provider call, so a
// stalled copy degrades into a regular upload instead of hanging the publish.
const copyFileTimeout = 30 * time.Second

func ReservedBranchName(branch string) bool {
	return branch == casDir || branch == bsDiffDir
}

type UpdateStorage interface {
	GetBranches(appId string) ([]string, error)
	GetRuntimeVersions(appId string, branch string) ([]types.RuntimeVersionWithStats, error)
	GetUpdates(appId string, branch string, runtimeVersion string) ([]types.Update, error)
	GetFile(update types.Update, assetPath string) (*types.BucketFile, error)
	RequestUploadUrlForFileUpdate(appId string, branch string, runtimeVersion string, updateId string, fileName string) (*UploadRequest, error)
	UploadFileIntoUpdate(update types.Update, fileName string, file io.Reader) error
	CopyFileIntoUpdate(source types.Update, target types.Update, fileName string) error
	DeleteUpdateFolder(appId string, branch string, runtimeVersion string, updateId string) error
	CreateUpdateFrom(previousUpdate *types.Update, newUpdateId string) (*types.Update, error)
}

type FileUploadRequest struct {
	RequestUploadUrl string `json:"requestUploadUrl"`
	FileName         string `json:"fileName"`
	FilePath         string `json:"filePath"`
	OriginalFileName string `json:"originalFileName"`
	Hash             string `json:"hash"`
	// Headers must be sent verbatim by the uploader on its PUT to
	// RequestUploadUrl. Azure Put Blob rejects requests missing
	// x-ms-blob-type, and carrying the requirement in the response keeps
	// the CLI provider-agnostic. Local uploads carry their per-file token here.
	Headers map[string]string `json:"headers,omitempty"`
}

type UploadFile struct {
	Name string
	Hash string
	// InUpdateFolder routes the presign to the update folder instead of
	// cas/{hash}. metadata.json differs on every publish by construction and
	// expoConfig.json is a kilobyte, so addressing them by content buys nothing
	// and would cost the manifest its only way to read them back.
	InUpdateFolder bool
}

// RequestUploadUrlsForFileUpdates presigns one publish's uploads, routing each
// file by where it lives: cas/{hash} for content-addressed files, the update
// folder for the rest.
func RequestUploadUrlsForFileUpdates(ctx context.Context, appId, branch, runtimeVersion, updateId string, files []UploadFile) ([]FileUploadRequest, error) {
	resolvedBucket := GetBucket()

	// Several files may name the same blob; presign it once.
	toSign := make([]UploadFile, 0, len(files))
	seenBlobs := make(map[string]struct{}, len(files))
	for _, file := range files {
		if !file.InUpdateFolder {
			if _, dup := seenBlobs[file.Hash]; dup {
				continue
			}
			seenBlobs[file.Hash] = struct{}{}
		}
		toSign = append(toSign, file)
	}

	requests := make([]FileUploadRequest, len(toSign))
	var wg sync.WaitGroup
	errChan := make(chan error, len(toSign))
	wg.Add(len(toSign))
	for i, file := range toSign {
		go func(index int, file UploadFile) {
			defer wg.Done()
			var upload *UploadRequest
			var err error
			if file.InUpdateFolder {
				upload, err = resolvedBucket.RequestUploadUrlForFileUpdate(appId, branch, runtimeVersion, updateId, file.Name)
			} else {
				upload, err = resolvedBucket.RequestBlobUploadURL(ctx, appId, file.Hash, branch)
			}
			if err != nil {
				errChan <- err
				return
			}
			requests[index] = FileUploadRequest{
				RequestUploadUrl: upload.URL,
				FileName:         filepath.Base(file.Name),
				FilePath:         file.Name,
				OriginalFileName: file.Name,
				Hash:             file.Hash,
				Headers:          upload.Headers,
			}
		}(i, file)
	}

	wg.Wait()
	close(errChan)

	if len(errChan) > 0 {
		return nil, <-errChan
	}

	return requests, nil
}
