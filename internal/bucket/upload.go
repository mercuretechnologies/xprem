package bucket

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/url"
	"strings"
	"time"
	"xprem/config"
	"xprem/internal/crypto"
	"xprem/internal/objectstore"
	"xprem/internal/providers/expo"

	"github.com/golang-jwt/jwt/v5"
)

const LocalUploadTokenHeader = "local-upload-token"

// presignUpload hands out the request an uploader PUTs key with. A local
// directory has no upload URL, so local uploads go through the server's own
// route with a token scoped to the app and the branch.
func presignUpload(ctx context.Context, objectStore objectstore.Store, localUploads bool, appId, branch, key string) (*objectstore.UploadRequest, error) {
	if !localUploads {
		return objectStore.PresignPut(ctx, key)
	}
	token, err := mintUploadToken(uploadClaims{Key: key, AppID: appId, Branch: branch})
	if err != nil {
		return nil, err
	}
	uploadURL, err := url.Parse(config.GetEnv("BASE_URL"))
	if err != nil {
		return nil, fmt.Errorf("invalid base URL: %w", err)
	}
	uploadURL.Path, err = url.JoinPath(uploadURL.Path, appId, "uploadLocalFile")
	if err != nil {
		return nil, fmt.Errorf("error joining path: %w", err)
	}
	uploadURL.RawQuery, uploadURL.Fragment = "", ""
	return &objectstore.UploadRequest{URL: uploadURL.String(), Method: "PUT", Headers: map[string]string{LocalUploadTokenHeader: token}}, nil
}

// uploadClaims is the claim set of a local upload token.
type uploadClaims struct {
	jwt.RegisteredClaims
	Key    string `json:"key"`
	Action string `json:"action"`
	AppID  string `json:"appId"`
	Branch string `json:"branch"`
}

func mintUploadToken(claims uploadClaims) (string, error) {
	claims.Action = "uploadLocalFile"
	claims.Subject = GetSubjectForApp(claims.AppID)
	claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(10 * time.Minute))
	return crypto.GenerateJWTToken(config.GetEnv("JWT_SECRET"), claims)
}

// GetSubjectForApp is the token subject of an app: the app id in control-plane
// mode, the Expo account owner in stateless mode.
func GetSubjectForApp(appId string) string {
	if !config.IsDBMode() {
		return expo.FetchSelfUsername(appId)
	}
	return fmt.Sprintf("app:%s", appId)
}

// ValidateUploadToken verifies a local upload token and returns the key it
// grants, with the app and branch it was minted for so the route can check
// them against the request.
func ValidateUploadToken(token string) (key string, appId string, branch string, err error) {
	claims := uploadClaims{}
	if _, err := crypto.DecodeAndExtractJWTToken(config.GetEnv("JWT_SECRET"), token, &claims); err != nil {
		return "", "", "", err
	}
	key, appId, branch = claims.Key, claims.AppID, claims.Branch
	if appId == "" || claims.Subject != GetSubjectForApp(appId) {
		return "", "", "", errors.New("invalid token sub")
	}
	if claims.Action != "uploadLocalFile" {
		return "", "", "", errors.New("invalid token action")
	}
	// A blob key sits outside any branch, but the branch claim is still
	// required so scoped keys can be judged.
	if branch == "" || !(keyInBranch(key, appId, branch) || isBlobKey(key, appId)) {
		return "", "", "", errors.New("upload token key does not match its branch")
	}
	return key, appId, branch, nil
}

func keyInBranch(key, appId, branch string) bool {
	prefix := appId + "/" + branch + "/"
	return strings.HasPrefix(key, prefix) && len(key) > len(prefix) && objectstore.ValidateKey(key) == nil
}

func isBlobKey(key, appId string) bool {
	prefix := appId + "/" + casDir + "/"
	return strings.HasPrefix(key, prefix) && ValidateBlobHash(strings.TrimPrefix(key, prefix)) == nil
}

// ResolveUploadTokenBranch returns the branch an upload token was minted for,
// through the same validation the handler runs, so one definition of a valid
// token drives both the access decision and the write.
func ResolveUploadTokenBranch(token string) (string, error) {
	_, _, branch, err := ValidateUploadToken(token)
	return branch, err
}

// ErrBlobHashMismatch reports a blob whose bytes do not hash to its name.
var ErrBlobHashMismatch = errors.New("uploaded blob does not match its hash")

// HandleUpload stores a local upload under the key its token granted to the
// app. A blob must hash to its own name, or nothing lands at the key.
func HandleUpload(ctx context.Context, appId, key string, body io.Reader) error {
	if isBlobKey(key, appId) {
		body = newHashCheckedReader(body, strings.TrimPrefix(key, appId+"/"+casDir+"/"))
	}
	return GetBucket().ObjectStore.Put(ctx, key, body)
}

type hashCheckedReader struct {
	io.Reader
	digest   hash.Hash
	expected string
}

func newHashCheckedReader(body io.Reader, expected string) *hashCheckedReader {
	digest := sha256.New()
	return &hashCheckedReader{Reader: io.TeeReader(body, digest), digest: digest, expected: expected}
}

func (r *hashCheckedReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err == io.EOF && base64.RawURLEncoding.EncodeToString(r.digest.Sum(nil)) != r.expected {
		return n, ErrBlobHashMismatch
	}
	return n, err
}
