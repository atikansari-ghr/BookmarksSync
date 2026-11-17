// Package server builds the HTTP handler and runs it with safe defaults.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/version"
)

const (
	// DefaultMaxBodyBytes caps request bodies unless Options.MaxBodyBytes is set.
	DefaultMaxBodyBytes int64 = 1 << 20

	apiPrefix = "/api/"
)

// Checker reports whether a dependency is usable. *database.DB satisfies it.
type Checker interface {
	Ping(ctx context.Context) error
}

// Registrar lets later components add routes. Handlers registered here are
// wrapped with Options.APIMiddleware (for example authentication that injects
// a request scope into the context). Built-in public endpoints are not.
type Registrar interface {
	Handle(pattern string, h http.Handler)
}

// Options configures NewHandler.
type Options struct {
	Version version.Info
	// Ready is checked by /readyz; it must be set.
	Ready Checker
	// APIMiddleware wraps every route added through Register.
	APIMiddleware func(http.Handler) http.Handler
	// Register adds routes using Go 1.22 "METHOD /path" patterns.
	Register     func(r Registrar)
	MaxBodyBytes int64
	// BodyLimit optionally selects a per-request body limit. A positive result
	// overrides MaxBodyBytes; zero or a negative result uses the default limit.
	BodyLimit func(*http.Request) int64
	Logger    *slog.Logger
}

type registrar struct {
	mux *http.ServeMux
	mw  func(http.Handler) http.Handler
}

func (r registrar) Handle(pattern string, h http.Handler) {
	if r.mw != nil {
		h = r.mw(h)
	}
	r.mux.Handle(pattern, h)
}

// NewHandler returns the root handler.
func NewHandler(opts Options) http.Handler {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.MaxBodyBytes <= 0 {
		opts.MaxBodyBytes = DefaultMaxBodyBytes
	}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := opts.Ready.Ping(ctx); err != nil {
			opts.Logger.Warn("readiness check failed", "error", err)
			WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("GET /api/v1/version", func(w http.ResponseWriter, _ *http.Request) {
		WriteJSON(w, http.StatusOK, opts.Version)
	})
	if opts.Register != nil {
		opts.Register(registrar{mux: mux, mw: opts.APIMiddleware})
	}
	mux.HandleFunc(apiPrefix, func(w http.ResponseWriter, r *http.Request) {
		notFoundOrMethodNotAllowed(mux, w, r)
	})

	var h http.Handler = &fallback{mux: mux}
	next := h
	h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := opts.MaxBodyBytes
		if opts.BodyLimit != nil {
			if selected := opts.BodyLimit(r); selected > 0 {
				limit = selected
			}
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next.ServeHTTP(w, r)
	})
	h = recoverer(h, opts.Logger)
	h = baseHeaders(h)
	return h
}

// fallback turns the mux's plain-text 405 for known paths into JSON.
type fallback struct{ mux *http.ServeMux }

func (f *fallback) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if _, pattern := f.mux.Handler(r); pattern == "" {
		if allow := allowedMethods(f.mux, r); len(allow) > 0 {
			w.Header().Set("Allow", strings.Join(allow, ", "))
			WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		WriteError(w, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	f.mux.ServeHTTP(w, r)
}

// notFoundOrMethodNotAllowed serves the /api/ catch-all: 405 if the path is
// registered for some other method, 404 otherwise.
func notFoundOrMethodNotAllowed(mux *http.ServeMux, w http.ResponseWriter, r *http.Request) {
	if allow := allowedMethods(mux, r); len(allow) > 0 {
		w.Header().Set("Allow", strings.Join(allow, ", "))
		WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	WriteError(w, http.StatusNotFound, "not_found", "resource not found")
}

// allowedMethods probes the mux for methods that have a real (non catch-all) route.
func allowedMethods(mux *http.ServeMux, r *http.Request) []string {
	var allow []string
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		probe := r.Clone(r.Context())
		probe.Method = m
		if _, pattern := mux.Handler(probe); pattern != "" && pattern != apiPrefix {
			allow = append(allow, m)
			if m == http.MethodGet {
				allow = append(allow, http.MethodHead)
			}
		}
	}
	sort.Strings(allow)
	return allow
}

func baseHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func recoverer(next http.Handler, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				log.Error("handler panic", "panic", rec, "path", r.URL.Path)
				WriteError(w, http.StatusInternalServerError, "internal_error", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// WriteJSON writes v as a JSON response.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError writes the standard JSON error envelope.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	WriteJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

// NewHTTPServer returns an http.Server with conservative timeouts and header limits.
func NewHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
}

// Serve runs h on ln until ctx is cancelled, then drains in-flight requests for
// up to grace. It returns nil after a clean shutdown.
func Serve(ctx context.Context, ln net.Listener, h http.Handler, grace time.Duration) error {
	srv := NewHTTPServer(ln.Addr().String(), h)
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), grace)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		_ = srv.Close()
		return err
	}
	if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
