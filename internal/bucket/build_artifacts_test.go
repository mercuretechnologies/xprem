package bucket

import (
	"context"
	"encoding/base64"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"xprem/internal/objectstore"
	"xprem/internal/types"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

const (
	testIdentifierID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	testBuildID      = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
)

func testArtifact() BuildArtifact {
	return BuildArtifact{IdentifierID: testIdentifierID, BuildID: testBuildID, Type: types.BuildArtifactAPK}
}

// buildArtifactEnv opens the build store over its own temporary directory,
// under keyPrefix, and returns that directory.
func buildArtifactEnv(t *testing.T, keyPrefix string) (*BuildArtifactStore, string) {
	t.Helper()
	localUploadEnv(t)
	dir := t.TempDir()
	t.Setenv("LOCAL_BUILDS_BASE_PATH", dir)
	t.Setenv("BUCKET_KEY_PREFIX", keyPrefix)
	store, err := OpenBuildArtifactStore()
	require.NoError(t, err)
	return store, dir
}

// unreachableObjectStore panics on any call.
type unreachableObjectStore struct{ objectstore.Store }

func TestBuildArtifactStoreLocation(t *testing.T) {
	t.Run("local builds stay private inside the updates directory by default", func(t *testing.T) {
		updatesDir := localUploadEnv(t)
		t.Setenv("LOCAL_BUILDS_BASE_PATH", "")
		store, err := OpenBuildArtifactStore()
		require.NoError(t, err)
		require.True(t, store.SharesUpdatesLocation())
		require.NoError(t, store.Put(context.Background(), testArtifact(), false, strings.NewReader("apk")))
		folder, err := os.Stat(filepath.Join(updatesDir, BuildsPrefix))
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o700), folder.Mode().Perm())
		key, err := testArtifact().Key(false)
		require.NoError(t, err)
		file, err := os.Stat(filepath.Join(updatesDir, filepath.FromSlash(key)))
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), file.Mode().Perm())
	})
	t.Run("local builds use their own directory when set", func(t *testing.T) {
		store, dir := buildArtifactEnv(t, "")
		require.False(t, store.SharesUpdatesLocation())
		require.NoError(t, store.Put(context.Background(), testArtifact(), false, strings.NewReader("apk")))
		require.DirExists(t, filepath.Join(dir, BuildsPrefix))
	})
	t.Setenv("STORAGE_MODE", "s3")
	t.Setenv("S3_BUCKET_NAME", "updates")
	t.Setenv("BUCKET_KEY_PREFIX", "")
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "build-test-access")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "build-test-secret")
	t.Setenv("AWS_BASE_ENDPOINT", "")
	t.Setenv("AWS_S3_FORCE_PATH_STYLE", "false")
	for _, tc := range []struct {
		name, buildsBucket, host string
		sharesUpdates            bool
	}{
		{"cloud builds default to the updates bucket", "", "updates.s3.us-east-1.amazonaws.com", true},
		{"cloud builds use their own bucket when set", "builds", "builds.s3.us-east-1.amazonaws.com", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("S3_BUCKET_BUILDS_NAME", tc.buildsBucket)
			store, err := OpenBuildArtifactStore()
			require.NoError(t, err)
			require.Equal(t, tc.sharesUpdates, store.SharesUpdatesLocation())
			upload, err := store.PresignPut(context.Background(), "app-1", testArtifact())
			require.NoError(t, err)
			signedURL, err := url.Parse(upload.URL)
			require.NoError(t, err)
			require.Equal(t, tc.host, signedURL.Host)
		})
	}
}

