package bucket

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"xprem/config"
	"xprem/internal/providers/expo"
	"xprem/internal/types"
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

func (b *LocalBucket) fileExists(filePath string) (bool, error) {
	if b.BasePath == "" {
		return false, errors.New("BasePath not set")
	}
	_, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// openFile returns nil, nil when the file does not exist.
func (b *LocalBucket) openFile(filePath string) (*types.BucketFile, error) {
	if b.BasePath == "" {
		return nil, errors.New("BasePath not set")
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

func (b *LocalBucket) writeFile(filePath string, body io.Reader) error {
	if b.BasePath == "" {
		return errors.New("BasePath not set")
	}
	return writeFileAtomically(filePath, body, "")
}

// writeFileAtomically streams body into a temporary file next to target and
// renames it into place, so an interrupted write leaves nothing at target. A
// non-empty expectedHash must equal the base64url SHA-256 of the body.
func writeFileAtomically(target string, body io.Reader, expectedHash string) error {
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".upload-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	digest := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(tmp, digest), body)
	syncErr := tmp.Sync()
	closeErr := tmp.Close()
	if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
		return err
	}
	if expectedHash != "" && base64.RawURLEncoding.EncodeToString(digest.Sum(nil)) != expectedHash {
		return ErrBlobHashMismatch
	}
	return os.Rename(tmp.Name(), target)
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
