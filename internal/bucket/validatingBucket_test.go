package bucket

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"xprem/internal/types"

	"github.com/stretchr/testify/assert"
)

// stubBucket records the last call so tests can verify whether the validating
// decorator delegated to the inner bucket or short-circuited on validation.
type stubBucket struct {
	called bool
}

func (s *stubBucket) mark() { s.called = true }

func (s *stubBucket) GetBranches(appId string) ([]string, error) { s.mark(); return nil, nil }
func (s *stubBucket) GetRuntimeVersions(appId, branch string) ([]types.RuntimeVersionWithStats, error) {
	s.mark()
	return nil, nil
}
func (s *stubBucket) GetUpdates(appId, branch, runtimeVersion string) ([]types.Update, error) {
	s.mark()
	return nil, nil
}
func (s *stubBucket) GetFile(update types.Update, assetPath string) (*types.BucketFile, error) {
	s.mark()
	return nil, nil
}
func (s *stubBucket) RequestUploadUrlForFileUpdate(appId, branch, runtimeVersion, updateId, fileName string) (string, error) {
	s.mark()
	return "", nil
}
func (s *stubBucket) UploadFileIntoUpdate(update types.Update, fileName string, file io.Reader) error {
	s.mark()
	return nil
}
func (s *stubBucket) CopyFileIntoUpdate(source types.Update, target types.Update, fileName string) error {
	s.mark()
	return nil
}
func (s *stubBucket) DeleteUpdateFolder(appId, branch, runtimeVersion, updateId string) error {
	s.mark()
	return nil
}
func (s *stubBucket) CreateUpdateFrom(previousUpdate *types.Update, newUpdateId string) (*types.Update, error) {
	s.mark()
	return nil, nil
}
func (s *stubBucket) GetInstanceID() (string, error)              { s.mark(); return "", nil }
func (s *stubBucket) PersistInstanceID(_ string) error            { s.mark(); return nil }
func (s *stubBucket) RetrieveMigrationHistory() ([]string, error) { s.mark(); return nil, nil }
func (s *stubBucket) ApplyMigration(migrationId string) error     { s.mark(); return nil }
func (s *stubBucket) RemoveMigrationFromHistory(id string) error  { s.mark(); return nil }
func (s *stubBucket) BlobExists(context.Context, string, string) (bool, error) {
	s.mark()
	return false, nil
}
func (s *stubBucket) GetBlob(context.Context, string, string) (*types.BucketFile, error) {
	s.mark()
	return nil, nil
}
func (s *stubBucket) PutBlob(context.Context, string, string, io.Reader) error {
	s.mark()
	return nil
}
func (s *stubBucket) BSDiffExists(context.Context, string, string, string, string) (bool, error) {
	s.mark()
	return false, nil
}
func (s *stubBucket) GetBSDiff(context.Context, string, string, string, string) (*types.BucketFile, error) {
	s.mark()
	return nil, nil
}
func (s *stubBucket) PutBSDiff(context.Context, string, string, string, string, io.Reader) error {
	s.mark()
	return nil
}
func (s *stubBucket) DeleteBSDiffs(context.Context, string, string) error {
	s.mark()
	return nil
}
func (s *stubBucket) RequestBlobUploadURL(_, _, _ string) (string, error) {
	s.mark()
	return "", nil
}

func validUpdate() types.Update {
	return types.Update{AppId: "app-1", Branch: "main", RuntimeVersion: "1.0", UpdateId: "123"}
}

func TestValidateSegment_RejectsTraversal(t *testing.T) {
	cases := []struct{ name, value string }{
		{"branch with dot-dot", ".."},
		{"branch with slash", "foo/bar"},
		{"branch with backslash", "foo\\bar"},
		{"empty", ""},
		{"single dot", "."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Error(t, validateSegment("branch", c.value))
		})
	}
}

func TestValidateSegment_AcceptsValidNames(t *testing.T) {
	cases := []string{"main", "feature-x", "v1.2.3", "release_2025", "..hidden"} // ".." as prefix of a name is allowed (not a segment of its own)
	for _, v := range cases {
		t.Run(v, func(t *testing.T) {
			assert.NoError(t, validateSegment("branch", v))
		})
	}
}

func TestValidateSegment_RejectsNullAndControlChars(t *testing.T) {
	cases := map[string]string{
		"null byte":       "foo\x00bar",
		"soh":             "foo\x01bar",
		"bell":            "foo\x07bar",
		"backspace":       "foo\x08bar",
		"tab":             "foo\tbar",
		"newline":         "foo\nbar",
		"carriage return": "foo\rbar",
		"escape":          "foo\x1bbar",
		"del":             "foo\x7fbar",
	}
	for name, v := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Error(t, validateSegment("branch", v))
		})
	}
}