func TestRequestBuildArtifactUpload(t *testing.T) {
	localStore, _ := buildArtifactEnv(t, "")
	t.Setenv("BASE_URL", "https://ota.example.com/sub/path/")
	t.Setenv("JWT_SECRET", "build-upload-test-secret")
	t.Setenv("AZURE_STORAGE_ACCOUNT_NAME", "buildtest")
	t.Setenv("AZURE_STORAGE_ACCOUNT_KEY", base64.StdEncoding.EncodeToString([]byte("build-upload-test-key")))
	t.Setenv("AZURE_BLOB_ENDPOINT", "")
	for _, tc := range []struct {
		name  string
		store *BuildArtifactStore
	}{
		{"local", localStore},
		{"azure", &BuildArtifactStore{objectStore: objectstore.WithPrefix(objectstore.Open(objectstore.ModeAzure, "artifacts"), "prefix/")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := time.Now()
			upload, err := tc.store.PresignPut(context.Background(), "app-1", testArtifact())
			require.NoError(t, err)
			require.Equal(t, "PUT", upload.Method)
			if tc.name == "local" {
				require.Equal(t, "https://ota.example.com/sub/path/app-1/build/"+testIdentifierID+"/artifacts/"+testBuildID+"/upload", upload.URL)
				require.Len(t, upload.Headers, 1)
				require.NoError(t, ValidateBuildUploadToken(upload.Headers[LocalUploadTokenHeader], "app-1", testIdentifierID, testBuildID))
				claims := jwt.MapClaims{}
				_, err := jwt.ParseWithClaims(upload.Headers[LocalUploadTokenHeader], claims, func(*jwt.Token) (any, error) {
					return []byte("build-upload-test-secret"), nil
				}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired(), jwt.WithSubject("build-upload"))
				require.NoError(t, err)
				require.Equal(t, "app-1", claims["appId"])
				require.Equal(t, testIdentifierID, claims["identifierId"])
				require.Equal(t, testBuildID, claims["buildId"])
				expiresAt, err := claims.GetExpirationTime()
				require.NoError(t, err)
				require.GreaterOrEqual(t, expiresAt.Unix(), before.Add(10*time.Minute).Unix())
				require.LessOrEqual(t, expiresAt.Unix(), time.Now().Add(10*time.Minute).Unix())
			} else {
				require.Equal(t, map[string]string{"x-ms-blob-type": "BlockBlob"}, upload.Headers)
				signedURL, err := url.Parse(upload.URL)
				require.NoError(t, err)
				require.Equal(t, "/artifacts/prefix/builds/android/"+testIdentifierID+"/.uploads/"+testBuildID+".apk", signedURL.Path)
				require.NotEmpty(t, signedURL.Query().Get("sig"))
				require.Equal(t, "cw", signedURL.Query().Get("sp"))
			}

			upload, err = tc.store.PresignPut(context.Background(), "app-1", BuildArtifact{})
			require.Error(t, err)
			require.Nil(t, upload, "validation failures do not return an upload descriptor")
		})
	}
}

func TestValidateBuildUploadTokenRejectsInvalidGrants(t *testing.T) {
	t.Setenv("JWT_SECRET", "build-upload-test-secret")
	require.Error(t, ValidateBuildUploadToken("not-a-token", "app-1", testIdentifierID, testBuildID))
	for name, tc := range map[string]struct {
		change func(jwt.MapClaims)
		method jwt.SigningMethod
		secret string
	}{
		"expired":            {change: func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-time.Minute).Unix() }},
		"missing expiration": {change: func(c jwt.MapClaims) { delete(c, "exp") }},
		"wrong subject":      {change: func(c jwt.MapClaims) { c["sub"] = "uploadLocalFile" }},
		"missing subject":    {change: func(c jwt.MapClaims) { delete(c, "sub") }},
		"another app":        {change: func(c jwt.MapClaims) { c["appId"] = "app-2" }},
		"another identifier": {change: func(c jwt.MapClaims) { c["identifierId"] = testBuildID }},
		"another build":      {change: func(c jwt.MapClaims) { c["buildId"] = testIdentifierID }},
		"missing app":        {change: func(c jwt.MapClaims) { delete(c, "appId") }},
		"missing identifier": {change: func(c jwt.MapClaims) { delete(c, "identifierId") }},
		"missing build":      {change: func(c jwt.MapClaims) { delete(c, "buildId") }},
		"wrong algorithm":    {method: jwt.SigningMethodHS384},
		"wrong secret":       {secret: "other-secret"},
	} {
		t.Run(name, func(t *testing.T) {
			claims := jwt.MapClaims{
				"sub": "build-upload", "exp": time.Now().Add(10 * time.Minute).Unix(),
				"appId": "app-1", "identifierId": testIdentifierID, "buildId": testBuildID,
			}
			if tc.change != nil {
				tc.change(claims)
			}
			if tc.method == nil {
				tc.method = jwt.SigningMethodHS256
			}
			if tc.secret == "" {
				tc.secret = "build-upload-test-secret"
			}
			token, err := jwt.NewWithClaims(tc.method, claims).SignedString([]byte(tc.secret))
			require.NoError(t, err)
			require.Error(t, ValidateBuildUploadToken(token, "app-1", testIdentifierID, testBuildID))
		})
	}
}

