package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"email-invoice-guard/internal/core"
)

type fakeStore struct {
	core.Store
	statusErr error
}

func (f fakeStore) Status(context.Context) (core.SystemStatus, error) {
	return core.SystemStatus{}, f.statusErr
}

func testConfig() core.Config {
	return core.Config{
		ListenAddress: ":0",
		HealthPath:    "/healthz",
	}
}

func serve(t *testing.T, h http.Handler, method, path string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHealthReportsStatusOnly(t *testing.T) {
	srv := NewWithMounts(testConfig(), nil, Mounts{})
	rec := serve(t, srv.Handler, http.MethodGet, "/healthz", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"status":"ok"}` {
		t.Fatalf("body: %q", got)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content type: %q", ct)
	}
	body := rec.Body.String()
	for _, forbidden := range []string{"token", "secret", "key", "database", "password"} {
		if strings.Contains(strings.ToLower(body), forbidden) {
			t.Fatalf("health body leaked %q: %s", forbidden, body)
		}
	}
}

func TestHealthAppliesSecurityHeaders(t *testing.T) {
	srv := NewWithMounts(testConfig(), nil, Mounts{})
	rec := serve(t, srv.Handler, http.MethodGet, "/healthz", nil)

	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
		"Cache-Control":          "no-store",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Fatalf("%s: got %q want %q", k, got, v)
		}
	}
}

func TestHealthReadinessFailureIsOpaque(t *testing.T) {
	srv := NewWithMounts(testConfig(), fakeStore{statusErr: errors.New("db path /secret/location is locked")}, Mounts{})
	rec := serve(t, srv.Handler, http.MethodGet, "/healthz", nil)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: %d", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"status":"unavailable"}` {
		t.Fatalf("body: %q", got)
	}
	if strings.Contains(rec.Body.String(), "secret") {
		t.Fatal("readiness error text leaked")
	}
}

func TestHealthRejectsNonReadMethods(t *testing.T) {
	srv := NewWithMounts(testConfig(), nil, Mounts{})
	rec := serve(t, srv.Handler, http.MethodPost, "/healthz", strings.NewReader("x"))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status: %d", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow != "GET, HEAD" {
		t.Fatalf("allow: %q", allow)
	}
}

func TestHealthPathIsConfigurable(t *testing.T) {
	cfg := testConfig()
	cfg.HealthPath = "/livez"
	srv := NewWithMounts(cfg, nil, Mounts{})

	if rec := serve(t, srv.Handler, http.MethodGet, "/livez", nil); rec.Code != http.StatusOK {
		t.Fatalf("livez status: %d", rec.Code)
	}
	if rec := serve(t, srv.Handler, http.MethodGet, "/healthz", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("old path should be gone, got %d", rec.Code)
	}
}

func TestRoutesMountExactPaths(t *testing.T) {
	var seen []string
	recorder := func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen = append(seen, name+" "+r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		})
	}
	srv := NewWithMounts(testConfig(), nil, Mounts{
		OAuth: recorder("oauth"),
		QQ:    recorder("qq"),
	})

	if rec := serve(t, srv.Handler, http.MethodGet, "/oauth/microsoft/callback", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("oauth status: %d", rec.Code)
	}
	if rec := serve(t, srv.Handler, http.MethodGet, "/setup/qq", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("qq status: %d", rec.Code)
	}
	if rec := serve(t, srv.Handler, http.MethodGet, "/setup/qq/extra", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unexpected nested route: %d", rec.Code)
	}
	want := []string{"oauth /oauth/microsoft/callback", "qq /setup/qq"}
	if len(seen) != len(want) || seen[0] != want[0] || seen[1] != want[1] {
		t.Fatalf("handlers: %v", seen)
	}
}

func TestNilMountsLeaveRoutesUnmounted(t *testing.T) {
	srv := NewWithMounts(testConfig(), nil, Mounts{})
	for _, path := range []string{"/oauth/microsoft/callback", "/setup/qq"} {
		if rec := serve(t, srv.Handler, http.MethodGet, path, nil); rec.Code != http.StatusNotFound {
			t.Fatalf("%s should be unmounted, got %d", path, rec.Code)
		}
	}
}

func TestServerTimeoutsAreSet(t *testing.T) {
	srv := NewWithMounts(testConfig(), nil, Mounts{})
	if srv.ReadHeaderTimeout <= 0 || srv.ReadTimeout <= 0 || srv.WriteTimeout <= 0 || srv.IdleTimeout <= 0 {
		t.Fatalf("timeouts: %+v", srv)
	}
	if srv.MaxHeaderBytes <= 0 {
		t.Fatalf("max header bytes: %d", srv.MaxHeaderBytes)
	}
	if srv.Addr != ":0" {
		t.Fatalf("addr: %q", srv.Addr)
	}
}

func TestPostBodyIsBounded(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	srv := NewWithMounts(testConfig(), nil, Mounts{QQ: handler})

	big := strings.Repeat("a", maxBodyBytes*2)
	rec := serve(t, srv.Handler, http.MethodPost, "/setup/qq", strings.NewReader(big))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body should be rejected, got %d", rec.Code)
	}
}

func TestContractNewMountsHealthWithoutSources(t *testing.T) {
	srv := New(testConfig(), nil, nil, nil)
	if rec := serve(t, srv.Handler, http.MethodGet, "/healthz", nil); rec.Code != http.StatusOK {
		t.Fatalf("health status: %d", rec.Code)
	}
	// With no Microsoft source and no QQ onboarding, the onboarding routes are
	// not mounted rather than serving a placeholder.
	for _, path := range []string{"/oauth/microsoft/callback", "/setup/qq"} {
		if rec := serve(t, srv.Handler, http.MethodGet, path, nil); rec.Code != http.StatusNotFound {
			t.Fatalf("%s should be unmounted, got %d", path, rec.Code)
		}
	}
}

func TestHealthPathCollisionFallsBack(t *testing.T) {
	cfg := testConfig()
	cfg.HealthPath = qqSetupPath
	qq := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	// Must not panic on duplicate mux registration.
	srv := NewWithMounts(cfg, nil, Mounts{QQ: qq})

	if rec := serve(t, srv.Handler, http.MethodGet, defaultHealthPath, nil); rec.Code != http.StatusOK {
		t.Fatalf("fallback health status: %d", rec.Code)
	}
	if rec := serve(t, srv.Handler, http.MethodGet, qqSetupPath, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("qq route status: %d", rec.Code)
	}
}

func TestDefaultListenAddressAndHealthPath(t *testing.T) {
	srv := NewWithMounts(core.Config{}, nil, Mounts{})
	if srv.Addr != defaultListenAddress {
		t.Fatalf("addr: %q", srv.Addr)
	}
	if rec := serve(t, srv.Handler, http.MethodGet, defaultHealthPath, nil); rec.Code != http.StatusOK {
		t.Fatalf("default health status: %d", rec.Code)
	}
}

func TestUnknownPathIsNotFoundWithHeaders(t *testing.T) {
	srv := NewWithMounts(testConfig(), nil, Mounts{})
	rec := serve(t, srv.Handler, http.MethodGet, "/nope", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: %d", rec.Code)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("security headers missing on 404")
	}
}

// Guard against accidental removal of the readiness timeout budget.
func TestHealthReadinessTimeoutConstant(t *testing.T) {
	if healthCheckTimeout <= 0 || healthCheckTimeout > 5*time.Second {
		t.Fatalf("health timeout: %v", healthCheckTimeout)
	}
}
