package network

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
)

type PolicyLoader func(context.Context) (Policy, error)

// Gate enforces client-address rules before any application or built-in route.
// LoadPolicy is called for each request so administrator changes and local CLI
// recovery apply immediately without restarting the server.
type Gate struct {
	TrustedProxies  []netip.Prefix
	ManagementCIDRs []netip.Prefix
	Lookup          CountryLookup
	LoadPolicy      PolicyLoader
	Logger          *slog.Logger
}

// Middleware denies unknown or disallowed clients with the same generic response,
// without revealing whether a route exists. It rewrites RemoteAddr to the resolved
// visitor IP for downstream authentication limits and audit logs.
func (g *Gate) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if g == nil {
			next.ServeHTTP(w, r)
			return
		}
		log := g.Logger
		if log == nil {
			log = slog.Default()
		}
		ip, err := ResolveClientIP(r, g.TrustedProxies)
		if err != nil {
			log.Warn("client access denied", "reason", "client_address_unresolved")
			writeAccessError(w, http.StatusForbidden, "access_denied", "access denied")
			return
		}
		if contains(g.ManagementCIDRs, ip) {
			request := r.Clone(r.Context())
			request.RemoteAddr = net.JoinHostPort(ip.String(), "0")
			next.ServeHTTP(w, request)
			return
		}
		policy := DefaultPolicy()
		if g.LoadPolicy != nil {
			policy, err = g.LoadPolicy(r.Context())
			if err != nil {
				log.Warn("client access policy unavailable", "error", err.Error())
				writeAccessError(w, http.StatusServiceUnavailable, "service_unavailable", "service unavailable")
				return
			}
		}
		allowed, err := CountryAllowed(ip, policy, g.ManagementCIDRs, g.Lookup)
		if err != nil {
			log.Warn("client access policy evaluation failed", "error", err.Error())
			writeAccessError(w, http.StatusServiceUnavailable, "service_unavailable", "service unavailable")
			return
		}
		if !allowed {
			writeAccessError(w, http.StatusForbidden, "access_denied", "access denied")
			return
		}
		request := r.Clone(r.Context())
		request.RemoteAddr = net.JoinHostPort(ip.String(), "0")
		next.ServeHTTP(w, request)
	})
}

func writeAccessError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": message}})
}
