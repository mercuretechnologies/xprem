package bucket

import (
	"encoding/base64"
	"fmt"
	"log"
	"strings"
	"sync"
	"unicode"
	"xprem/config"
	"xprem/internal/objectstore"
	"xprem/internal/types"

	"github.com/google/uuid"
)

const (
	maxSegmentLen  = 128
	casDir         = "cas"
	bsDiffDir      = "bsdiff"
	blobHashLength = 43
)

var s3KeyPrefixDeprecationOnce sync.Once

// validateSegment accepts one identifier (branch, runtimeVersion, updateId,
// migrationId) as a single key segment.
func validateSegment(name, value string) error {
	if value == "" {
		return fmt.Errorf("invalid %s: must not be empty", name)
	}
	if len(value) > maxSegmentLen {
		return fmt.Errorf("invalid %s: exceeds max length %d", name, maxSegmentLen)
	}
	if strings.ContainsAny(value, "/\\") {
		return fmt.Errorf("invalid %s: must not contain path separators", name)
	}
	if value == "." || value == ".." {
		return fmt.Errorf("invalid %s: reserved name", name)
	}
	for _, r := range value {
		if r == 0x00 {
			return fmt.Errorf("invalid %s: must not contain null bytes", name)
		}
		if unicode.IsControl(r) {
			return fmt.Errorf("invalid %s: must not contain control characters", name)
		}
	}
	return nil
}

// validateRelativePath accepts a nested file path inside an update folder.
func validateRelativePath(name, value string) error {
	if err := objectstore.ValidateKey(value); err != nil {
		return fmt.Errorf("invalid %s: %w", name, err)
	}
	return nil
}

func validateBranch(branch string) error {
	if err := validateSegment("branch", branch); err != nil {
		return err
	}
	if ReservedBranchName(branch) {
		return fmt.Errorf("invalid branch: %q is reserved", branch)
	}
	return nil
}

func validateUpdate(u *types.Update) error {
	if u == nil {
		return fmt.Errorf("update must not be nil")
	}
	return validateUpdateRef(u.AppId, u.Branch, u.RuntimeVersion, u.UpdateId)
}

func validateUpdateRef(appId, branch, runtimeVersion, updateId string) error {
	if err := validateSegment("appId", appId); err != nil {
		return err
	}
	if err := validateBranch(branch); err != nil {
		return err
	}
	if err := validateSegment("runtimeVersion", runtimeVersion); err != nil {
		return err
	}
	return validateSegment("updateId", updateId)
}

// validateUpdateUUID accepts only the canonical lowercase spelling, so one
// update cannot own two patch keys.
func validateUpdateUUID(name, value string) error {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.String() != value {
		return fmt.Errorf("invalid %s: must be a canonical lowercase UUID", name)
	}
	return nil
}

func ValidateBlobHash(hash string) error {
	if len(hash) != blobHashLength {
		return fmt.Errorf("invalid hash: must be %d characters", blobHashLength)
	}
	// Strict rejects spellings with non-zero trailing padding bits, which
	// decode to the same digest but would mint a second CAS key.
	if _, err := base64.RawURLEncoding.Strict().DecodeString(hash); err != nil {
		return fmt.Errorf("invalid hash: must be canonical base64url")
	}
	return nil
}

func ValidateUploadFile(name, hash string) error {
	if err := validateRelativePath("file name", name); err != nil {
		return err
	}
	return ValidateBlobHash(hash)
}

func ReservedBranchName(branch string) bool {
	return branch == casDir || branch == bsDiffDir
}

func updatePrefix(appId, branch, runtimeVersion, updateId string) string {
	return appId + "/" + branch + "/" + runtimeVersion + "/" + updateId + "/"
}

// BlobObjectKey is {appId}/cas/{hash}, without the bucket key prefix.
func BlobObjectKey(appId, hash string) string {
	return appId + "/" + casDir + "/" + hash
}

// BSDiffBranchPrefix is {appId}/bsdiff/{branch}/, under which every patch of
// the branch lives. Update ids are only unique within a branch.
func BSDiffBranchPrefix(appId, branch string) string {
	return appId + "/" + bsDiffDir + "/" + branch + "/"
}

// BSDiffObjectKey is {appId}/bsdiff/{branch}/{targetUpdateUUID}/{sourceUpdateUUID}:
// the patch that turns the source update's bundle into the target's. The
// source UUID is the last segment so a CDN edge can echo it as the
// expo-base-update-id header.
func BSDiffObjectKey(appId, branch, targetUpdateUUID, sourceUpdateUUID string) string {
	return BSDiffBranchPrefix(appId, branch) + targetUpdateUUID + "/" + sourceUpdateUUID
}

// ResolveKeyPrefix returns the bucket key prefix, normalized to end with "/"
// when non-empty. It reads BUCKET_KEY_PREFIX first and falls back to the
// legacy S3_KEY_PREFIX env var. Panics on unsafe values (absolute paths or
// ".." segments) to fail-fast on operator misconfiguration that could let
// the local backend escape its BasePath.
//
// Exported because the CDN builders need the same prefix when signing
// object URLs, a CloudFront or GCS-direct URL that omits the prefix
// points to a non-existent object and 404s.
func ResolveKeyPrefix() string {
	prefix := config.GetEnv("BUCKET_KEY_PREFIX")
	if prefix == "" {
		// TODO: remove S3_KEY_PREFIX backward-compat once users migrated to BUCKET_KEY_PREFIX
		prefix = config.GetEnv("S3_KEY_PREFIX")
		if prefix != "" {
			s3KeyPrefixDeprecationOnce.Do(func() {
				log.Println("WARNING: S3_KEY_PREFIX is deprecated and will be removed in a future release; use BUCKET_KEY_PREFIX instead")
			})
		}
	}
	if prefix == "" {
		return ""
	}
	if strings.ContainsRune(prefix, '\\') {
		panic("bucket key prefix must not contain '\\' characters")
	}
	if strings.HasPrefix(prefix, "/") {
		panic("bucket key prefix must not be absolute (starts with '/')")
	}
	for _, seg := range strings.Split(prefix, "/") {
		if seg == ".." {
			panic("bucket key prefix must not contain '..' segments")
		}
	}
	if prefix[len(prefix)-1] != '/' {
		prefix += "/"
	}
	return prefix
}
