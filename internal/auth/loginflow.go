package auth

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
	"github.com/atikansari-ghr/BookmarksSync/internal/server"
)

// LoginFlowStore is optional so password-only Identity adapters remain compatible.
type LoginFlowStore interface {
	BeginLoginFlow(context.Context) (string, string, error)
	LoginFlowPending(context.Context, string) error
	ApproveLoginFlow(context.Context, string, int64) error
	PollLoginFlow(context.Context, string) (string, string, error)
}

var loginFlowHandle = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

func (h *Handler) configureLoginFlow() {
	h.loginFlows, _ = h.ids.(LoginFlowStore)
	h.loginFlowBegin = NewLimiter(Limits{MaxKeys: 10000, Window: time.Minute, UserFailures: 20, PeerFailures: 20}, h.now)
	h.loginFlowPoll = NewLimiter(Limits{MaxKeys: 10000, Window: time.Minute, UserFailures: 600, PeerFailures: 600}, h.now)
}

func (h *Handler) registerLoginFlow(r server.Registrar) {
	if h.loginFlows == nil {
		return
	}
	r.Handle("POST /index.php/login/v2", noStore(http.HandlerFunc(h.beginLoginFlow)))
	r.Handle("POST /login/v2/poll", noStore(http.HandlerFunc(h.pollLoginFlow)))
	r.Handle("GET /api/v1/auth/connect", h.RequireSession(http.HandlerFunc(h.inspectLoginFlow)))
	r.Handle("GET /api/v1/auth/connect/continuation", h.RequireSession(http.HandlerFunc(h.continueLoginFlow)))
	r.Handle("POST /api/v1/auth/connect/approve", h.RequireSession(h.RequireRecentAuth(http.HandlerFunc(h.approveLoginFlow))))
}

func (h *Handler) continueLoginFlow(w http.ResponseWriter, r *http.Request) {
	destination := "/auth/security"
	if cookie, err := r.Cookie("__Host-bookmarksync_connect"); err == nil && loginFlowHandle.MatchString(cookie.Value) {
		if h.loginFlows.LoginFlowPending(r.Context(), cookie.Value) == nil {
			destination = "/auth/connect?flow=" + cookie.Value
		}
	}
	http.SetCookie(w, &http.Cookie{Name: "__Host-bookmarksync_connect", Value: "", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	server.WriteJSON(w, http.StatusOK, map[string]string{"destination": destination})
}

func countLoginFlow(w http.ResponseWriter, r *http.Request, l *Limiter) bool {
	reservation, wait := l.Reserve("loginflow", r.RemoteAddr)
	if reservation == nil {
		writeRateLimited(w, wait)
		return false
	}
	reservation.Fail()
	return true
}

func (h *Handler) beginLoginFlow(w http.ResponseWriter, r *http.Request) {
	if !countLoginFlow(w, r, h.loginFlowBegin) {
		return
	}
	browser, poll, err := h.loginFlows.BeginLoginFlow(r.Context())
	if errors.Is(err, identity.ErrLoginFlowCapacity) {
		server.WriteError(w, 503, "login_flow_unavailable", "try again later")
		return
	}
	if err != nil {
		h.internal(w, "begin client connection", err)
		return
	}
	server.WriteJSON(w, 200, map[string]any{"poll": map[string]string{"token": poll, "endpoint": h.origin + "/login/v2/poll"}, "login": h.origin + "/auth/connect?flow=" + browser})
}

func flowNotFound(w http.ResponseWriter) {
	server.WriteError(w, 404, "not_found", "connection request is pending, expired or already used")
}

func (h *Handler) pollLoginFlow(w http.ResponseWriter, r *http.Request) {
	if !countLoginFlow(w, r, h.loginFlowPoll) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAuthBodyBytes)
	if r.ParseForm() != nil || len(r.PostForm["token"]) != 1 || !loginFlowHandle.MatchString(r.PostForm.Get("token")) {
		flowNotFound(w)
		return
	}
	username, password, err := h.loginFlows.PollLoginFlow(r.Context(), r.PostForm.Get("token"))
	if errors.Is(err, identity.ErrNotFound) {
		flowNotFound(w)
		return
	}
	if err != nil {
		h.internal(w, "redeem client connection", err)
		return
	}
	server.WriteJSON(w, 200, map[string]string{"server": h.origin, "loginName": username, "appPassword": password})
}

func (h *Handler) inspectLoginFlow(w http.ResponseWriter, r *http.Request) {
	flow := r.URL.Query().Get("flow")
	if !loginFlowHandle.MatchString(flow) {
		flowNotFound(w)
		return
	}
	if err := h.loginFlows.LoginFlowPending(r.Context(), flow); err != nil {
		if errors.Is(err, identity.ErrNotFound) {
			flowNotFound(w)
		} else {
			h.internal(w, "inspect client connection", err)
		}
		return
	}
	server.WriteJSON(w, 200, map[string]string{"status": "pending"})
}

func (h *Handler) approveLoginFlow(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Flow      string `json:"flow"`
		SessionID int64  `json:"sessionId"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if !loginFlowHandle.MatchString(in.Flow) {
		flowNotFound(w)
		return
	}
	sess, _, _ := FromContext(r.Context())
	if in.SessionID != sess.ID {
		server.WriteError(w, http.StatusConflict, "session_changed", "your signed-in account changed; reload before approving")
		return
	}
	err := h.loginFlows.ApproveLoginFlow(r.Context(), in.Flow, sess.ID)
	if errors.Is(err, identity.ErrNotFound) {
		flowNotFound(w)
		return
	}
	if err != nil {
		h.internal(w, "approve client connection", err)
		return
	}
	server.WriteJSON(w, 200, map[string]string{"status": "approved"})
}
