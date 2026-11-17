// Package webui serves the embedded sign-in and account-security interface (built
// from web/ with Vite into static/) under /auth/. Pages get a strict
// Content-Security-Policy and are never cached; content-hashed assets are cached
// immutably. Only the known pages and files are served: anything else is 404.
package webui

import (
	"embed"
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"

	"github.com/atikansari-ghr/BookmarksSync/internal/server"
)

//go:embed static
var embedded embed.FS

// Files returns the embedded build (index.html, favicon.svg, assets/...).
func Files() fs.FS {
	sub, err := fs.Sub(embedded, "static")
	if err != nil {
		panic(err) // the embed directive guarantees the directory exists
	}
	return sub
}

// pages are the client-side routes served with the app shell.
var pages = map[string]bool{
	"/auth":               true,
	"/auth/login":         true,
	"/auth/security":      true,
	"/auth/oidc/complete": true,
}

// contentTypes pins the types of files the build emits, independent of the host's
// MIME registry (Windows may override Go's built-in table).
var contentTypes = map[string]string{
	".js":    "text/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".woff2": "font/woff2",
	".json":  "application/json",
}

// ContentSecurityPolicy allows only same-origin scripts, styles and API calls.
const ContentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"font-src 'self'; connect-src 'self'; manifest-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// UI serves one build.
type UI struct {
	files fs.FS
	index []byte
}

// New returns a UI for files, which must contain index.html.
func New(files fs.FS) (*UI, error) {
	index, err := fs.ReadFile(files, "index.html")
	if err != nil {
		return nil, errors.New("webui: the build has no index.html; run npm run build in web/")
	}
	return &UI{files: files, index: index}, nil
}

// Register adds the /auth/ pages and assets and redirects / to the sign-in page.
func (u *UI) Register(r server.Registrar) {
	r.Handle("GET /auth/", http.HandlerFunc(u.serve))
	r.Handle("GET /auth", http.HandlerFunc(u.serve))
	r.Handle("GET /{$}", http.RedirectHandler("/auth/login", http.StatusFound))
}

func pageHeaders(h http.Header) {
	h.Set("Content-Security-Policy", ContentSecurityPolicy)
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cache-Control", "no-store")
}

func (u *UI) serve(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimSuffix(r.URL.Path, "/")
	if p == "" {
		p = "/auth"
	}
	if pages[p] {
		pageHeaders(w.Header())
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(u.index)
		}
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/auth/")
	if name == "index.html" || name == "" || strings.HasSuffix(name, "/") || name != path.Clean(name) || !fs.ValidPath(name) {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(u.files, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ctype, ok := contentTypes[path.Ext(name)]
	if !ok {
		if ctype = mime.TypeByExtension(path.Ext(name)); ctype == "" {
			ctype = "application/octet-stream"
		}
	}
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Content-Security-Policy", ContentSecurityPolicy)
	if strings.HasPrefix(name, "assets/") {
		h.Set("Cache-Control", "public, max-age=31536000, immutable") // content-hashed names
	} else {
		h.Set("Cache-Control", "no-store")
	}
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}
