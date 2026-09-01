package bucket

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"xprem/config"
	"xprem/internal/crypto"
	"xprem/internal/helpers"
	"xprem/internal/providers/expo"
	"xprem/internal/types"

	"github.com/golang-jwt/jwt/v5"
)

type LocalBucket struct {
	BasePath  string
	KeyPrefix string
}

func (b *LocalBucket) rootPath() string {
	if b.KeyPrefix == "" {
		return b.BasePath
	}
	return filepath.Join(b.BasePath, b.KeyPrefix)
}

func (b *LocalBucket) DeleteUpdateFolder(appId string, branch string, runtimeVersion string, updateId string) error {
	if b.BasePath == "" {
		return errors.New("BasePath not set")
	}
	dirPath := filepath.Join(b.rootPath(), appId, branch, runtimeVersion, updateId)
	return os.RemoveAll(dirPath)
}

func (b *LocalBucket) RequestUploadUrlForFileUpdate(appId string, branch string, runtimeVersion string, updateId string, fileName string) (string, error) {
	if b.BasePath == "" {
		return "", errors.New("BasePath not set")
	}
	dirPath := filepath.Join(b.rootPath(), appId, branch, runtimeVersion, updateId)
	err := os.MkdirAll(dirPath, os.ModePerm)
	if err != nil {
		return "", err
	}
	token, err := crypto.GenerateJWTToken(config.GetEnv("JWT_SECRET"), uploadClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   GetSubjectForApp(appId),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute * 10)),
		},
		FilePath: filepath.Join(dirPath, fileName),
		Action:   "uploadLocalFile",
		AppID:    appId,
		Branch:   branch,
	})
	if err != nil {
		return "", err
	}
	parsedURL, err := url.Parse(config.GetEnv("BASE_URL"))
	if err != nil {
		return "", fmt.Errorf("invalid base URL: %w", err)
	}
	// The route is registered under the /{APP_ID} subrouter in router.go,
	// so the URL must include the appId segment or the client PUT 404s.
	parsedURL.Path, err = url.JoinPath(parsedURL.Path, appId, "uploadLocalFile")
	if err != nil {
		return "", fmt.Errorf("error joining path: %w", err)
	}
	query := url.Values{}
	query.Set("token", token)
	parsedURL.RawQuery = query.Encode()
	return parsedURL.String(), nil
}

func (b *LocalBucket) GetUpdates(appId string, branch string, runtimeVersion string) ([]types.Update, error) {
	if b.BasePath == "" {
		return nil, errors.New("BasePath not set")
	}
	dirPath := filepath.Join(b.rootPath(), appId, branch, runtimeVersion)
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return []types.Update{}, nil
	}
	var updates []types.Update
	for _, entry := range entries {
		if entry.IsDir() {
			updateId, err := strconv.ParseInt(entry.Name(), 10, 64)
			if err == nil {
				updates = append(updates, types.Update{
					AppId:          appId,
					Branch:         branch,
					RuntimeVersion: runtimeVersion,
					UpdateId:       strconv.FormatInt(updateId, 10),
					CreatedAt:      helpers.NormalizeTimestampToDuration(updateId),
				})
			}
		}
	}
	return updates, nil
}

func (b *LocalBucket) GetFile(update types.Update, assetPath string) (*types.BucketFile, error) {
	if b.BasePath == "" {
		return nil, errors.New("BasePath not set")
	}

	expectedBase := filepath.Join(b.rootPath(), update.AppId, update.Branch, update.RuntimeVersion, update.UpdateId)
	filePath := filepath.Join(expectedBase, assetPath)
	// Use filepath.Rel so sibling dirs sharing a string prefix (e.g. ".../123" vs ".../1234")
	// aren't treated as nested, and so "." (the base itself) is accepted.
	rel, err := filepath.Rel(expectedBase, filePath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return nil, errors.New("invalid asset path")
	}

	file, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}

	return &types.BucketFile{
		Reader:    file,
		CreatedAt: info.ModTime(),
	}, nil
}
func (b *LocalBucket) GetBranches(appId string) ([]string, error) {
	if b.BasePath == "" {
		return nil, errors.New("BasePath not set")
	}
	entries, err := os.ReadDir(filepath.Join(b.rootPath(), appId))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var branches []string
	for _, entry := range entries {
		if entry.IsDir() {
			branches = append(branches, entry.Name())
		}
	}
	return branches, nil
}

