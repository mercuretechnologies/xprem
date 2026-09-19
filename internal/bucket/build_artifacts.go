package bucket

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
	"xprem/internal/types"
)

var ErrBuildDownloadExpired = errors.New("build download link expired")

// BuildsPrefix is the bucket-root directory of every build artifact, a sibling
// of the {appId}/ OTA trees.
const BuildsPrefix = "builds"

const (
	buildStagingDir   = ".uploads"
	buildUploadExpiry = 10 * time.Minute
)

type BuildArtifact struct {
	IdentifierID string
	BuildID      string
	Type         types.BuildArtifactType
}

func (r BuildArtifact) downloadContentType() string {
	if r.Type == types.BuildArtifactAPK {
		return "application/vnd.android.package-archive"
	}
	return "application/octet-stream"
}

func (r BuildArtifact) downloadDisposition() string {
	return fmt.Sprintf(`attachment; filename="%s.%s"`, r.BuildID, r.Type)
}

// Key is builds/{platform}/{identifierId}/{buildId}.{type}, with an
// .uploads/ segment before the file name for the staging copy. The reference
// must be validated before the key is used for storage.
func (r BuildArtifact) Key(staging bool) string {
	platform, _ := r.Type.Platform()
	folder := BuildsPrefix + "/" + string(platform) + "/" + r.IdentifierID + "/"
	if staging {
		folder += buildStagingDir + "/"
	}
	return folder + r.BuildID + "." + string(r.Type)
}

type BuildArtifactStorage interface {
	GetBuildArtifact(ctx context.Context, ref BuildArtifact, staging bool) (*types.BucketFile, error)
	PutBuildArtifact(ctx context.Context, ref BuildArtifact, staging bool, body io.Reader) error
	DeleteBuildArtifact(ctx context.Context, ref BuildArtifact, staging bool) error
	RequestBuildArtifactUploadURL(ctx context.Context, appID string, ref BuildArtifact) (*UploadRequest, error)
	// An empty URL means the artifact must be streamed through the server.
	RequestBuildArtifactDownloadURL(ctx context.Context, ref BuildArtifact, expiresAt time.Time) (string, error)
}
