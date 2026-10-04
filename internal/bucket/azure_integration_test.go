package bucket

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
	"xprem/internal/objectstore"
	azureprovider "xprem/internal/providers/azure"
	"xprem/internal/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This suite runs the Azure backend against an Azurite emulator
// (docker compose up azurite) and skips when TEST_AZURE_BLOB_ENDPOINT is
// not set, mirroring how the database tests gate on TEST_DATABASE_URL.
func setupAzuriteBucket(t *testing.T) *Bucket {
	endpoint := os.Getenv("TEST_AZURE_BLOB_ENDPOINT")
	if endpoint == "" {
		t.Skip("TEST_AZURE_BLOB_ENDPOINT not set; skipping Azurite integration tests")
	}
	t.Setenv("AZURE_BLOB_ENDPOINT", endpoint)
	t.Setenv("AZURE_STORAGE_ACCOUNT_NAME", "devstoreaccount1")
	t.Setenv("AZURE_STORAGE_ACCOUNT_KEY", "Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw==")

	client, err := azureprovider.GetClient()
	require.NoError(t, err)
	// One container per test: no cross-test pollution and nothing to clean
	// between assertions.
	containerName := fmt.Sprintf("it-%d", time.Now().UnixNano())
	_, err = client.CreateContainer(context.Background(), containerName, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteContainer(context.Background(), containerName, nil)
	})
	return Open(objectstore.ModeAzure, containerName, "")
}

func azuriteUpload(t *testing.T, b *Bucket, u types.Update, name, content string) {
	t.Helper()
	require.NoError(t, b.UpdateStore.PutFile(context.Background(), u, name, strings.NewReader(content)))
}

func TestAzuriteUploadListGet(t *testing.T) {
	b := setupAzuriteBucket(t)
	u1 := types.Update{AppId: "app-1", Branch: "production", RuntimeVersion: "1", UpdateId: "1674170951"}
	u2 := types.Update{AppId: "app-1", Branch: "production", RuntimeVersion: "1", UpdateId: "1674170952"}
	u3 := types.Update{AppId: "app-1", Branch: "staging", RuntimeVersion: "2", UpdateId: "1674170953"}
	unchecked := types.Update{AppId: "app-1", Branch: "production", RuntimeVersion: "1", UpdateId: "1674170954"}

	azuriteUpload(t, b, u1, "bundles/android.js", "bundle-1")
	azuriteUpload(t, b, u1, ".check", "")
	azuriteUpload(t, b, u1, "metadata.json", "{}")
	azuriteUpload(t, b, u2, "bundles/android.js", "bundle-2")
	azuriteUpload(t, b, u2, ".check", "")
	azuriteUpload(t, b, u3, "bundles/ios.js", "bundle-3")
	azuriteUpload(t, b, u3, ".check", "")
	// An update folder without its .check marker is still being uploaded
	// and must not appear in the stats.
	azuriteUpload(t, b, unchecked, "bundles/android.js", "partial")

	branches, err := b.UpdateStore.Branches(context.Background(), "app-1")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"production", "staging"}, branches)

	runtimeVersions, err := b.UpdateStore.RuntimeVersions(context.Background(), "app-1", "production")
	require.NoError(t, err)
	require.Len(t, runtimeVersions, 1)
	assert.Equal(t, "1", runtimeVersions[0].RuntimeVersion)
	assert.Equal(t, 2, runtimeVersions[0].NumberOfUpdates)

	updates, err := b.UpdateStore.List(context.Background(), "app-1", "production", "1")
	require.NoError(t, err)
	assert.Len(t, updates, 3)

	file, err := b.UpdateStore.GetFile(context.Background(), u1, "bundles/android.js")
	require.NoError(t, err)
	require.NotNil(t, file)
	content, err := io.ReadAll(file.Reader)
	require.NoError(t, err)
	assert.Equal(t, "bundle-1", string(content))
	assert.False(t, file.CreatedAt.IsZero())

	missing, err := b.UpdateStore.GetFile(context.Background(), u1, "does-not-exist.js")
	require.NoError(t, err)
	assert.Nil(t, missing)
}

