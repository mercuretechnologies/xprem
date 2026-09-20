package bucket

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"xprem/config"
)

var s3KeyPrefixDeprecationOnce sync.Once

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

// Bucket composes the storage features supported by every backend.
type Bucket interface {
	UpdateStorage
	BlobStorage
	BSDiffStorage
	BuildArtifactStorage
	BuildCacheStorage
	MigrationStorage
	InstanceStorage
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

const LocalUploadTokenHeader = "local-upload-token"

// UploadRequest describes a PUT, including any per-file authorization headers.
type UploadRequest struct {
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers,omitempty"`
}
