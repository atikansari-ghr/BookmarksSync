package api

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/atikansari-ghr/BookmarksSync/internal/server"
)

const brandingAssetLimit int64 = 2 << 20

var brandingAssetTypes = map[string]struct{ ext, contentType string }{
	"image/png":  {".png", "image/png"},
	"image/jpeg": {".jpg", "image/jpeg"},
	"image/webp": {".webp", "image/webp"},
}

func assetID(v string) bool {
	if len(v) != 32 {
		return false
	}
	for _, c := range v {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func brandingAssetReferenced(id string, settings map[string]string) bool {
	return assetID(id) && (settings["branding.logo-id"] == id || settings["branding.background-id"] == id)
}

func (a *API) brandingAssetExists(id string) bool {
	if !assetID(id) || a.assetDir == "" {
		return false
	}
	for _, kind := range brandingAssetTypes {
		info, err := os.Lstat(filepath.Join(a.assetDir, id+kind.ext))
		if err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}

// Remove only generated assets that are no longer referenced by either login
// setting. Callers serialize settings changes with assetMu to prevent races with
// another upload or settings update. Callers must hold assetMu through cleanup.
func (a *API) cleanupUnreferencedBrandingAssets(candidates []string, settings map[string]string) {
	for _, id := range candidates {
		if !assetID(id) || brandingAssetReferenced(id, settings) {
			continue
		}
		for _, kind := range brandingAssetTypes {
			_ = os.Remove(filepath.Join(a.assetDir, id+kind.ext))
		}
	}
}

func (a *API) serveBrandingAsset(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, "/api/v1/auth/presentation/assets/") {
		return false
	}
	id := r.PathValue("id")
	if !assetID(id) || a.assetDir == "" {
		http.NotFound(w, r)
		return true
	}
	settings, err := a.ids.SystemSettings(r.Context())
	if err != nil {
		writeDomainError(w, err)
		return true
	}
	if !brandingAssetReferenced(id, settings) {
		http.NotFound(w, r)
		return true
	}
	for _, kind := range brandingAssetTypes {
		path := filepath.Join(a.assetDir, id+kind.ext)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		defer f.Close()
		w.Header().Set("Content-Type", kind.contentType)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "public, max-age=3600, immutable")
		_, _ = io.Copy(w, f)
		return true
	}
	http.NotFound(w, r)
	return true
}

func (a *API) uploadBrandingAsset(w http.ResponseWriter, r *http.Request) {
	if a.assetDir == "" {
		server.WriteError(w, http.StatusServiceUnavailable, "asset_storage_unavailable", "branding asset storage is unavailable")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, brandingAssetLimit+(64<<10))
	if err := r.ParseMultipartForm(64 << 10); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "request body too large") {
			server.WriteError(w, http.StatusRequestEntityTooLarge, "file_too_large", "branding image exceeds 2 MiB")
		} else {
			server.WriteError(w, http.StatusBadRequest, "invalid_upload", "a multipart image upload is required")
		}
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	kind := r.FormValue("kind")
	settingKey := map[string]string{"logo": "branding.logo-id", "background": "branding.background-id"}[kind]
	if settingKey == "" {
		server.WriteError(w, http.StatusBadRequest, "invalid_upload", "kind must be logo or background")
		return
	}
	file, _, err := r.FormFile("asset")
	if err != nil {
		server.WriteError(w, http.StatusBadRequest, "invalid_upload", "an image file is required")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, brandingAssetLimit+1))
	if err != nil {
		server.WriteError(w, http.StatusBadRequest, "invalid_upload", "image could not be read")
		return
	}
	if int64(len(data)) > brandingAssetLimit {
		server.WriteError(w, http.StatusRequestEntityTooLarge, "file_too_large", "branding image exceeds 2 MiB")
		return
	}
	contentType := http.DetectContentType(data)
	typ, ok := brandingAssetTypes[contentType]
	if !ok {
		server.WriteError(w, http.StatusBadRequest, "unsupported_image", "only PNG, JPEG, and WebP images are supported")
		return
	}
	if err := os.MkdirAll(a.assetDir, 0700); err != nil {
		server.WriteError(w, http.StatusInternalServerError, "asset_storage_error", "branding image could not be stored")
		return
	}
	dirInfo, err := os.Lstat(a.assetDir)
	if err != nil || !dirInfo.IsDir() || dirInfo.Mode()&os.ModeSymlink != 0 {
		server.WriteError(w, http.StatusInternalServerError, "asset_storage_error", "branding image storage is not a private directory")
		return
	}
	if err := os.Chmod(a.assetDir, 0700); err != nil {
		server.WriteError(w, http.StatusInternalServerError, "asset_storage_error", "branding image storage permissions could not be secured")
		return
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		server.WriteError(w, http.StatusInternalServerError, "asset_storage_error", "branding image could not be stored")
		return
	}
	id := hex.EncodeToString(random)
	path := filepath.Join(a.assetDir, id+typ.ext)
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		server.WriteError(w, http.StatusInternalServerError, "asset_storage_error", "branding image could not be stored")
		return
	}
	_, writeErr := out.Write(data)
	closeErr := out.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(path)
		server.WriteError(w, http.StatusInternalServerError, "asset_storage_error", "branding image could not be stored")
		return
	}
	a.assetMu.Lock()
	old, err := a.ids.SystemSettings(r.Context())
	if err != nil {
		a.assetMu.Unlock()
		_ = os.Remove(path)
		writeDomainError(w, err)
		return
	}
	settings, err := a.ids.UpdateSystemSettings(r.Context(), map[string]*string{settingKey: &id})
	if err != nil {
		a.assetMu.Unlock()
		_ = os.Remove(path)
		writeDomainError(w, err)
		return
	}
	a.cleanupUnreferencedBrandingAssets([]string{old[settingKey]}, settings)
	a.assetMu.Unlock()
	a.recordAdminActivity(r, "branding_asset_uploaded", "system", 0)
	writeJSON(w, http.StatusCreated, map[string]any{"url": "/api/v1/auth/presentation/assets/" + id, "settings": settings})
}
