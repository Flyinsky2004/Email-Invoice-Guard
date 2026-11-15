// Package oauth exposes the Microsoft OAuth callback handler.
package oauth

import (
	"io"
	"net/http"

	"email-invoice-guard/internal/providers/microsoft"
)

const connectedPage = "<!doctype html><html><head><meta charset=\"utf-8\"><title>Connected</title></head>" +
	"<body><p>Microsoft account connected.</p></body></html>"

// Handler serves the authorization-code callback. It never echoes callback
// parameters or underlying errors back to the browser.
type Handler struct {
	source *microsoft.Source
}

// New mounts the Microsoft callback handler.
func New(source *microsoft.Source) http.Handler {
	return &Handler{source: source}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")

	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.source == nil {
		http.Error(w, "authorization unavailable", http.StatusInternalServerError)
		return
	}

	query := r.URL.Query()
	if query.Get("error") != "" {
		http.Error(w, "authorization failed", http.StatusBadRequest)
		return
	}
	state, code := query.Get("state"), query.Get("code")
	if state == "" || code == "" {
		http.Error(w, "invalid callback", http.StatusBadRequest)
		return
	}
	if _, err := h.source.CompleteAuthorization(r.Context(), state, code); err != nil {
		http.Error(w, "authorization failed", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, connectedPage)
}
