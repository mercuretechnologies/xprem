package update

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The manifest and the asset route once computed this apart and had drifted into
// exactly each other's answers: the bundle was announced with no type at all, and
// every image and font was announced as JavaScript.
func TestAssetContentType(t *testing.T) {
	cases := []struct {
		name          string
		ext           string
		isLaunchAsset bool
		want          string
	}{
		{"the launch asset is the JS bundle", "", true, "application/javascript"},
		{"the launch asset ignores any extension it carries", "bundle", true, "application/javascript"},
		{"png", "png", false, "image/png"},
		{"ttf", "ttf", false, "font/ttf"},
		// The two call sites spell the extension differently; neither may lose.
		{"a leading dot is tolerated", ".png", false, "image/png"},
		// An empty type would go on the wire as an empty Content-Type header.
		{"an unknown extension still names something", "wat", false, "application/octet-stream"},
		{"no extension still names something", "", false, "application/octet-stream"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, AssetContentType(tc.ext, tc.isLaunchAsset))
		})
	}
}
