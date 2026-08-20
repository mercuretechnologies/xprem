package bucket

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"xprem/config"
	"xprem/internal/types"
)

var s3KeyPrefixDeprecationOnce sync.Once

// maxSegmentLen bounds any single path segment (branch, runtimeVersion,
// updateId, migrationId). Keeps DoS surface small on map keys and
// filesystem paths while staying comfortably above realistic names
// (UUIDs are 36, semver+build metadata under 100).
const maxSegmentLen = 128

// copyFileTimeout bounds a single CopyFileIntoUpdate provider call, so a
// stalled copy degrades into a regular upload instead of hanging the publish.
const copyFileTimeout = 30 * time.Second

// validateSegment ensures a single-segment identifier (branch, runtimeVersion,
// updateId, migrationId) is safe to embed in a storage path / object key.
// Defense-in-depth against path traversal on the local backend and weird
// keys on S3/GCS. Rejects empties, path separators, "." / "..", null bytes,
// control characters, and anything over maxSegmentLen.
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
	// Null bytes truncate keys in C-based filesystem syscalls; control
	// characters break URL encoding / logging / key listing on S3/GCS.
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

// validateRelativePath validates multi-segment paths supplied for fileName /
// assetPath. Nested paths are allowed (e.g. "assets/image.png") but no
// absolute paths and no ".." segments. Backslashes are rejected outright -
// on Windows filepath.Join treats them as separators, so allowing them would
// let an attacker escape the intended directory via a path like
// "assets\..\..\etc\passwd".
func validateRelativePath(name, value string) error {
	if value == "" {
		return fmt.Errorf("invalid %s: must not be empty", name)
	}
	if strings.ContainsRune(value, '\\') {
		return fmt.Errorf("invalid %s: must not contain '\\' characters", name)
	}
	if strings.HasPrefix(value, "/") {
		return fmt.Errorf("invalid %s: must not be absolute", name)
	}
	for _, seg := range strings.Split(value, "/") {
		if seg == ".." {
			return fmt.Errorf("invalid %s: must not contain '..' segments", name)
		}
	}
	return nil
}

func validateUpdate(u *types.Update) error {
	if u == nil {
		return fmt.Errorf("update must not be nil")
	}
	if err := validateSegment("appId", u.AppId); err != nil {
		return err
	}
	if err := validateSegment("branch", u.Branch); err != nil {
		return err
	}
	if err := validateSegment("runtimeVersion", u.RuntimeVersion); err != nil {
		return err
	}
	if err := validateSegment("updateId", u.UpdateId); err != nil {
		return err
	}
	return nil
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
	return resolveKeyPrefix()
}