func (b *LocalBucket) GetRuntimeVersions(appId string, branch string) ([]types.RuntimeVersionWithStats, error) {
	if b.BasePath == "" {
		return nil, errors.New("BasePath not set")
	}
	dirPath := filepath.Join(b.rootPath(), appId, branch)
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, err
	}
	var runtimeVersions []types.RuntimeVersionWithStats
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		runtimeVersion := entry.Name()
		updatesPath := filepath.Join(dirPath, runtimeVersion)
		updates, err := os.ReadDir(updatesPath)
		if err != nil {
			continue
		}
		var updateTimestamps []int64
		for _, update := range updates {
			if !update.IsDir() {
				continue
			}
			if _, err := os.Stat(filepath.Join(updatesPath, update.Name(), ".check")); err != nil {
				continue
			}
			timestamp, err := strconv.ParseInt(update.Name(), 10, 64)
			if err != nil {
				continue
			}
			updateTimestamps = append(updateTimestamps, timestamp)
		}
		if len(updateTimestamps) == 0 {
			continue
		}

		sort.Slice(updateTimestamps, func(i, j int) bool { return updateTimestamps[i] < updateTimestamps[j] })

		runtimeVersions = append(runtimeVersions, types.RuntimeVersionWithStats{
			RuntimeVersion:  runtimeVersion,
			CreatedAt:       helpers.NormalizeTimestamp(updateTimestamps[0]).Format(time.RFC3339),
			LastUpdatedAt:   helpers.NormalizeTimestamp(updateTimestamps[len(updateTimestamps)-1]).Format(time.RFC3339),
			NumberOfUpdates: len(updateTimestamps),
		})
	}

	return runtimeVersions, nil
}

// PutObject implements the audit archive's object write.
func (b *LocalBucket) PutObject(_ context.Context, key string, body []byte) error {
	// Same anti-traversal mechanism as the update assets (validatingBucket):
	// no "..", no absolute paths, no backslashes.
	if err := validateRelativePath("object key", key); err != nil {
		return err
	}
	filePath := filepath.Join(b.rootPath(), filepath.FromSlash(key))
	// Tighter than the update assets: the audit archive carries emails, IPs
	// and user agents, no other local account has business reading it.
	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		return err
	}
	return os.WriteFile(filePath, body, 0o600)
}

func (b *LocalBucket) UploadFileIntoUpdate(update types.Update, fileName string, file io.Reader) error {
	filePath := filepath.Join(b.rootPath(), update.AppId, update.Branch, update.RuntimeVersion, update.UpdateId, fileName)
	err := os.MkdirAll(filepath.Dir(filePath), os.ModePerm)
	if err != nil {
		return err
	}
	out, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, file)
	if err != nil {
		return err
	}
	return nil
}

func (b *LocalBucket) CopyFileIntoUpdate(source types.Update, target types.Update, fileName string) error {
	sourcePath := filepath.Join(b.rootPath(), source.AppId, source.Branch, source.RuntimeVersion, source.UpdateId, fileName)
	in, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer in.Close()
	return b.UploadFileIntoUpdate(target, fileName, in)
}

// GetSubjectForApp resolves the tamper-proof identity token subject (sub) based
// on the active runtime environment mode. If no relational database configuration
// is present, it defaults to the legacy dual-mode behavior by requesting the account
// owner's Expo username. In standalone deployments (indicated by a
// configured DB URL), it bypasses external third-party dependencies completely and
// returns a deterministic, app-scoped identifier prefixed with 'app:' to cleanly
// lock down token claims to that specific target binary application.
func GetSubjectForApp(appId string) string {
	isDBMode := config.IsDBMode()
	if !isDBMode {
		// Fetch expo username
		return expo.FetchSelfUsername(appId)
	}
	return fmt.Sprintf("app:%s", appId)
}

// uploadClaims is the claim set of a local upload URL token. Branch is what
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
// /{AppB}/uploadLocalFile?token=<appA_token>.
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

func (b *LocalBucket) blobPath(appId, hash string) string {
	return filepath.Join(b.rootPath(), appId, casDir, hash)
}