func TestAzuriteSASUploadRequiresBlockBlobHeader(t *testing.T) {
	b := setupAzuriteBucket(t)
	upload, err := b.UpdateStore.PresignPut(context.Background(), "app-1", "production", "1", "1674170951", "bundles/android.js")
	require.NoError(t, err)

	// Without the header Azure refuses the PUT: the exact failure eoas hit
	// before the server started returning upload headers.
	reqWithoutHeader, err := http.NewRequest(http.MethodPut, upload.URL, strings.NewReader("payload"))
	require.NoError(t, err)
	respWithoutHeader, err := http.DefaultClient.Do(reqWithoutHeader)
	require.NoError(t, err)
	respWithoutHeader.Body.Close()
	assert.Equal(t, http.StatusBadRequest, respWithoutHeader.StatusCode)

	req, err := http.NewRequest(http.MethodPut, upload.URL, strings.NewReader("payload"))
	require.NoError(t, err)
	for name, value := range upload.Headers {
		req.Header.Set(name, value)
	}
	req.Header.Set("Content-Type", "application/javascript")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusCreated, resp.StatusCode)

	update := types.Update{AppId: "app-1", Branch: "production", RuntimeVersion: "1", UpdateId: "1674170951"}
	file, err := b.UpdateStore.GetFile(context.Background(), update, "bundles/android.js")
	require.NoError(t, err)
	require.NotNil(t, file)
	content, err := io.ReadAll(file.Reader)
	require.NoError(t, err)
	assert.Equal(t, "payload", string(content))
}

func TestAzuriteCreateFrom(t *testing.T) {
	b := setupAzuriteBucket(t)
	source := types.Update{AppId: "app-1", Branch: "production", RuntimeVersion: "1", UpdateId: "1674170951"}
	azuriteUpload(t, b, source, "bundles/android.js", "bundle")
	azuriteUpload(t, b, source, "assets/logo.png", "png-bytes")
	azuriteUpload(t, b, source, ".check", "")
	azuriteUpload(t, b, source, "update-metadata.json", "{}")

	newUpdate, err := b.UpdateStore.CreateFrom(context.Background(), &source, "1674170999")
	require.NoError(t, err)
	require.NotNil(t, newUpdate)
	assert.Equal(t, "1674170999", newUpdate.UpdateId)

	copied, err := b.UpdateStore.GetFile(context.Background(), *newUpdate, "bundles/android.js")
	require.NoError(t, err)
	require.NotNil(t, copied)
	content, err := io.ReadAll(copied.Reader)
	require.NoError(t, err)
	assert.Equal(t, "bundle", string(content))

	copiedAsset, err := b.UpdateStore.GetFile(context.Background(), *newUpdate, "assets/logo.png")
	require.NoError(t, err)
	require.NotNil(t, copiedAsset)

	// Markers are deliberately not copied: the republished update gets its
	// own .check and metadata once finalized.
	check, err := b.UpdateStore.GetFile(context.Background(), *newUpdate, ".check")
	require.NoError(t, err)
	assert.Nil(t, check)
	metadata, err := b.UpdateStore.GetFile(context.Background(), *newUpdate, "update-metadata.json")
	require.NoError(t, err)
	assert.Nil(t, metadata)
}

func TestAzuriteDeleteUpdate(t *testing.T) {
	b := setupAzuriteBucket(t)
	doomed := types.Update{AppId: "app-1", Branch: "production", RuntimeVersion: "1", UpdateId: "1674170951"}
	sibling := types.Update{AppId: "app-1", Branch: "production", RuntimeVersion: "1", UpdateId: "1674170952"}
	azuriteUpload(t, b, doomed, "bundles/android.js", "bundle")
	azuriteUpload(t, b, doomed, "assets/logo.png", "png-bytes")
	azuriteUpload(t, b, sibling, "bundles/android.js", "kept")

	require.NoError(t, b.UpdateStore.Delete(context.Background(), "app-1", "production", "1", "1674170951"))

	deleted, err := b.UpdateStore.GetFile(context.Background(), doomed, "bundles/android.js")
	require.NoError(t, err)
	assert.Nil(t, deleted)
	kept, err := b.UpdateStore.GetFile(context.Background(), sibling, "bundles/android.js")
	require.NoError(t, err)
	assert.NotNil(t, kept)
}