func resolveKeyPrefix() string {
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

type Bucket interface {
	GetBranches(appId string) ([]string, error)
	GetRuntimeVersions(appId string, branch string) ([]types.RuntimeVersionWithStats, error)
	GetUpdates(appId string, branch string, runtimeVersion string) ([]types.Update, error)
	GetFile(update types.Update, assetPath string) (*types.BucketFile, error)
	RequestUploadUrlForFileUpdate(appId string, branch string, runtimeVersion string, updateId string, fileName string) (string, error)
	UploadFileIntoUpdate(update types.Update, fileName string, file io.Reader) error
	CopyFileIntoUpdate(source types.Update, target types.Update, fileName string) error
	DeleteUpdateFolder(appId string, branch string, runtimeVersion string, updateId string) error
	CreateUpdateFrom(previousUpdate *types.Update, newUpdateId string) (*types.Update, error)
	RetrieveMigrationHistory() ([]string, error)
	ApplyMigration(migrationId string) error
	RemoveMigrationFromHistory(migrationId string) error
	GetInstanceID() (string, error)
	PersistInstanceID(id string) error
}

type BucketType string

const (
	S3BucketType    BucketType = "s3"
	LocalBucketType BucketType = "local"
	GCSBucketType   BucketType = "gcs"
	AzureBucketType BucketType = "azure"
)

func ResolveBucketType() BucketType {
	storageMode := config.GetEnv("STORAGE_MODE")
	switch storageMode {
	case "local", "":
		return LocalBucketType
	case "s3":
		return S3BucketType
	case "gcs":
		return GCSBucketType
	case "azure":
		return AzureBucketType
	default:
		return LocalBucketType
	}
}

var (
	bucketInstance Bucket
	once           sync.Once
)

func GetBucket() Bucket {
	once.Do(func() {
		if bucketInstance == nil {
			bucketType := ResolveBucketType()
			keyPrefix := resolveKeyPrefix()
			var inner Bucket
			switch bucketType {
			case S3BucketType:
				inner = &S3Bucket{
					BucketName: config.GetEnv("S3_BUCKET_NAME"),
					KeyPrefix:  keyPrefix,
				}
			case GCSBucketType:
				inner = &GCSBucket{
					BucketName: config.GetEnv("GCS_BUCKET_NAME"),
					KeyPrefix:  keyPrefix,
				}
			case AzureBucketType:
				inner = &AzureBucket{
					ContainerName: config.GetEnv("AZURE_BLOB_CONTAINER_NAME"),
					KeyPrefix:     keyPrefix,
				}
			case LocalBucketType:
				inner = &LocalBucket{
					BasePath:  config.GetEnv("LOCAL_BUCKET_BASE_PATH"),
					KeyPrefix: keyPrefix,
				}
			default:
				panic(fmt.Sprintf("Unknown bucket type: %s", bucketType))
			}
			bucketInstance = &validatingBucket{Inner: inner}
		}
	})
	return bucketInstance
}

func ConvertReadCloserToBytes(rc io.ReadCloser) ([]byte, error) {
	defer rc.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, rc); err != nil {
		return nil, fmt.Errorf("error copying file to buffer: %w", err)
	}
	return buf.Bytes(), nil
}

func ResetBucketInstance() {
	bucketInstance = nil
	once = sync.Once{}
}

type FileUploadRequest struct {
	RequestUploadUrl string `json:"requestUploadUrl"`
	FileName         string `json:"fileName"`
	FilePath         string `json:"filePath"`
	// Headers must be sent verbatim by the uploader on its PUT to
	// RequestUploadUrl. Azure Put Blob rejects requests missing
	// x-ms-blob-type, and carrying the requirement in the response keeps
	// the CLI provider-agnostic. Absent for the other backends.
	Headers map[string]string `json:"headers,omitempty"`
}

func RequestUploadUrlsForFileUpdates(appId string, branch string, runtimeVersion string, updateId string, fileNames []string) ([]FileUploadRequest, error) {
	uniqueFileNames := make(map[string]struct{})
	for _, fileName := range fileNames {
		uniqueFileNames[fileName] = struct{}{}
	}

	bucket := GetBucket()
	var uploadHeaders map[string]string
	if _, ok := UnwrapBucket(bucket).(*AzureBucket); ok {
		uploadHeaders = map[string]string{"x-ms-blob-type": "BlockBlob"}
	}

	var requests []FileUploadRequest
	var mu sync.Mutex
	var wg sync.WaitGroup
	errChan := make(chan error, len(uniqueFileNames))

	wg.Add(len(uniqueFileNames))
	for fileName := range uniqueFileNames {
		go func(fileName string) {
			defer wg.Done()
			requestUploadUrl, err := bucket.RequestUploadUrlForFileUpdate(appId, branch, runtimeVersion, updateId, fileName)
			if err != nil {
				errChan <- err
				return
			}
			mu.Lock()
			requests = append(requests, FileUploadRequest{
				RequestUploadUrl: requestUploadUrl,
				FileName:         filepath.Base(fileName),
				FilePath:         fileName,
				Headers:          uploadHeaders,
			})
			mu.Unlock()
		}(fileName)
	}

	wg.Wait()
	close(errChan)

	if len(errChan) > 0 {
		return nil, <-errChan
	}

	return requests, nil
}
