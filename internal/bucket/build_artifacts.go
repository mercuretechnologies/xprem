package bucket

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
	"xprem/config"
	"xprem/internal/objectstore"
	"xprem/internal/types"

	"github.com/golang-jwt/jwt/v5"
)

// BuildsPrefix is the bucket-root directory of every build artifact, a sibling
// of the {appId}/ OTA trees.
const BuildsPrefix = "builds"

const (
	buildStagingDir   = ".uploads"
	buildUploadExpiry = 10 * time.Minute
)

var buildsLocationEnv = map[objectstore.Mode]string{
	objectstore.ModeS3:    "S3_BUCKET_BUILDS_NAME",
	objectstore.ModeGCS:   "GCS_BUCKET_BUILDS_NAME",
	objectstore.ModeAzure: "AZURE_BLOB_BUILDS_CONTAINER_NAME",
	objectstore.ModeLocal: "LOCAL_BUILDS_BASE_PATH",
}

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

func (r BuildArtifact) Validate() error {
	if _, err := r.Type.Platform(); err != nil {
		return err
	}
	if err := validateUUID("identifierId", r.IdentifierID); err != nil {
		return err
	}
	return validateUUID("buildId", r.BuildID)
}

// Key is builds/{platform}/{identifierId}/{buildId}.{type}, with an
// .uploads/ segment before the file name for the staging copy.
func (r BuildArtifact) Key(staging bool) (string, error) {
	platform, err := r.Type.Platform()
	if err != nil {
		return "", err
	}
	folder := BuildsPrefix + "/" + string(platform) + "/" + r.IdentifierID + "/"
	if staging {
		folder += buildStagingDir + "/"
	}
	return folder + r.BuildID + "." + string(r.Type), nil
}

// BuildArtifactStore holds the artifact of each build, and its staging copy
// until the upload is verified.
type BuildArtifactStore struct {
	objectStore   objectstore.Store
	localUploads  bool
	sharesUpdates bool
}

// OpenBuildArtifactStore opens the location the *_BUILDS_* env var names, or
// the updates location when it is unset. On disk the artifacts stay private
// to the server's user either way.
func OpenBuildArtifactStore() (*BuildArtifactStore, error) {
	mode := objectstore.ResolveMode()
	location := config.GetEnv(buildsLocationEnv[mode])
	sharesUpdates := location == ""
	if sharesUpdates {
		location = objectstore.UpdatesLocation(mode)
	}
	var objectStore objectstore.Store
	switch {
	case mode == objectstore.ModeLocal:
		objectStore = objectstore.OpenPrivateDirectory(location)
	case sharesUpdates:
		objectStore = objectstore.Open(mode, location)
	default:
		var err error
		objectStore, sharesUpdates, err = objectstore.OpenFeatureStore(buildsLocationEnv, true)
		if err != nil {
			return nil, err
		}
	}
	return &BuildArtifactStore{
		objectStore:   objectstore.WithPrefix(objectStore, ResolveKeyPrefix()),
		localUploads:  mode == objectstore.ModeLocal,
		sharesUpdates: sharesUpdates,
	}, nil
}

// BuildsLocationEnv names the env var giving build artifacts their own location in mode.
func BuildsLocationEnv(mode objectstore.Mode) string {
	return buildsLocationEnv[mode]
}

// SharesUpdatesLocation reports whether the artifacts live in the updates bucket.
func (s *BuildArtifactStore) SharesUpdatesLocation() bool {
	return s.sharesUpdates
}

func (s *BuildArtifactStore) key(ref BuildArtifact, staging bool) (string, error) {
	if err := ref.Validate(); err != nil {
		return "", err
	}
	return ref.Key(staging)
}

// Get returns nil, nil when the artifact is absent.
func (s *BuildArtifactStore) Get(ctx context.Context, ref BuildArtifact, staging bool) (*types.BucketFile, error) {
	key, err := s.key(ref, staging)
	if err != nil {
		return nil, err
	}
	return s.objectStore.Get(ctx, key)
}

func (s *BuildArtifactStore) Put(ctx context.Context, ref BuildArtifact, staging bool, body io.Reader) error {
	key, err := s.key(ref, staging)
	if err != nil {
		return err
	}
	return s.objectStore.Put(ctx, key, body)
}

// Delete succeeds when the artifact is already absent.
func (s *BuildArtifactStore) Delete(ctx context.Context, ref BuildArtifact, staging bool) error {
	key, err := s.key(ref, staging)
	if err != nil {
		return err
	}
	return s.objectStore.Delete(ctx, key)
}

// PresignPut hands out the upload of the staging copy. A local directory has
// no upload URL, so local uploads go through the server's build route with a
// token scoped to the build.
func (s *BuildArtifactStore) PresignPut(ctx context.Context, appID string, ref BuildArtifact) (*objectstore.UploadRequest, error) {
	if err := validateSegment("appId", appID); err != nil {
		return nil, err
	}
	key, err := s.key(ref, true)
	if err != nil {
		return nil, err
	}
	if !s.localUploads {
		return s.objectStore.PresignPut(ctx, key)
	}
	return localBuildUpload(appID, ref)
}

// PresignGet returns "" when the server must stream the artifact itself.
func (s *BuildArtifactStore) PresignGet(ctx context.Context, ref BuildArtifact, expiresAt time.Time) (string, error) {
	key, err := s.key(ref, false)
	if err != nil {
		return "", err
	}
	if !expiresAt.After(time.Now()) {
		return "", objectstore.ErrDownloadExpired
	}
	return s.objectStore.PresignGet(ctx, key, objectstore.Download{
		ExpiresAt:          expiresAt,
		ContentDisposition: ref.downloadDisposition(),
		ContentType:        ref.downloadContentType(),
	})
}

// buildUploadClaims binds a local upload token to one app, identifier, and build.
type buildUploadClaims struct {
	jwt.RegisteredClaims
	AppID        string `json:"appId"`
	IdentifierID string `json:"identifierId"`
	BuildID      string `json:"buildId"`
}

func localBuildUpload(appID string, ref BuildArtifact) (*objectstore.UploadRequest, error) {
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
	return &objectstore.UploadRequest{URL: uploadURL, Method: "PUT", Headers: map[string]string{LocalUploadTokenHeader: token}}, nil
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
