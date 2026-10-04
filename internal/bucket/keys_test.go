package bucket

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

const testBlobHash = "LPJNul-wow4m6DsqxbninhsWHlwfp0JecwQzYpOLmCQ"

func TestValidateSegment(t *testing.T) {
	for _, value := range []string{"main", "release-1.0", "17", strings.Repeat("a", maxSegmentLen)} {
		assert.NoError(t, validateSegment("branch", value), value)
	}
	for name, value := range map[string]string{
		"empty":            "",
		"dot":              ".",
		"dotdot":           "..",
		"nested traversal": "../etc",
		"slash":            "a/b",
		"backslash":        "a\\b",
		"null byte":        "a\x00b",
		"control char":     "a\nb",
		"oversized":        strings.Repeat("a", maxSegmentLen+1),
	} {
		assert.Error(t, validateSegment("branch", value), name)
	}
}

func TestValidateRelativePath(t *testing.T) {
	assert.NoError(t, validateRelativePath("assetPath", "assets/image.png"))
	for _, value := range []string{"", "/etc/passwd", "../../../etc/passwd", "assets/../../etc", "assets\\..\\..\\etc"} {
		assert.Error(t, validateRelativePath("assetPath", value), value)
	}
}

func TestValidateBlobHash(t *testing.T) {
	assert.NoError(t, ValidateBlobHash(testBlobHash))
	assert.Error(t, ValidateBlobHash(""))
	assert.Error(t, ValidateBlobHash("short"))
	assert.Error(t, ValidateBlobHash(testBlobHash+"x"))
	assert.Error(t, ValidateBlobHash("LPJNul+wow4m6DsqxbninhsWHlwfp0JecwQzYpOLmCQ"))
	assert.Error(t, ValidateBlobHash("LPJNul/wow4m6DsqxbninhsWHlwfp0JecwQzYpOLmCQ"))
	// Same digest as testBlobHash, non-canonical spelling (trailing bits set).
	assert.Error(t, ValidateBlobHash("LPJNul-wow4m6DsqxbninhsWHlwfp0JecwQzYpOLmCR"))
}

func TestValidateUploadFile(t *testing.T) {
	assert.NoError(t, ValidateUploadFile("assets/icon.png", testBlobHash))
	assert.Error(t, ValidateUploadFile("", testBlobHash))
	assert.Error(t, ValidateUploadFile("../etc/passwd", testBlobHash))
	assert.Error(t, ValidateUploadFile("metadata.json", "short"))
}

func TestObjectKeys(t *testing.T) {
	assert.Equal(t, "app-1/cas/"+testBlobHash, BlobObjectKey("app-1", testBlobHash))
	assert.Equal(t, "app-1/bsdiff/main/", BSDiffBranchPrefix("app-1", "main"))
	assert.Equal(t, "app-1/bsdiff/main/6f2b1c4e-1b3a-4b4e-9c1d-0a1b2c3d4e5f/0b9a8c7d-6e5f-4a3b-8c2d-1e0f9a8b7c6d", BSDiffObjectKey("app-1", "main", "6f2b1c4e-1b3a-4b4e-9c1d-0a1b2c3d4e5f", "0b9a8c7d-6e5f-4a3b-8c2d-1e0f9a8b7c6d"))
	assert.True(t, ReservedBranchName("cas"))
	assert.True(t, ReservedBranchName("bsdiff"))
}

func TestResolveKeyPrefix(t *testing.T) {
	t.Setenv("S3_KEY_PREFIX", "")
	for env, want := range map[string]string{"": "", "myapp": "myapp/", "myapp/": "myapp/", "a/b": "a/b/"} {
		t.Setenv("BUCKET_KEY_PREFIX", env)
		assert.Equal(t, want, ResolveKeyPrefix(), env)
	}

	// TODO: remove once S3_KEY_PREFIX backward-compat is dropped.
	t.Setenv("BUCKET_KEY_PREFIX", "")
	t.Setenv("S3_KEY_PREFIX", "legacy")
	assert.Equal(t, "legacy/", ResolveKeyPrefix())
	t.Setenv("BUCKET_KEY_PREFIX", "new")
	assert.Equal(t, "new/", ResolveKeyPrefix())

	for _, unsafe := range []string{"../etc", "/etc", "myapp/../other", "a\\b"} {
		t.Setenv("BUCKET_KEY_PREFIX", unsafe)
		assert.Panics(t, func() { ResolveKeyPrefix() }, unsafe)
	}
}
