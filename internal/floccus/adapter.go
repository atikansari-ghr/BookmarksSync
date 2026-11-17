// Package floccus is the wire adapter that lets the unmodified floccus extension
// (v5.13.0 "nextcloud-bookmarks" adapter) sync against the bookmark domain.
//
// It implements only the routes, verbs and envelopes the extension actually uses
// (see docs/floccus-compatibility.md), authenticates every request with HTTP Basic
// username:app-secret, and never emits a "ticket" field or a redirect.
package floccus

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
	"github.com/atikansari-ghr/BookmarksSync/internal/server"
)

const (
	restIndexed = "/index.php/apps/bookmarks/public/rest/v2"
	restPlain   = "/apps/bookmarks/public/rest/v2"
	capPath     = "/ocs/v2.php/cloud/capabilities"
	capIndexed  = "/index.php/ocs/v2.php/cloud/capabilities"
)

// Adapter serves the Nextcloud-Bookmarks-compatible API.
type Adapter struct {
	ids    *identity.Service
	svc    *bookmarks.Service
	log    *slog.Logger
	routes []route
}

func New(ids *identity.Service, svc *bookmarks.Service, log *slog.Logger) *Adapter {
	if log == nil {
		log = slog.Default()
	}
	a := &Adapter{ids: ids, svc: svc, log: log}
	a.routes = a.routeTable()
	return a
}

// Register mounts the adapter. Both the /index.php form and the indexless alias are
// registered, each with and without a trailing slash, so no request is redirected.
func (a *Adapter) Register(r server.Registrar) {
	h := http.HandlerFunc(a.serve)
	for _, base := range []string{restIndexed, restPlain} {
		r.Handle(base, h)
		r.Handle(base+"/", h)
	}
	r.Handle(capPath, h)
	r.Handle(capIndexed, h)
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (a *Adapter) serve(w http.ResponseWriter, r *http.Request) {
	sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
	var credID int64
	defer func() {
		// Path only: never the query (which can carry bookmark URLs) or any header.
		a.log.Info("floccus request", "method", r.Method, "path", r.URL.Path, "credential", credID, "status", sw.status)
	}()

	user, secret, ok := r.BasicAuth()
	if !ok {
		a.unauthorized(sw)
		return
	}
	p, err := a.ids.Authenticate(r.Context(), user, secret)
	if err != nil {
		if errors.Is(err, identity.ErrUnauthorized) {
			a.unauthorized(sw)
		} else {
			a.internal(sw, err)
		}
		return
	}
	credID = p.CredentialID
	sc, err := a.svc.ResolveScope(r.Context(), p)
	if err != nil {
		if errors.Is(err, identity.ErrUnauthorized) {
			a.unauthorized(sw)
		} else {
			a.internal(sw, err)
		}
		return
	}

	path := r.URL.Path
	if path == capPath || path == capIndexed {
		if r.Method != http.MethodGet {
			a.methodNotAllowed(sw, []string{http.MethodGet})
			return
		}
		a.capabilities(sw)
		return
	}
	rest, ok := strings.CutPrefix(path, restIndexed)
	if !ok {
		rest, _ = strings.CutPrefix(path, restPlain)
	}
	a.dispatch(sw, r, sc, rest)
}

func (a *Adapter) unauthorized(w http.ResponseWriter) {
	writeJSON(w, http.StatusUnauthorized, envelopeError("unauthorized"))
}

func (a *Adapter) internal(w http.ResponseWriter, err error) {
	a.log.Error("floccus internal error", "error", err)
	writeJSON(w, http.StatusInternalServerError, envelopeError("internal server error"))
}

func (a *Adapter) methodNotAllowed(w http.ResponseWriter, allow []string) {
	w.Header().Set("Allow", strings.Join(allow, ", "))
	writeJSON(w, http.StatusMethodNotAllowed, envelopeError("method not allowed"))
}

func envelopeError(msg string) map[string]any {
	return map[string]any{"status": "error", "data": []string{msg}}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func success(w http.ResponseWriter, extra map[string]any) {
	body := map[string]any{"status": "success"}
	for k, v := range extra {
		body[k] = v
	}
	writeJSON(w, http.StatusOK, body)
}

// fail maps a domain error to its HTTP status and the standard error envelope.
func (a *Adapter) fail(w http.ResponseWriter, err error) {
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		writeJSON(w, http.StatusRequestEntityTooLarge, envelopeError("request body too large"))
	case errors.Is(err, bookmarks.ErrNotFound):
		writeJSON(w, http.StatusNotFound, envelopeError("not found"))
	case errors.Is(err, bookmarks.ErrForbidden):
		writeJSON(w, http.StatusForbidden, envelopeError(forbiddenMessage(err)))
	case errors.Is(err, bookmarks.ErrInvalid):
		writeJSON(w, http.StatusBadRequest, envelopeError(err.Error()))
	case errors.Is(err, bookmarks.ErrConflict):
		writeJSON(w, http.StatusConflict, envelopeError(err.Error()))
	case errors.Is(err, bookmarks.ErrLocked):
		writeJSON(w, http.StatusLocked, envelopeError("locked"))
	case errors.Is(err, identity.ErrUnauthorized):
		a.unauthorized(w)
	default:
		a.internal(w, err)
	}
}

func forbiddenMessage(err error) string {
	if strings.Contains(err.Error(), "read-only") {
		return "read-only share or credential"
	}
	return "forbidden: the bookmark has placements outside this share"
}

func (a *Adapter) badRequest(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, envelopeError(msg))
}

func (a *Adapter) capabilities(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]any{"ocs": map[string]any{
		"meta": map[string]any{"status": "ok", "statuscode": 200, "message": "OK"},
		"data": map[string]any{"capabilities": map[string]any{"bookmarks": map[string]any{
			"javascript-bookmarks": true,
			"hash-functions":       []string{"sha256"},
		}}},
	}})
}
