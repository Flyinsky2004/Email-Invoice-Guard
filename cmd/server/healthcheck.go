package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultProbeAddress = ":8080"
	defaultProbePath    = "/healthz"
	probeTimeout        = 5 * time.Second
	probeBodyLimit      = 4 << 10
)

// healthcheckTarget derives the local probe URL. It reads only non-secret
// settings, so the container healthcheck never needs the bot token or the
// encryption key.
func healthcheckTarget(getenv func(string) string) (string, error) {
	if raw := strings.TrimSpace(getenv("HEALTHCHECK_URL")); raw != "" {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return "", errors.New("HEALTHCHECK_URL: must be an absolute http(s) URL")
		}
		return u.String(), nil
	}

	addr := strings.TrimSpace(getenv("LISTEN_ADDRESS"))
	if addr == "" {
		addr = defaultProbeAddress
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("LISTEN_ADDRESS: %w", err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}

	path := strings.TrimSpace(getenv("HEALTH_PATH"))
	if path == "" {
		path = defaultProbePath
	}
	if !strings.HasPrefix(path, "/") {
		return "", errors.New("HEALTH_PATH: must start with /")
	}
	return "http://" + net.JoinHostPort(host, port) + path, nil
}

// probe performs one GET and accepts only HTTP 200.
func probe(ctx context.Context, client *http.Client, target string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, probeBodyLimit))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health endpoint returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// runHealthcheck returns a process exit code and writes diagnostics to out.
func runHealthcheck(getenv func(string) string, client *http.Client, out io.Writer) int {
	target, err := healthcheckTarget(getenv)
	if err != nil {
		fmt.Fprintln(out, "healthcheck:", err)
		return 1
	}
	if client == nil {
		client = &http.Client{Timeout: probeTimeout}
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	if err := probe(ctx, client, target); err != nil {
		fmt.Fprintln(out, "healthcheck:", err)
		return 1
	}
	return 0
}
