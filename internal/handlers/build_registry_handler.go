package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"
	"xprem/config"
	"xprem/internal/bucket"
	"xprem/internal/ios"
	"xprem/internal/objectstore"
	"xprem/internal/repository"
	"xprem/internal/services"
	"xprem/internal/types"
	"xprem/internal/validation"

	"github.com/gorilla/mux"
)

const buildRequestBodyLimit = 16 << 10

type BuildRegistryHandler struct{ service *services.BuildService }

func NewBuildRegistryHandler(service *services.BuildService) *BuildRegistryHandler {
	return &BuildRegistryHandler{service: service}
}

func renderBuildRegistryError(w http.ResponseWriter, err error) {
	var missing *repository.ErrResourceNotFound
	switch {
	case errors.Is(err, services.ErrUnauthorized):
		RenderError(w, http.StatusUnauthorized, "Invalid or expired upload authorization.")
	case errors.Is(err, services.ErrBuildConflict), errors.Is(err, services.ErrBuildNotReady), errors.Is(err, services.ErrBuildState), errors.Is(err, repository.ErrBuildLogOffset):
		RenderError(w, http.StatusConflict, err.Error())
	case errors.Is(err, services.ErrBuildIntegrity):
		RenderError(w, http.StatusBadRequest, err.Error())
	case errors.As(err, &missing):
		RenderError(w, http.StatusNotFound, "Build not found.")
	case validation.IsValidationError(err), errors.Is(err, repository.ErrNotSupportedInStatelessMode), errors.Is(err, services.ErrBuildStorageUnavailable):
		RenderError(w, http.StatusBadRequest, err.Error())
	default:
		log.Printf("[BUILD] Could not process the build request: %v", err)
		RenderError(w, http.StatusInternalServerError, "Could not process the build request.")
	}
}

func decodeBuildBody(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, buildRequestBodyLimit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil || decoder.More() {
		RenderError(w, http.StatusBadRequest, "Invalid build request body.")
		return false
	}
	return true
}

