package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestHealthcheckTargetFromListenAddress(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"defaults", map[string]string{}, "http://127.0.0.1:8080/healthz"},
		{"wildcard ipv4", map[string]string{"LISTEN_ADDRESS": "0.0.0.0:9090"}, "http://127.0.0.1:9090/healthz"},
		{"wildcard ipv6", map[string]string{"LISTEN_ADDRESS": "[::]:8081"}, "http://127.0.0.1:8081/healthz"},
		{"explicit host", map[string]string{"LISTEN_ADDRESS": "127.0.0.1:7000", "HEALTH_PATH": "/livez"}, "http://127.0.0.1:7000/livez"},
		{"url override", map[string]string{"HEALTHCHECK_URL": "http://127.0.0.1:9999/ready"}, "http://127.0.0.1:9999/ready"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := healthcheckTarget(env(c.env))
			if err != nil {
				t.Fatalf("healthcheckTarget: %v", err)
			}
			if got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestHealthcheckTargetRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
	}{
		{"missing port", map[string]string{"LISTEN_ADDRESS": "8080"}},
		{"bad scheme", map[string]string{"HEALTHCHECK_URL": "ftp://127.0.0.1/healthz"}},
		{"relative url", map[string]string{"HEALTHCHECK_URL": "/healthz"}},
		{"bad path", map[string]string{"HEALTH_PATH": "healthz"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := healthcheckTarget(env(c.env)); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestProbeAcceptsOnlyOK(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ok.Close()
	if err := probe(t.Context(), ok.Client(), ok.URL); err != nil {
		t.Fatalf("probe ok server: %v", err)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer bad.Close()
	if err := probe(t.Context(), bad.Client(), bad.URL); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("want 503 error, got %v", err)
	}
}

func TestRunHealthcheckExitCodes(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ok.Close()

	var out bytes.Buffer
	if code := runHealthcheck(env(map[string]string{"HEALTHCHECK_URL": ok.URL}), ok.Client(), &out); code != 0 {
		t.Fatalf("healthy exit code: %d (%s)", code, out.String())
	}

	out.Reset()
	if code := runHealthcheck(env(map[string]string{"LISTEN_ADDRESS": "nonsense"}), nil, &out); code != 1 {
		t.Fatalf("invalid config exit code: %d", code)
	}
	if !strings.Contains(out.String(), "healthcheck:") {
		t.Fatalf("missing diagnostic: %q", out.String())
	}
}
