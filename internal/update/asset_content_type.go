package update

import (
	"mime"
	"strings"
)

// Fonts are common Expo assets but absent from Go's builtin MIME table; without
// this the result depends on the host's MIME database.
func init() {
	mime.AddExtensionType(".ttf", "font/ttf")
	mime.AddExtensionType(".otf", "font/otf")
}

// AssetContentType is the media type an update's asset is announced with in the
// manifest AND served with by the asset route. Shared on purpose: the two used to
// compute it apart and had drifted into exactly each other's answers.
func AssetContentType(ext string, isLaunchAsset bool) string {
	if isLaunchAsset {
		return "application/javascript"
	}
	// TypeByExtension wants the leading dot, and answers "" for anything it does
	// not know — which would otherwise put an empty Content-Type on the wire.
	if resolved := mime.TypeByExtension("." + strings.TrimPrefix(ext, ".")); resolved != "" {
		return resolved
	}
	return "application/octet-stream"
}
