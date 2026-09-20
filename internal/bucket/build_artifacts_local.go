package bucket

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"xprem/config"
	"xprem/internal/types"

	"github.com/golang-jwt/jwt/v5"
)

func (b *LocalBucket) RequestBuildArtifactDownloadURL(context.Context, BuildArtifact, time.Time) (string, error) {
	return "", nil
}

func (b *LocalBucket) buildArtifactPath(ref BuildArtifact, staging bool) string {
	return filepath.Join(b.rootPath(), filepath.FromSlash(ref.Key(staging)))
}

func (b *LocalBucket) GetBuildArtifact(_ context.Context, ref BuildArtifact, staging bool) (*types.BucketFile, error) {
	path := b.buildArtifactPath(ref, staging)
	return b.openFile(path)
}

func (b *LocalBucket) PutBuildArtifact(_ context.Context, ref BuildArtifact, staging bool, body io.Reader) error {
	path := b.buildArtifactPath(ref, staging)
	return b.writeFileAtomically(path, body)
}

func (b *LocalBucket) writeFileAtomically(target string, body io.Reader) error {
	if b.BasePath == "" {
		return errors.New("BasePath not set")
	}
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".upload-")
	// A concurrent DeleteBuildArtifact may have pruned dir in between.
	if os.IsNotExist(err) {
		if err = os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		f, err = os.CreateTemp(dir, ".upload-")
	}
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, copyErr := io.Copy(f, body)
	syncErr := f.Sync()
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), target)
}

// DeleteBuildArtifact is a no-op when the file is absent.
func (b *LocalBucket) DeleteBuildArtifact(_ context.Context, ref BuildArtifact, staging bool) error {
	path := b.buildArtifactPath(ref, staging)
	if b.BasePath == "" {
		return errors.New("BasePath not set")
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	pruneEmptyBuildDirs(filepath.Join(b.rootPath(), BuildsPrefix), filepath.Dir(path))
	return nil
}

// pruneEmptyBuildDirs removes dir and its empty parents, stopping at root.
func pruneEmptyBuildDirs(root, dir string) {
	for {
		rel, err := filepath.Rel(root, dir)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			return
		}
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// buildUploadClaims binds a local upload token to one app, identifier, and build.
type buildUploadClaims struct {
	jwt.RegisteredClaims
	AppID        string `json:"appId"`
	IdentifierID string `json:"identifierId"`
	BuildID      string `json:"buildId"`
}

// ValidateBuildUploadToken verifies a local build upload token and its scope
// against the app, identifier, and build named in the request.
func ValidateBuildUploadToken(token, appID, identifierID, buildID string) error {
	claims := &buildUploadClaims{}
	_, err := jwt.ParseWithClaims(token, claims, func(*jwt.Token) (any, error) {
		return []byte(config.GetEnv("JWT_SECRET")), nil
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired(), jwt.WithSubject("build-upload"))
	if err != nil {
		return err
	}
	if claims.AppID != appID || claims.IdentifierID != identifierID || claims.BuildID != buildID {
		return errors.New("upload token does not match the requested build")
	}
	return nil
}

// RequestBuildArtifactUploadURL returns the server URL and authorization header
// for a local build artifact upload.
func (b *LocalBucket) RequestBuildArtifactUploadURL(_ context.Context, appID string, ref BuildArtifact) (*UploadRequest, error) {
	uploadURL := config.BaseURL() + fmt.Sprintf("/%s/build/%s/artifacts/%s/upload", appID, ref.IdentifierID, ref.BuildID)
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, buildUploadClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "build-upload",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(buildUploadExpiry)),
		},
		AppID: appID, IdentifierID: ref.IdentifierID, BuildID: ref.BuildID,
	}).SignedString([]byte(config.GetEnv("JWT_SECRET")))
	if err != nil {
		return nil, err
	}
	return &UploadRequest{URL: uploadURL, Method: "PUT", Headers: map[string]string{LocalUploadTokenHeader: token}}, nil
}