func TestValidateSegment_RejectsOversizedValues(t *testing.T) {
	over := strings.Repeat("a", maxSegmentLen+1)
	err := validateSegment("branch", over)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds max length")
}

func TestValidateSegment_AcceptsValueAtMaxLength(t *testing.T) {
	exact := strings.Repeat("a", maxSegmentLen)
	assert.NoError(t, validateSegment("branch", exact))
}

func TestValidateRelativePath_RejectsTraversal(t *testing.T) {
	cases := []struct{ name, value string }{
		{"dot-dot segment", "assets/../../../etc/passwd"},
		{"leading dot-dot", "../secret"},
		{"absolute unix", "/etc/passwd"},
		{"absolute windows", "\\etc\\passwd"},
		// Backslash anywhere in the path, not just as a prefix. On Windows
		// filepath.Join treats "\" as a separator, so "assets\..\..\etc" would
		// traverse just like "assets/../..". The segment-level check only
		// catches the leading-\ case, so enforce the full ban here.
		{"mid-path backslash", "assets\\..\\..\\etc\\passwd"},
		{"trailing backslash", "assets/foo\\"},
		{"empty", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Error(t, validateRelativePath("assetPath", c.value))
		})
	}
}

func TestValidateRelativePath_AcceptsNestedPaths(t *testing.T) {
	cases := []string{"image.png", "assets/img/logo.png", "deep/nested/path/file.js"}
	for _, v := range cases {
		t.Run(v, func(t *testing.T) {
			assert.NoError(t, validateRelativePath("assetPath", v))
		})
	}
}

// setPrefixEnv swaps the prefix env vars for one test and restores them on
// cleanup. Required because config.GetEnv reads process env directly.
func setPrefixEnv(t *testing.T, bucketKey, s3Key string) {
	t.Helper()
	prev := map[string]string{
		"BUCKET_KEY_PREFIX": os.Getenv("BUCKET_KEY_PREFIX"),
		"S3_KEY_PREFIX":     os.Getenv("S3_KEY_PREFIX"),
	}
	os.Setenv("BUCKET_KEY_PREFIX", bucketKey)
	os.Setenv("S3_KEY_PREFIX", s3Key)
	t.Cleanup(func() {
		os.Setenv("BUCKET_KEY_PREFIX", prev["BUCKET_KEY_PREFIX"])
		os.Setenv("S3_KEY_PREFIX", prev["S3_KEY_PREFIX"])
	})
}

func TestResolveKeyPrefix_HappyPaths(t *testing.T) {
	t.Run("empty returns empty", func(t *testing.T) {
		setPrefixEnv(t, "", "")
		assert.Equal(t, "", resolveKeyPrefix())
	})
	t.Run("appends trailing slash", func(t *testing.T) {
		setPrefixEnv(t, "eoota", "")
		assert.Equal(t, "eoota/", resolveKeyPrefix())
	})
	t.Run("preserves existing trailing slash", func(t *testing.T) {
		setPrefixEnv(t, "eoota/", "")
		assert.Equal(t, "eoota/", resolveKeyPrefix())
	})
	t.Run("s3 legacy fallback when bucket prefix unset", func(t *testing.T) {
		setPrefixEnv(t, "", "legacy")
		assert.Equal(t, "legacy/", resolveKeyPrefix())
	})
}

func TestResolveKeyPrefix_PanicsOnUnsafeValues(t *testing.T) {
	cases := map[string]string{
		"absolute unix": "/eoota",
		"dot-dot":       "foo/../bar",
		"backslash":     "eoota\\bad",
		"windows drive": "C:\\eoota",
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			setPrefixEnv(t, bad, "")
			assert.Panics(t, func() { _ = resolveKeyPrefix() })
		})
	}
}

func TestValidatingBucket_GetFile_RejectsTraversalInBranch(t *testing.T) {
	stub := &stubBucket{}
	v := &validatingBucket{Inner: stub}
	update := types.Update{AppId: "app-1", Branch: "../evil", RuntimeVersion: "1.0", UpdateId: "123"}
	_, err := v.GetFile(update, "asset.png")
	assert.Error(t, err)
	assert.False(t, stub.called, "inner bucket should not be called when validation fails")
}

func TestValidatingBucket_GetFile_RejectsTraversalInAssetPath(t *testing.T) {
	stub := &stubBucket{}
	v := &validatingBucket{Inner: stub}
	_, err := v.GetFile(validUpdate(), "../../../etc/passwd")
	assert.Error(t, err)
	assert.False(t, stub.called)
}

func TestValidatingBucket_UploadFileIntoUpdate_RejectsTraversalInFileName(t *testing.T) {
	stub := &stubBucket{}
	v := &validatingBucket{Inner: stub}
	err := v.UploadFileIntoUpdate(validUpdate(), "../evil.js", bytes.NewReader(nil))
	assert.Error(t, err)
	assert.False(t, stub.called)
}

