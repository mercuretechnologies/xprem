// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package audit

import (
	"context"
	"expo-open-ota/internal/bucket"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuditLogsObjectStoreLocalRoundtrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_AUDIT_LOGS_BASE_PATH", dir)

	store, err := GetAuditLogsObjectStore()
	require.NoError(t, err)

	require.NoError(t, store.PutObject(context.Background(),
		"2026/07/22/1-5.ndjson", []byte("{\"stream\":\"audit\"}\n")))

	written, err := os.ReadFile(filepath.Join(dir, "2026", "07", "22", "1-5.ndjson"))
	require.NoError(t, err)
	assert.Equal(t, "{\"stream\":\"audit\"}\n", string(written))
}

func TestAuditLogsObjectStoreRefusesEscapingKeys(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_AUDIT_LOGS_BASE_PATH", dir)

	store, err := GetAuditLogsObjectStore()
	require.NoError(t, err)

	err = store.PutObject(context.Background(), "../escape.ndjson", []byte("x"))
	require.ErrorContains(t, err, "invalid object key")
	_, statErr := os.Stat(filepath.Join(filepath.Dir(dir), "escape.ndjson"))
	require.True(t, os.IsNotExist(statErr))
}

func TestAuditLogsObjectStoreRefusesTheUpdatesDestination(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_BUCKET_BASE_PATH", dir)
	t.Setenv("LOCAL_AUDIT_LOGS_BASE_PATH", dir)
	_, err := GetAuditLogsObjectStore()
	require.ErrorContains(t, err, "dedicated directory")

	t.Setenv("STORAGE_MODE", "s3")
	t.Setenv("S3_BUCKET_NAME", "updates")
	t.Setenv("S3_BUCKET_AUDIT_LOGS_NAME", "updates")
	_, err = GetAuditLogsObjectStore()
	require.ErrorContains(t, err, "dedicated bucket")
}

func TestAuditLogsObjectStoreRequiresADestinationName(t *testing.T) {
	t.Setenv("STORAGE_MODE", "s3")
	t.Setenv("S3_BUCKET_AUDIT_LOGS_NAME", "")
	_, err := GetAuditLogsObjectStore()
	require.ErrorContains(t, err, "S3_BUCKET_AUDIT_LOGS_NAME is not set")
}

func TestAuditLogsObjectStoreIgnoresTheUpdatesKeyPrefix(t *testing.T) {
	t.Setenv("BUCKET_KEY_PREFIX", "tenant-a/")

	t.Setenv("STORAGE_MODE", "s3")
	t.Setenv("S3_BUCKET_NAME", "updates")
	t.Setenv("S3_BUCKET_AUDIT_LOGS_NAME", "audit")
	store, err := GetAuditLogsObjectStore()
	require.NoError(t, err)
	s3Store, ok := store.(*bucket.S3Bucket)
	require.True(t, ok)
	assert.Equal(t, "audit", s3Store.BucketName)
	assert.Empty(t, s3Store.KeyPrefix)

	t.Setenv("STORAGE_MODE", "gcs")
	t.Setenv("GCS_BUCKET_NAME", "updates")
	t.Setenv("GCS_BUCKET_AUDIT_LOGS_NAME", "audit")
	store, err = GetAuditLogsObjectStore()
	require.NoError(t, err)
	gcsStore, ok := store.(*bucket.GCSBucket)
	require.True(t, ok)
	assert.Empty(t, gcsStore.KeyPrefix)

	t.Setenv("STORAGE_MODE", "azure")
	t.Setenv("AZURE_BLOB_CONTAINER_NAME", "updates")
	t.Setenv("AZURE_BLOB_AUDIT_LOGS_CONTAINER_NAME", "audit")
	store, err = GetAuditLogsObjectStore()
	require.NoError(t, err)
	azureStore, ok := store.(*bucket.AzureBucket)
	require.True(t, ok)
	assert.Empty(t, azureStore.KeyPrefix)
}
