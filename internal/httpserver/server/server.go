// Package server builds the public HTTP surface: a secret-free health probe
// and the two onboarding routes. Handlers are injected so the package stays
// independently compilable from the provider modules.
package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"email-invoice-guard/internal/core"
)

const (
	// maxBodyBytes bounds onboarding POST bodies; setup pages only submit a
	// short authorization code.
	maxBodyBytes = 64 << 10
	// healthCheckTimeout bounds the optional readiness probe.
	healthCheckTimeout = 2 * time.Second

	defaultListenAddress = ":8080"
	defaultHealthPath    = "/healthz"

	oauthCallbackPath = "/oauth/microsoft/callback"
	qqSetupPath       = "/setup/qq"
)

// Mounts carries the optional onboarding handlers. A nil handler leaves its
// route unmounted instead of serving a placeholder.
type Mounts struct {
	OAuth http.Handler // mounted at /oauth/microsoft/callback
	QQ    http.Handler // mounted at /setup/qq
}

// NewWithMounts assembles the HTTP server from injected handlers. It is the
// dependency-free constructor used by tests and by the Stage B contract
// wrapper.
func NewWithMounts(cfg core.Config, store core.Store, mounts Mounts) *http.Server {
	mux := http.NewServeMux()

	healthPath := cfg.HealthPath
	if healthPath == "" || healthPath == oauthCallbackPath || healthPath == qqSetupPath {
		// Never let a misconfigured health path collide with a reserved route
		// and panic the mux at startup.
		healthPath = defaultHealthPath
	}
	mux.Handle(healthPath, healthHandler(store))

	if mounts.OAuth != nil {
		mux.Handle(oauthCallbackPath, mounts.OAuth)
	}
	if mounts.QQ != nil {
		mux.Handle(qqSetupPath, mounts.QQ)
	}

	addr := cfg.ListenAddress
	if addr == "" {
		addr = defaultListenAddress
	}

	return &http.Server{
		Addr:              addr,
		Handler:           secureHeaders(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
}

// healthHandler reports liveness only. When a store is present it also checks
// readiness, but failures are reduced to a status string: no error text,
// counts, paths or secrets are exposed.
func healthHandler(store core.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeStatus(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if store != nil {
			ctx, cancel := context.WithTimeout(r.Context(), healthCheckTimeout)
			defer cancel()
			if _, err := store.Status(ctx); err != nil {
				writeStatus(w, http.StatusServiceUnavailable, "unavailable")
				return
			}
		}
		writeStatus(w, http.StatusOK, "ok")
	}
}

func writeStatus(w http.ResponseWriter, code int, status string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(struct {
		Status string `json:"status"`
	}{Status: status})
}

// secureHeaders applies conservative defaults to every response and bounds
// request bodies for state-changing methods.
func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}