func TestValidatingBucket_UploadFileIntoUpdate_RejectsReservedBranch(t *testing.T) {
	stub := &stubBucket{}
	v := &validatingBucket{Inner: stub}
	update := validUpdate()
	update.Branch = casDir
	err := v.UploadFileIntoUpdate(update, "metadata.json", bytes.NewReader(nil))
	assert.Error(t, err)
	assert.False(t, stub.called, "an update folder under cas/ must never reach the backend")
}

func TestValidatingBucket_CopyFileIntoUpdate_RejectsTraversalInFileName(t *testing.T) {
	stub := &stubBucket{}
	v := &validatingBucket{Inner: stub}
	err := v.CopyFileIntoUpdate(validUpdate(), validUpdate(), "../evil.js")
	assert.Error(t, err)
	assert.False(t, stub.called)
}

func TestValidatingBucket_CopyFileIntoUpdate_RejectsInvalidUpdates(t *testing.T) {
	badUpdate := validUpdate()
	badUpdate.UpdateId = "123/../456"
	for _, c := range []struct {
		name           string
		source, target types.Update
	}{
		{"bad source", badUpdate, validUpdate()},
		{"bad target", validUpdate(), badUpdate},
	} {
		t.Run(c.name, func(t *testing.T) {
			stub := &stubBucket{}
			v := &validatingBucket{Inner: stub}
			err := v.CopyFileIntoUpdate(c.source, c.target, "assets/img.png")
			assert.Error(t, err)
			assert.False(t, stub.called)
		})
	}
}

func TestValidatingBucket_CopyFileIntoUpdate_DelegatesOnValidInput(t *testing.T) {
	stub := &stubBucket{}
	v := &validatingBucket{Inner: stub}
	err := v.CopyFileIntoUpdate(validUpdate(), validUpdate(), "assets/img.png")
	assert.NoError(t, err)
	assert.True(t, stub.called)
}

func TestValidatingBucket_DeleteUpdateFolder_RejectsSlashInUpdateId(t *testing.T) {
	stub := &stubBucket{}
	v := &validatingBucket{Inner: stub}
	err := v.DeleteUpdateFolder("app-1", "main", "1.0", "123/../456")
	assert.Error(t, err)
	assert.False(t, stub.called)
}

func TestValidatingBucket_RequestUploadUrl_RejectsTraversalInFileName(t *testing.T) {
	stub := &stubBucket{}
	v := &validatingBucket{Inner: stub}
	_, err := v.RequestUploadUrlForFileUpdate("app-1", "main", "1.0", "123", "../etc/passwd")
	assert.Error(t, err)
	assert.False(t, stub.called)
}

func TestValidatingBucket_CreateUpdateFrom_RejectsTraversalInPreviousUpdate(t *testing.T) {
	stub := &stubBucket{}
	v := &validatingBucket{Inner: stub}
	prev := &types.Update{AppId: "app-1", Branch: "../evil", RuntimeVersion: "1.0", UpdateId: "123"}
	_, err := v.CreateUpdateFrom(prev, "456")
	assert.Error(t, err)
	assert.False(t, stub.called)
}

func TestValidatingBucket_ApplyMigration_RejectsSlash(t *testing.T) {
	stub := &stubBucket{}
	v := &validatingBucket{Inner: stub}
	err := v.ApplyMigration("../other")
	assert.Error(t, err)
	assert.False(t, stub.called)
}

func TestValidatingBucket_ValidInputsDelegate(t *testing.T) {
	stub := &stubBucket{}
	v := &validatingBucket{Inner: stub}
	_, err := v.GetFile(validUpdate(), "assets/image.png")
	assert.NoError(t, err)
	assert.True(t, stub.called)
}

// The backends list the children of {appId}/, and the cas folder is one of
// them: without the filter the dashboard renders "cas" as a branch.
func TestValidatingBucketGetBranchesHidesCas(t *testing.T) {
	base := t.TempDir()
	writeFile(t, filepath.Join(base, "app-1", "branch-a", "1.0", "100", ".check"))
	writeFile(t, filepath.Join(base, "app-1", "cas", "some-blob-hash"))
	writeFile(t, filepath.Join(base, "app-1", "bsDiff", "branch-a", "200", "100"))

	b := &validatingBucket{Inner: &LocalBucket{BasePath: base}}

	branches, err := b.GetBranches("app-1")
	assert.Nil(t, err)
	assert.Equal(t, []string{"branch-a"}, branches)
}

func TestValidatingBucketRejectsTheReservedBranchName(t *testing.T) {
	b := &validatingBucket{Inner: &stubBucket{}}

	_, err := b.GetRuntimeVersions("app-1", casDir)
	assert.ErrorContains(t, err, "reserved")

	_, err = b.RequestUploadUrlForFileUpdate("app-1", casDir, "1.0", "100", "metadata.json")
	assert.ErrorContains(t, err, "reserved")
}
