package bucket

import (
	"context"
	"errors"
	"io"
	"time"
	"xprem/internal/types"
)

var ErrCacheObjectExists = errors.New("cache object already exists")
var ErrCacheDirectUploadRequired = errors.New("cache uploads require the signed bucket URL")

type BuildCacheObject struct {
	AppID        string
	IdentifierID string
	Namespace    types.BuildCacheNamespace
	ID           string
}

func (r BuildCacheObject) Key() string {
	return BuildsPrefix + "/cache/" + r.AppID + "/" + r.IdentifierID + "/" + string(r.Namespace) + "/" + r.ID
}

type BuildCacheStorage interface {
	GetBuildCache(context.Context, BuildCacheObject) (*types.BucketFile, error)
	DeleteBuildCache(context.Context, BuildCacheObject) error
	// A nil upload means the authenticated local upload route must be used.
	RequestBuildCacheUploadURL(context.Context, BuildCacheObject) (*UploadRequest, error)
	RequestBuildCacheDownloadURL(context.Context, BuildCacheObject, time.Time) (string, error)
}

// Only local storage receives uploads through the server.
type LocalBuildCacheStorage interface {
	PutBuildCache(context.Context, BuildCacheObject, io.Reader) error
}