func (b *LocalBucket) BlobExists(_ context.Context, appId, hash string) (bool, error) {
	if b.BasePath == "" {
		return false, errors.New("BasePath not set")
	}
	_, err := os.Stat(b.blobPath(appId, hash))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (b *LocalBucket) GetBlob(_ context.Context, appId, hash string) (*types.BucketFile, error) {
	if b.BasePath == "" {
		return nil, errors.New("BasePath not set")
	}
	file, err := os.Open(b.blobPath(appId, hash))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	return &types.BucketFile{
		Reader:    file,
		CreatedAt: info.ModTime(),
	}, nil
}

func (b *LocalBucket) PutBlob(_ context.Context, appId, hash string, body io.Reader) error {
	if b.BasePath == "" {
		return errors.New("BasePath not set")
	}
	filePath := b.blobPath(appId, hash)
	if err := os.MkdirAll(filepath.Dir(filePath), os.ModePerm); err != nil {
		return err
	}
	out, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, body)
	return err
}

func (b *LocalBucket) RequestBlobUploadURL(appId, hash, branch string) (string, error) {
	if b.BasePath == "" {
		return "", errors.New("BasePath not set")
	}
	dirPath := filepath.Join(b.rootPath(), appId, casDir)
	if err := os.MkdirAll(dirPath, os.ModePerm); err != nil {
		return "", err
	}
	token, err := crypto.GenerateJWTToken(config.GetEnv("JWT_SECRET"), uploadClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   GetSubjectForApp(appId),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute * 10)),
		},
		FilePath: filepath.Join(dirPath, hash),
		Action:   "uploadLocalFile",
		AppID:    appId,
		Branch:   branch,
	})
	if err != nil {
		return "", err
	}
	parsedURL, err := url.Parse(config.GetEnv("BASE_URL"))
	if err != nil {
		return "", fmt.Errorf("invalid base URL: %w", err)
	}
	parsedURL.Path, err = url.JoinPath(parsedURL.Path, appId, "uploadLocalFile")
	if err != nil {
		return "", fmt.Errorf("error joining path: %w", err)
	}
	query := url.Values{}
	query.Set("token", token)
	parsedURL.RawQuery = query.Encode()
	return parsedURL.String(), nil
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

func HandleUploadFile(filePath string, body multipart.File) (bool, error) {
	err := os.MkdirAll(filepath.Dir(filePath), os.ModePerm)
	if err != nil {
		return false, err
	}
	file, err := os.Create(filePath)
	if err != nil {
		return false, err
	}
	defer file.Close()
	_, err = io.Copy(file, body)
	if err != nil {
		return false, err
	}
	return true, nil
}

func (b *LocalBucket) CreateUpdateFrom(previousUpdate *types.Update, newUpdateId string) (*types.Update, error) {
	if previousUpdate == nil {
		return nil, errors.New("previousUpdate is nil")
	}
	if previousUpdate.UpdateId == "" {
		return nil, errors.New("previousUpdate.UpdateId is empty")
	}
	if newUpdateId == "" {
		return nil, errors.New("newUpdateId is empty")
	}

	previousUpdatePath := filepath.Join(b.rootPath(), previousUpdate.AppId, previousUpdate.Branch, previousUpdate.RuntimeVersion, previousUpdate.UpdateId)
	newUpdatePath := filepath.Join(b.rootPath(), previousUpdate.AppId, previousUpdate.Branch, previousUpdate.RuntimeVersion, newUpdateId)

	err := os.MkdirAll(newUpdatePath, os.ModePerm)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(previousUpdatePath)
	if err != nil {
		return nil, err
	}

	var wg sync.WaitGroup
	errChan := make(chan error, len(entries))
	sem := make(chan struct{}, runtime.NumCPU())

	for _, entry := range entries {
		name := entry.Name()
		if name == "update-metadata.json" || name == ".check" {
			continue
		}

		srcPath := filepath.Join(previousUpdatePath, name)
		dstPath := filepath.Join(newUpdatePath, name)

		wg.Add(1)
		go func(entry fs.DirEntry, src, dst string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			var err error
			if entry.IsDir() {
				err = copyDirParallel(src, dst)
			} else {
				err = copyFile(src, dst)
			}
			if err != nil {
				errChan <- err
			}
		}(entry, srcPath, dstPath)
	}

	wg.Wait()
	close(errChan)

	for e := range errChan {
		if e != nil {
			return nil, e
		}
	}

	updateId, err := strconv.ParseInt(newUpdateId, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("error parsing update ID: %w", err)
	}
	return &types.Update{
		AppId:          previousUpdate.AppId,
		Branch:         previousUpdate.Branch,
		RuntimeVersion: previousUpdate.RuntimeVersion,
		UpdateId:       newUpdateId,
		CreatedAt:      helpers.NormalizeTimestampToDuration(updateId),
	}, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	if err != nil {
		return err
	}
	return out.Sync()
}