func TestBuildObjectKeysAreIsolatedAndValidated(t *testing.T) {
	ref := testArtifact()
	key, err := ref.Key(false)
	require.NoError(t, err)
	require.Equal(t, "builds/android/"+testIdentifierID+"/"+testBuildID+".apk", key)
	staging, err := ref.Key(true)
	require.NoError(t, err)
	require.Equal(t, "builds/android/"+testIdentifierID+"/.uploads/"+testBuildID+".apk", staging)
	ios := ref
	ios.Type = types.BuildArtifactIPA
	key, err = ios.Key(false)
	require.NoError(t, err)
	require.Equal(t, "builds/ios/"+testIdentifierID+"/"+testBuildID+".ipa", key)

	store := &BuildArtifactStore{objectStore: unreachableObjectStore{}}
	ctx := context.Background()
	for name, bad := range map[string]BuildArtifact{
		"traversal identifier": {IdentifierID: "../escape", BuildID: testBuildID, Type: types.BuildArtifactAPK},
		"uppercase uuid":       {IdentifierID: strings.ToUpper(testIdentifierID), BuildID: testBuildID, Type: types.BuildArtifactAPK},
		"unknown type":         {IdentifierID: testIdentifierID, BuildID: testBuildID, Type: "apk/../x"},
		"empty build":          {IdentifierID: testIdentifierID, Type: types.BuildArtifactAAB},
	} {
		_, err := store.Get(ctx, bad, false)
		require.Error(t, err, name)
		require.Error(t, store.Put(ctx, bad, false, strings.NewReader("x")), name)
		require.Error(t, store.Delete(ctx, bad, false), name)
		_, err = store.PresignPut(ctx, "app-1", bad)
		require.Error(t, err, name)
	}
	for _, appID := range []string{"", "../escape", "app/other"} {
		upload, err := store.PresignPut(ctx, appID, ref)
		require.Error(t, err, appID)
		require.Nil(t, upload)
	}
}

func TestLocalBuildStagingCannotOverwritePublishedArtifact(t *testing.T) {
	store, _ := buildArtifactEnv(t, "tenant/")
	ref := testArtifact()
	ctx := context.Background()
	require.NoError(t, store.Put(ctx, ref, false, strings.NewReader("verified")))
	require.NoError(t, store.Put(ctx, ref, true, strings.NewReader("late upload")))
	file, err := store.Get(ctx, ref, false)
	require.NoError(t, err)
	defer file.Reader.Close()
	contents, err := io.ReadAll(file.Reader)
	require.NoError(t, err)
	require.Equal(t, "verified", string(contents))
}

func TestLocalBuildStorageHonoursKeyPrefixAndLeavesNoTempFiles(t *testing.T) {
	store, dir := buildArtifactEnv(t, "tenant/")
	require.NoError(t, store.Put(context.Background(), testArtifact(), true, strings.NewReader("bytes")))
	entries, err := os.ReadDir(filepath.Join(dir, "tenant", "builds", "android", testIdentifierID, ".uploads"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, testBuildID+".apk", entries[0].Name())
}

func TestLocalBuildGetReturnsNilWhenAbsent(t *testing.T) {
	store, _ := buildArtifactEnv(t, "")
	file, err := store.Get(context.Background(), testArtifact(), false)
	require.NoError(t, err)
	require.Nil(t, file)
}

func TestLocalBuildDeleteIsIdempotentAndPrunesEmptyDirectories(t *testing.T) {
	store, dir := buildArtifactEnv(t, "")
	ref := testArtifact()
	ctx := context.Background()
	require.NoError(t, store.Put(ctx, ref, true, strings.NewReader("staged")))
	require.NoError(t, store.Put(ctx, ref, false, strings.NewReader("final")))

	require.NoError(t, store.Delete(ctx, ref, true))
	require.NoError(t, store.Delete(ctx, ref, true))
	_, err := os.Stat(filepath.Join(dir, "builds", "android", testIdentifierID, ".uploads"))
	require.True(t, os.IsNotExist(err), "empty .uploads directory should be pruned")
	file, err := store.Get(ctx, ref, false)
	require.NoError(t, err)
	require.NotNil(t, file, "final artifact survives a staging delete")
	file.Reader.Close()

	require.NoError(t, store.Delete(ctx, ref, false))
	_, err = os.Stat(filepath.Join(dir, "builds", "android", testIdentifierID))
	require.True(t, os.IsNotExist(err), "empty identifier directory should be pruned")
	require.DirExists(t, dir, "the store root is never pruned")
}

func TestLocalBuildDeleteKeepsSiblings(t *testing.T) {
	store, _ := buildArtifactEnv(t, "")
	ref := testArtifact()
	other := ref
	other.BuildID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	ctx := context.Background()
	require.NoError(t, store.Put(ctx, ref, false, strings.NewReader("one")))
	require.NoError(t, store.Put(ctx, other, false, strings.NewReader("two")))
	require.NoError(t, store.Delete(ctx, ref, false))
	file, err := store.Get(ctx, other, false)
	require.NoError(t, err)
	require.NotNil(t, file)
	file.Reader.Close()
}

func writeLegacyUpdate(t *testing.T, root string, segments ...string) {
	t.Helper()
	dir := filepath.Join(append([]string{root}, segments...)...)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".check"), []byte("ok"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "update-metadata.json"), []byte("{}"), 0o644))
}

