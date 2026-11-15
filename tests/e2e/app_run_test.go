package e2e_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"email-invoice-guard/internal/app"
	"email-invoice-guard/internal/core"
)

// TestAppRunStartsServesHealthAndShutsDown exercises the real runtime: storage
// open, module assembly, HTTP listener, Telegram polling, and graceful
// cancellation.
func TestAppRunStartsServesHealthAndShutsDown(t *testing.T) {
	tg := newMockTelegram(t)
	addr := freeAddr(t)
	cfg := core.Config{
		DatabasePath:    filepath.Join(t.TempDir(), "guard.db"),
		EncryptionKey:   []byte("01234567890123456789012345678901"),
		ListenAddress:   addr,
		PublicBaseURL:   "https://invoice.example.com",
		HealthPath:      "/healthz",
		Timezone:        "Asia/Taipei",
		ReminderTime:    "09:00",
		SyncInterval:    time.Hour,
		ShutdownTimeout: 3 * time.Second,
		Telegram: core.TelegramConfig{
			Token:          testToken,
			BaseURL:        tg.URL(),
			ChatID:         testChatID,
			AllowedUserIDs: []int64{testUserID},
			PollTimeout:    500 * time.Millisecond,
			RateInterval:   time.Millisecond,
		},
		// No account is configured, so the QQ endpoint is never dialed; it only
		// has to be the supported pinned host for runtime validation.
		IMAP: core.IMAPConfig{
			Address:        "imap.qq.com:993",
			ServerName:     "imap.qq.com",
			AllowedDomains: []string{"qq.com", "foxmail.com"},
			Timeout:        200 * time.Millisecond,
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx, cfg, discardLogger()) }()

	healthURL := "http://" + addr + "/healthz"
	deadline := time.Now().Add(8 * time.Second)
	var lastErr error
	ready := false
	for time.Now().Before(deadline) {
		resp, err := http.Get(healthURL)
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK && strings.Contains(string(body), `"status":"ok"`) {
				ready = true
				break
			}
			lastErr = fmt.Errorf("status %d body %q", resp.StatusCode, string(body))
		} else {
			lastErr = err
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		cancel()
		t.Fatalf("health endpoint never became ready: %v", lastErr)
	}

	waitFor(t, 3*time.Second, "telegram long polling", func() bool {
		return tg.count("getUpdates") > 0
	})

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("app.Run returned error: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("app.Run did not stop after cancellation")
	}

	if _, err := os.Stat(cfg.DatabasePath); err != nil {
		t.Fatalf("database not created: %v", err)
	}
}