func (b *LocalBucket) GetInstanceID() (string, error) {
	if b.BasePath == "" {
		return "", errors.New("BasePath not set")
	}
	content, err := os.ReadFile(filepath.Join(b.rootPath(), ".instanceid"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(content)), nil
}

func (b *LocalBucket) PersistInstanceID(id string) error {
	if b.BasePath == "" {
		return errors.New("BasePath not set")
	}
	return os.WriteFile(filepath.Join(b.rootPath(), ".instanceid"), []byte(id+"\n"), 0644)
}

func (b *LocalBucket) RetrieveMigrationHistory() ([]string, error) {
	if b.BasePath == "" {
		return nil, errors.New("BasePath not set")
	}
	migrationHistoryPath := filepath.Join(b.rootPath(), ".migrationhistory")
	file, err := os.Open(migrationHistoryPath)
	if err != nil {
		return nil, nil
	}
	defer file.Close()
	var migrations []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		migrations = append(migrations, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return migrations, nil
}

func (b *LocalBucket) ApplyMigration(migrationId string) error {
	if b.BasePath == "" {
		return errors.New("BasePath not set")
	}

	migrationHistoryPath := filepath.Join(b.rootPath(), ".migrationhistory")

	migrations, err := b.RetrieveMigrationHistory()
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("RetrieveMigrationHistory error: %w", err)
	}
	for _, id := range migrations {
		if id == migrationId {
			return nil
		}
	}

	file, err := os.OpenFile(migrationHistoryPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open .migrationhistory error: %w", err)
	}
	defer file.Close()

	if _, err := file.WriteString(migrationId + "\n"); err != nil {
		return fmt.Errorf("write .migrationhistory error: %w", err)
	}

	return nil
}

func (b *LocalBucket) RemoveMigrationFromHistory(migrationId string) error {
	if b.BasePath == "" {
		return errors.New("BasePath not set")
	}

	migrationHistoryPath := filepath.Join(b.rootPath(), ".migrationhistory")

	migrations, err := b.RetrieveMigrationHistory()
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("RetrieveMigrationHistory error: %w", err)
	}
	hasMigration := false
	for _, id := range migrations {
		if id == migrationId {
			hasMigration = true
			break
		}
	}
	if !hasMigration {
		return nil
	}

	var newMigrations []string
	for _, id := range migrations {
		if id != migrationId {
			newMigrations = append(newMigrations, id)
		}
	}

	file, err := os.OpenFile(migrationHistoryPath, os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open .migrationhistory error: %w", err)
	}
	defer file.Close()

	for _, id := range newMigrations {
		if _, err := file.WriteString(id + "\n"); err != nil {
			return fmt.Errorf("write .migrationhistory error: %w", err)
		}
	}

	return nil
}

func copyDirParallel(srcDir, dstDir string) error {
	err := os.MkdirAll(dstDir, os.ModePerm)
	if err != nil {
		return err
	}

	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return err
	}

	var wg sync.WaitGroup
	errChan := make(chan error, len(entries))
	sem := make(chan struct{}, runtime.NumCPU())
	for _, entry := range entries {
		srcPath := filepath.Join(srcDir, entry.Name())
		dstPath := filepath.Join(dstDir, entry.Name())

		wg.Add(1)
		go func(entry fs.DirEntry, src, dst string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			var err error
			if entry.IsDir() {
				err = copyDirParallel(src, dst)
			} else {
				err = copyFile(src, dst)
			}
			if err != nil {
				errChan <- err
			}
		}(entry, srcPath, dstPath)
	}
	wg.Wait()
	close(errChan)
	for e := range errChan {
		if e != nil {
			return e
		}
	}
	return nil
}