func (h *BuildRegistryHandler) Start(w http.ResponseWriter, r *http.Request) {
	var input services.BuildStartInput
	if !decodeBuildBody(w, r, &input) {
		return
	}
	build, err := h.service.Start(r.Context(), mux.Vars(r)["APP_ID"], services.BuildIdentifierFromContext(r.Context()), mux.Vars(r)["BUILD_ID"], input)
	if err != nil {
		renderBuildRegistryError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	RenderJSON(w, http.StatusOK, build)
}

func (h *BuildRegistryHandler) RegisterArtifact(w http.ResponseWriter, r *http.Request) {
	var input services.RegisterBuildInput
	if !decodeBuildBody(w, r, &input) {
		return
	}
	result, err := h.service.RegisterArtifact(r.Context(), mux.Vars(r)["APP_ID"], services.BuildIdentifierFromContext(r.Context()), mux.Vars(r)["BUILD_ID"], input)
	if err != nil {
		renderBuildRegistryError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	RenderJSON(w, http.StatusOK, result)
}

func (h *BuildRegistryHandler) Fail(w http.ResponseWriter, r *http.Request) {
	var input services.FailBuildInput
	if !decodeBuildBody(w, r, &input) {
		return
	}
	build, err := h.service.Fail(r.Context(), mux.Vars(r)["APP_ID"], services.BuildIdentifierFromContext(r.Context()), mux.Vars(r)["BUILD_ID"], input)
	if err != nil {
		renderBuildRegistryError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	RenderJSON(w, http.StatusOK, build)
}

func (h *BuildRegistryHandler) Complete(w http.ResponseWriter, r *http.Request) {
	build, err := h.service.Complete(r.Context(), mux.Vars(r)["APP_ID"], services.BuildIdentifierFromContext(r.Context()), mux.Vars(r)["BUILD_ID"])
	if err != nil {
		renderBuildRegistryError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	RenderJSON(w, http.StatusOK, build)
}

func (h *BuildRegistryHandler) UploadLocal(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	r.Body = http.MaxBytesReader(w, r.Body, services.MaxBuildSize+1)
	if err := h.service.UploadLocal(r.Context(), mux.Vars(r)["APP_ID"], services.BuildIdentifierFromContext(r.Context()), mux.Vars(r)["BUILD_ID"], r.Header.Get(bucket.LocalUploadTokenHeader), r.Body); err != nil {
		renderBuildRegistryError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *BuildRegistryHandler) List(w http.ResponseWriter, r *http.Request) {
	limit, offset := 20, 0
	var err error
	if value := r.URL.Query().Get("limit"); value != "" {
		limit, err = strconv.Atoi(value)
	}
	if err == nil {
		if value := r.URL.Query().Get("offset"); value != "" {
			offset, err = strconv.Atoi(value)
		}
	}
	if err != nil || limit < 1 || limit > 100 || offset < 0 || offset > 100000 {
		RenderError(w, http.StatusBadRequest, "Invalid pagination.")
		return
	}
	page, err := h.service.List(r.Context(), mux.Vars(r)["APP_ID"], int32(limit), int32(offset), r.URL.Query().Get("cursor"))
	if err != nil {
		renderBuildRegistryError(w, err)
		return
	}
	RenderJSON(w, http.StatusOK, page)
}

func (h *BuildRegistryHandler) Get(w http.ResponseWriter, r *http.Request) {
	b, err := h.service.Get(r.Context(), mux.Vars(r)["APP_ID"], mux.Vars(r)["BUILD_ID"])
	if err != nil {
		renderBuildRegistryError(w, err)
		return
	}
	RenderJSON(w, http.StatusOK, b)
}

func (h *BuildRegistryHandler) AppendLogs(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Offset  int32  `json:"offset"`
		Content string `json:"content"`
	}
	// JSON escaping can expand a byte to six characters (for example, a tab).
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, types.MaxBuildLogChunkBytes*6+1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		RenderError(w, http.StatusBadRequest, "Invalid log batch.")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		RenderError(w, http.StatusBadRequest, "Invalid log batch.")
		return
	}
	if err := h.service.AppendLogs(r.Context(), mux.Vars(r)["APP_ID"], services.BuildIdentifierFromContext(r.Context()), mux.Vars(r)["BUILD_ID"], input.Offset, input.Content); err != nil {
		renderBuildRegistryError(w, err)
		return
	}
	RenderJSON(w, http.StatusOK, map[string]int32{"nextOffset": input.Offset + int32(len(input.Content))})
}

func (h *BuildRegistryHandler) ListLogs(w http.ResponseWriter, r *http.Request) {
	after := int64(0)
	var err error
	if value := r.URL.Query().Get("after"); value != "" {
		after, err = strconv.ParseInt(value, 10, 32)
	}
	if err != nil {
		RenderError(w, http.StatusBadRequest, "Invalid log offset.")
		return
	}
	chunks, err := h.service.ListLogs(r.Context(), mux.Vars(r)["APP_ID"], mux.Vars(r)["BUILD_ID"], int32(after))
	if err != nil {
		renderBuildRegistryError(w, err)
		return
	}
	next := int32(after)
	if len(chunks) > 0 {
		last := chunks[len(chunks)-1]
		next = last.Offset + int32(len(last.Content))
	}
	w.Header().Set("Cache-Control", "no-store")
	RenderJSON(w, http.StatusOK, map[string]any{"chunks": chunks, "nextOffset": next})
}

func (h *BuildRegistryHandler) Download(w http.ResponseWriter, r *http.Request) {
	b, err := h.service.Get(r.Context(), mux.Vars(r)["APP_ID"], mux.Vars(r)["BUILD_ID"])
	if err != nil {
		renderBuildRegistryError(w, err)
		return
	}
	h.download(w, r, *b)
}

func (h *BuildRegistryHandler) download(w http.ResponseWriter, r *http.Request, b types.BuildRecord) {
	file, err := h.service.Download(r.Context(), b)
	if err != nil {
		renderBuildRegistryError(w, err)
		return
	}
	if file == nil {
		RenderError(w, http.StatusNotFound, "Artifact not found.")
		return
	}
	defer file.Reader.Close()
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "application/octet-stream")
	if b.ArtifactType == "apk" {
		w.Header().Set("Content-Type", "application/vnd.android.package-archive")
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.%s"`, b.ID, b.ArtifactType))
	w.Header().Set("Content-Length", strconv.FormatInt(b.Size, 10))
	_, _ = io.Copy(w, file.Reader)
}

func (h *BuildRegistryHandler) CreateShare(w http.ResponseWriter, r *http.Request) {
	input := struct {
		ExpiresInHours int `json:"expiresInHours"`
	}{24}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&input); err != nil && !errors.Is(err, io.EOF) {
		RenderError(w, http.StatusBadRequest, "Invalid share expiration.")
		return
	}
	share, token, err := h.service.CreateShare(r.Context(), mux.Vars(r)["APP_ID"], mux.Vars(r)["BUILD_ID"], input.ExpiresInHours)
	if err != nil {
		renderBuildRegistryError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	RenderJSON(w, http.StatusCreated, map[string]any{"share": share, "url": config.BaseURL() + "/build-shares/" + token})
}

func (h *BuildRegistryHandler) ListShares(w http.ResponseWriter, r *http.Request) {
	shares, err := h.service.ListShares(r.Context(), mux.Vars(r)["APP_ID"], mux.Vars(r)["BUILD_ID"])
	if err != nil {
		renderBuildRegistryError(w, err)
		return
	}
	RenderJSON(w, http.StatusOK, map[string]any{"shares": shares})
}

func (h *BuildRegistryHandler) RevokeShare(w http.ResponseWriter, r *http.Request) {
	if err := h.service.RevokeShare(r.Context(), mux.Vars(r)["APP_ID"], mux.Vars(r)["BUILD_ID"], mux.Vars(r)["SHARE_ID"]); err != nil {
		renderBuildRegistryError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// resolveShare loads the build behind a share link, answering for the caller when the link is dead.
func (h *BuildRegistryHandler) resolveShare(w http.ResponseWriter, r *http.Request) (*types.BuildRecord, time.Time, bool) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	b, expiresAt, err := h.service.ResolveShare(r.Context(), mux.Vars(r)["TOKEN"])
	var missing *repository.ErrResourceNotFound
	switch {
	case errors.As(err, &missing), errors.Is(err, repository.ErrNotSupportedInStatelessMode):
		http.Error(w, "Expired link", http.StatusBadRequest)
		return nil, time.Time{}, false
	case err != nil:
		log.Printf("[BUILD] Could not resolve a sharing link: %v", err)
		http.Error(w, "Could not resolve this sharing link.", http.StatusInternalServerError)
		return nil, time.Time{}, false
	}
	return b, expiresAt, true
}

// PublicShare downloads a shared APK, or shows the install page of a shared iOS build.
func (h *BuildRegistryHandler) PublicShare(w http.ResponseWriter, r *http.Request) {
	b, expiresAt, ok := h.resolveShare(w, r)
	if !ok {
		return
	}
	if b.ArtifactType != types.BuildArtifactIPA {
		h.serveSharedArtifact(w, r, *b, expiresAt)
		return
	}
	manifestURL := config.BaseURL() + "/build-shares/" + mux.Vars(r)["TOKEN"] + "/manifest.plist"
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = iosInstallPage.Execute(w, map[string]any{
		"ApplicationID": b.ApplicationID,
		"Version":       b.Metadata.Version,
		"BuildNumber":   b.Metadata.BuildNumber,
		"InstallURL":    template.URL("itms-services://?action=download-manifest&url=" + url.QueryEscape(manifestURL)),
	})
}

// PublicShareManifest answers the manifest iOS reads before installing a shared build.
func (h *BuildRegistryHandler) PublicShareManifest(w http.ResponseWriter, r *http.Request) {
	b, _, ok := h.resolveShare(w, r)
	if !ok {
		return
	}
	manifest, err := ios.InstallManifest(ios.InstallManifestInput{
		PackageURL:       config.BaseURL() + "/build-shares/" + mux.Vars(r)["TOKEN"] + "/app.ipa",
		BundleIdentifier: b.ApplicationID,
		Version:          b.Metadata.Version,
		Title:            b.ApplicationID,
	})
	if err != nil {
		http.Error(w, "Could not resolve this sharing link.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	_, _ = w.Write(manifest)
}

// PublicShareArtifact serves the file of a shared build to the iOS installer.
func (h *BuildRegistryHandler) PublicShareArtifact(w http.ResponseWriter, r *http.Request) {
	if b, expiresAt, ok := h.resolveShare(w, r); ok {
		h.serveSharedArtifact(w, r, *b, expiresAt)
	}
}

func (h *BuildRegistryHandler) serveSharedArtifact(w http.ResponseWriter, r *http.Request, b types.BuildRecord, expiresAt time.Time) {
	downloadURL, err := h.service.DownloadURL(r.Context(), b, expiresAt)
	switch {
	case errors.Is(err, objectstore.ErrDownloadExpired):
		http.Error(w, "Expired link", http.StatusBadRequest)
	case errors.Is(err, services.ErrBuildStorageUnavailable):
		http.Error(w, "Builds are turned off on this server.", http.StatusServiceUnavailable)
	case err != nil:
		log.Printf("[BUILD] Could not sign a shared build download: %v", err)
		http.Error(w, "Could not resolve this sharing link.", http.StatusInternalServerError)
	case downloadURL != "":
		w.Header().Set("Location", downloadURL)
		w.WriteHeader(http.StatusFound)
	default:
		h.download(w, r, b)
	}
}

var iosInstallPage = template.Must(template.New("ios-install").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Install {{.ApplicationID}}</title>
<style>
body{font-family:-apple-system,system-ui,sans-serif;margin:0;padding:32px 20px;background:#fafafa;color:#18181b}
main{max-width:420px;margin:0 auto}
h1{font-size:20px;margin:0 0 4px;word-break:break-all}
p{font-size:14px;line-height:1.5;color:#52525b}
a.install{display:block;margin:24px 0;padding:14px;border-radius:10px;background:#18181b;color:#fff;text-align:center;text-decoration:none;font-weight:600}
@media (prefers-color-scheme:dark){body{background:#09090b;color:#fafafa}p{color:#a1a1aa}a.install{background:#fafafa;color:#18181b}}
</style>
</head>
<body>
<main>
<h1>{{.ApplicationID}}</h1>
<p>Version {{.Version}} ({{.BuildNumber}})</p>
<a class="install" href="{{.InstallURL}}">Install</a>
<p>Open this page in Safari on the iPhone. Only iPhones that were registered for this app before this build was made can install it.</p>
</main>
</body>
</html>
`))
