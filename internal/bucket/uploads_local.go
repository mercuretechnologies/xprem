package bucket

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"xprem/config"
	"xprem/internal/crypto"

	"github.com/golang-jwt/jwt/v5"
)

// uploadClaims is the claim set of a local upload token. Branch is what
// lets the router judge the upload route, which names no branch of its own,
// against the API key's access rules.
type uploadClaims struct {
	jwt.RegisteredClaims
	FilePath string `json:"filePath"`
	Action   string `json:"action"`
	AppID    string `json:"appId"`
	Branch   string `json:"branch"`
}

// ValidateUploadTokenAndResolveFilePath decodes and verifies the JWT emitted
// by RequestUploadUrlForFileUpdate. It returns the resolved filesystem path
// plus the appId claim so the caller can confirm the token is scoped to the
// same app as the URL, without that check, an attacker who obtained a leaked
// token for AppA could PUT into AppB's bucket by hitting
// /{AppB}/uploadLocalFile with AppA's local-upload-token header.
func ValidateUploadTokenAndResolveFilePath(token string) (filePath string, appId string, branch string, err error) {
	claims := uploadClaims{}
	if _, err := crypto.DecodeAndExtractJWTToken(config.GetEnv("JWT_SECRET"), token, &claims); err != nil {
		return "", "", "", err
	}
	filePath, appId, branch = claims.FilePath, claims.AppID, claims.Branch
	if appId == "" || claims.Subject != GetSubjectForApp(appId) {
		return "", "", "", errors.New("invalid token sub")
	}
	if claims.Action != "uploadLocalFile" {
		return "", "", "", errors.New("invalid token action")
	}
	// The token carries the branch and the file path as two separate claims,
	// and the router authorizes the FIRST while the handler writes under the
	// SECOND. They agree by construction today, both being derived from the
	// same argument in RequestUploadUrlForFileUpdate, and this is what keeps
	// them agreeing: an access rule granting a key one branch must not admit a
	// file written into another.
	//
	// A token with no branch claim is refused rather than waved through. It
	// can only come from a server older than this claim, and the alternative
	// was a check whose safety was borrowed from another package (the access
	// rules happen to refuse a scoped key on an empty branch). The cost is a
	// publish that fails during a rolling deploy, in local-bucket mode, which
	// does not survive several replicas anyway.
	//
	// Blob uploads sit at {appId}/cas/{hash}, outside any branch directory.
	// The branch claim is still required so scoped keys can be judged; the
	// path check is the cas directory instead of the branch tree.
	if uploadPathIsBlob(filePath, appId) {
		if branch == "" {
			return "", "", "", errors.New("upload token path does not match its branch")
		}
		return filePath, appId, branch, nil
	}
	if !uploadPathIsInBranch(filePath, appId, branch) {
		return "", "", "", errors.New("upload token path does not match its branch")
	}
	return filePath, appId, branch, nil
}

// localBucketRoot mirrors (*LocalBucket).rootPath() for the package-level token
// validation below, which has no bucket instance to read it from.
func localBucketRoot() string {
	base := config.GetEnv("LOCAL_BUCKET_BASE_PATH")
	if base == "" {
		return ""
	}
	if prefix := resolveKeyPrefix(); prefix != "" {
		return filepath.Join(base, prefix)
	}
	return base
}

// uploadPathIsInBranch reports whether filePath sits inside the branch
// directory of the local bucket. Containment is checked against the bucket root
// as well as the branch: a path claim is only ever minted by
// RequestUploadUrlForFileUpdate, so one pointing outside the bucket is forged,
// and matching the appId/branch pair as a substring would accept it anywhere on
// the filesystem.
func uploadPathIsInBranch(filePath, appId, branch string) bool {
	root := localBucketRoot()
	if root == "" || filePath == "" || appId == "" || branch == "" {
		return false
	}
	expectedBase := filepath.Join(root, appId, branch)
	// filepath.Rel over Clean'd paths, as GetFile does: it collapses traversal
	// and does not treat a sibling sharing a string prefix as nested.
	rel, err := filepath.Rel(expectedBase, filepath.Clean(filePath))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return false
	}
	return true
}

func uploadPathIsBlob(filePath, appId string) bool {
	root := localBucketRoot()
	if root == "" || filePath == "" || appId == "" {
		return false
	}
	expectedBase := filepath.Join(root, appId, casDir)
	rel, err := filepath.Rel(expectedBase, filepath.Clean(filePath))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return false
	}
	if strings.ContainsRune(rel, os.PathSeparator) {
		return false
	}
	return ValidateBlobHash(rel) == nil
}

// localUploadRequest keeps the per-file grant out of URLs and request logs.
func (b *LocalBucket) localUploadRequest(appId, branch, filePath string) (*UploadRequest, error) {
	token, err := crypto.GenerateJWTToken(config.GetEnv("JWT_SECRET"), uploadClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   GetSubjectForApp(appId),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(10 * time.Minute)),
		},
		FilePath: filePath, Action: "uploadLocalFile", AppID: appId, Branch: branch,
	})
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
	return &UploadRequest{URL: uploadURL.String(), Method: "PUT", Headers: map[string]string{LocalUploadTokenHeader: token}}, nil
}

// ResolveUploadTokenBranch returns the branch an upload token was minted for.
// It is the router's read, before the handler validates the same token for
// itself, and it goes through the SAME validation so there is one definition
// of a valid upload token: signature, expiry, action and app subject. A
// separate, laxer read here would have let any JWT_SECRET-signed token drive
// the access decision on this route.
//
// A token minted by a server older than the branch claim yields "", which the
// access rules refuse for a scoped key and ignore for an unscoped one.
func ResolveUploadTokenBranch(token string) (string, error) {
	_, _, branch, err := ValidateUploadTokenAndResolveFilePath(token)
	return branch, err
}

// HandleUploadFile stores a local upload; a file under cas/ must hash to its own name.
func HandleUploadFile(appId, filePath string, body io.Reader) error {
	expectedHash := ""
	if uploadPathIsBlob(filePath, appId) {
		expectedHash = filepath.Base(filePath)
	}
	return writeFileAtomically(filePath, body, expectedHash)
}