// Cloud builds share the updates bucket by default, so the branch listing, the
// v1 re-path and update deletions run over their keys too.
func TestBuildTreeCoexistsWithOTATreesAndMigration(t *testing.T) {
	base := t.TempDir()
	objectStore := objectstore.Open(objectstore.ModeLocal, base)
	updates := &UpdateStore{objectStore: objectStore}
	store := &BuildArtifactStore{objectStore: objectStore, sharesUpdates: true}
	appId := "d8471dfc-c3e9-4e14-afd9-21dc34cc498a"
	writeLegacyUpdate(t, base, appId, "main", "1.0.0", "1674170951")
	writeLegacyUpdate(t, base, "staging", "1.0.0", "1674170952")
	ref := testArtifact()
	ctx := context.Background()
	require.NoError(t, store.Put(ctx, ref, true, strings.NewReader("staged")))
	require.NoError(t, store.Put(ctx, ref, false, strings.NewReader("final")))

	branches, err := updates.Branches(ctx, appId)
	require.NoError(t, err)
	require.Equal(t, []string{"main"}, branches)

	require.NoError(t, moveRootKeysUnder(ctx, objectStore, appId))
	_, err = os.Stat(filepath.Join(base, appId, "staging", "1.0.0", "1674170952", ".check"))
	require.NoError(t, err, "the v1 branch is re-pathed")
	_, err = os.Stat(filepath.Join(base, appId, BuildsPrefix))
	require.True(t, os.IsNotExist(err), "the builds tree is not mistaken for a v1 branch")
	file, err := store.Get(ctx, ref, false)
	require.NoError(t, err)
	require.NotNil(t, file)
	file.Reader.Close()
	require.NoError(t, updates.Delete(ctx, appId, "main", "1.0.0", "1674170951"))
	_, err = os.Stat(filepath.Join(base, appId, "main", "1.0.0", "1674170951"))
	require.True(t, os.IsNotExist(err))
	file, err = store.Get(ctx, ref, false)
	require.NoError(t, err)
	require.NotNil(t, file, "deleting an OTA update preserves the build artifact")
	file.Reader.Close()
}

func TestBuildKeysNeverConfirmAV1Triple(t *testing.T) {
	ref := testArtifact()
	for _, staging := range []bool{false, true} {
		key, err := ref.Key(staging)
		require.NoError(t, err)
		_, isMarker := v1BranchTripleFromMarker(key)
		require.False(t, isMarker)
		require.False(t, inConfirmedTriple(key, map[string]bool{}))
	}
}

func TestBuildArtifactValidate(t *testing.T) {
	require.NoError(t, testArtifact().Validate())

	for name, bad := range map[string]BuildArtifact{
		"empty identifier":     {BuildID: testBuildID, Type: types.BuildArtifactAPK},
		"traversal identifier": {IdentifierID: "../escape", BuildID: testBuildID, Type: types.BuildArtifactAPK},
		"uppercase identifier": {IdentifierID: strings.ToUpper(testIdentifierID), BuildID: testBuildID, Type: types.BuildArtifactAPK},
		"empty build":          {IdentifierID: testIdentifierID, Type: types.BuildArtifactAAB},
		"traversal build":      {IdentifierID: testIdentifierID, BuildID: "../escape", Type: types.BuildArtifactIPA},
		"uppercase build":      {IdentifierID: testIdentifierID, BuildID: strings.ToUpper(testBuildID), Type: types.BuildArtifactIPA},
		"empty type":           {IdentifierID: testIdentifierID, BuildID: testBuildID},
		"unknown type":         {IdentifierID: testIdentifierID, BuildID: testBuildID, Type: "apk/../x"},
	} {
		require.Error(t, bad.Validate(), name)
	}
}
