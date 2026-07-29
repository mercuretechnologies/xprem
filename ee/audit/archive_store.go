// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package audit

import (
	"errors"
	"fmt"
	"path/filepath"
	"xprem/config"
	"xprem/internal/bucket"
)

// GetAuditLogsObjectStore resolves the dedicated audit archive destination
// for the deployment's STORAGE_MODE. Each provider has its own variable
// (S3_BUCKET_AUDIT_LOGS_NAME, GCS_BUCKET_AUDIT_LOGS_NAME,
// AZURE_BLOB_AUDIT_LOGS_CONTAINER_NAME, LOCAL_AUDIT_LOGS_BASE_PATH), and the
// destination must differ from the updates bucket: reusing it would put
// security-sensitive logs behind the update assets' access rules. The provider
// structs live in internal/bucket (generic storage capability); knowing which
// destination the audit archive uses is enterprise logic, so it lives here.
func GetAuditLogsObjectStore() (ObjectPutter, error) {
	switch bucket.ResolveBucketType() {
	case bucket.S3BucketType:
		name := config.GetEnv("S3_BUCKET_AUDIT_LOGS_NAME")
		if name == "" {
			return nil, errors.New("audit archiving is enabled but S3_BUCKET_AUDIT_LOGS_NAME is not set")
		}
		if name == config.GetEnv("S3_BUCKET_NAME") {
			return nil, errors.New("S3_BUCKET_AUDIT_LOGS_NAME must be a dedicated bucket, not the updates bucket (S3_BUCKET_NAME)")
		}
		return &bucket.S3Bucket{BucketName: name}, nil
	case bucket.GCSBucketType:
		name := config.GetEnv("GCS_BUCKET_AUDIT_LOGS_NAME")
		if name == "" {
			return nil, errors.New("audit archiving is enabled but GCS_BUCKET_AUDIT_LOGS_NAME is not set")
		}
		if name == config.GetEnv("GCS_BUCKET_NAME") {
			return nil, errors.New("GCS_BUCKET_AUDIT_LOGS_NAME must be a dedicated bucket, not the updates bucket (GCS_BUCKET_NAME)")
		}
		return &bucket.GCSBucket{BucketName: name}, nil
	case bucket.AzureBucketType:
		name := config.GetEnv("AZURE_BLOB_AUDIT_LOGS_CONTAINER_NAME")
		if name == "" {
			return nil, errors.New("audit archiving is enabled but AZURE_BLOB_AUDIT_LOGS_CONTAINER_NAME is not set")
		}
		if name == config.GetEnv("AZURE_BLOB_CONTAINER_NAME") {
			return nil, errors.New("AZURE_BLOB_AUDIT_LOGS_CONTAINER_NAME must be a dedicated container, not the updates container (AZURE_BLOB_CONTAINER_NAME)")
		}
		return &bucket.AzureBucket{ContainerName: name}, nil
	default:
		path := config.GetEnv("LOCAL_AUDIT_LOGS_BASE_PATH")
		updatesPath := config.GetEnv("LOCAL_BUCKET_BASE_PATH")
		if samePath(path, updatesPath) {
			return nil, fmt.Errorf("LOCAL_AUDIT_LOGS_BASE_PATH (%s) must be a dedicated directory, not the updates directory (LOCAL_BUCKET_BASE_PATH)", path)
		}
		return &bucket.LocalBucket{BasePath: path}, nil
	}
}

func samePath(a string, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return a == b
	}
	return absA == absB
}
