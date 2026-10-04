// Package bucket is the layout of the updates bucket: one store per kind of
// object it holds, each owning its keys, over a raw object store.
package bucket

import (
	"context"
	"path/filepath"
	"sync"
	"time"
	"xprem/internal/objectstore"
)

// createFromCopyTimeout bounds each file copy of CreateFrom, which Azure may
// report as pending indefinitely.
const createFromCopyTimeout = 2 * time.Minute

type Bucket struct {
	// ObjectStore is the raw store, for the layout migrations that reshape it.
	ObjectStore   objectstore.Store
	BlobStore     *BlobStore
	UpdateStore   *UpdateStore
	PatchStore    *PatchStore
	InstanceStore *InstanceStore
	// localRoot is the bucket's directory in local mode, empty otherwise.
	localRoot string
}

// Open lays the stores out over location, under keyPrefix.
func Open(mode objectstore.Mode, location, keyPrefix string) *Bucket {
	localUploads := mode == objectstore.ModeLocal
	localRoot := ""
	var objectStore objectstore.Store
	if localUploads {
		// On disk the prefix is a directory, so spellings like ./tenant/ and
		// tenant// resolve to the same place, as they do when listed back.
		localRoot = filepath.Join(location, keyPrefix)
		objectStore = objectstore.Open(mode, localRoot)
	} else {
		objectStore = objectstore.WithPrefix(objectstore.Open(mode, location), keyPrefix)
	}
	return &Bucket{
		localRoot:     localRoot,
		ObjectStore:   objectStore,
		BlobStore:     &BlobStore{objectStore: objectStore, localUploads: localUploads},
		UpdateStore:   &UpdateStore{objectStore: objectStore, localUploads: localUploads},
		PatchStore:    &PatchStore{objectStore: objectStore},
		InstanceStore: &InstanceStore{objectStore: objectStore},
	}
}

var (
	bucketInstance *Bucket
	once           sync.Once
)

// GetBucket is the updates bucket the environment configures.
func GetBucket() *Bucket {
	once.Do(func() {
		if bucketInstance == nil {
			mode := objectstore.ResolveMode()
			bucketInstance = Open(mode, objectstore.UpdatesLocation(mode), ResolveKeyPrefix())
		}
	})
	return bucketInstance
}

func ResetBucketInstance() {
	bucketInstance = nil
	once = sync.Once{}
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
	var requests []FileUploadRequest
	// Several files may name the same blob; presign it once.
	presignedBlobs := make(map[string]bool, len(files))
	for _, file := range files {
		var upload *objectstore.UploadRequest
		var err error
		if file.InUpdateFolder {
			upload, err = resolvedBucket.UpdateStore.PresignPut(ctx, appId, branch, runtimeVersion, updateId, file.Name)
		} else {
			if presignedBlobs[file.Hash] {
				continue
			}
			presignedBlobs[file.Hash] = true
			upload, err = resolvedBucket.BlobStore.PresignPut(ctx, appId, file.Hash, branch)
		}
		if err != nil {
			return nil, err
		}
		requests = append(requests, FileUploadRequest{
			RequestUploadUrl: upload.URL,
			FileName:         filepath.Base(file.Name),
			FilePath:         file.Name,
			OriginalFileName: file.Name,
			Hash:             file.Hash,
			Headers:          upload.Headers,
		})
	}
	return requests, nil
}
