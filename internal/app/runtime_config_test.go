package app

import (
	"strings"
	"testing"
	"time"

	"email-invoice-guard/internal/core"
)

func runtimeConfig() core.Config {
	return core.Config{
		DatabasePath:    "/tmp/guard.db",
		EncryptionKey:   make([]byte, encryptionKeySize),
		ListenAddress:   ":8080",
		HealthPath:      "/healthz",
		PublicBaseURL:   "https://invoice.example.com",
		Timezone:        "Asia/Taipei",
		ReminderTime:    "09:00",
		SyncInterval:    time.Minute,
		ShutdownTimeout: 15 * time.Second,
		Telegram: core.TelegramConfig{
			Token:          testToken,
			ChatID:         -100,
			AllowedUserIDs: []int64{1},
		},
		IMAP: core.IMAPConfig{Address: "imap.qq.com:993"},
	}
}

func TestValidateRuntimeConfigAcceptsValid(t *testing.T) {
	if err := validateRuntimeConfig(runtimeConfig()); err != nil {
		t.Fatalf("validateRuntimeConfig: %v", err)
	}
}

func TestValidateRuntimeConfigRejectsMissingPieces(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*core.Config)
		want string
	}{
		{"database", func(c *core.Config) { c.DatabasePath = "" }, "DATABASE_PATH"},
		{"key", func(c *core.Config) { c.EncryptionKey = []byte("short") }, "encryption key"},
		{"listen", func(c *core.Config) { c.ListenAddress = "8080" }, "LISTEN_ADDRESS"},
		{"health", func(c *core.Config) { c.HealthPath = "healthz" }, "HEALTH_PATH"},
		{"timezone", func(c *core.Config) { c.Timezone = "Nowhere/City" }, "TIMEZONE"},
		{"reminder", func(c *core.Config) { c.ReminderTime = "9am" }, "REMINDER_TIME"},
		{"sync", func(c *core.Config) { c.SyncInterval = 0 }, "SYNC_INTERVAL"},
		{"shutdown", func(c *core.Config) { c.ShutdownTimeout = -1 }, "SHUTDOWN_TIMEOUT"},
		{"token", func(c *core.Config) { c.Telegram.Token = "" }, "TELEGRAM_BOT_TOKEN"},
		{"chat", func(c *core.Config) { c.Telegram.ChatID = 0 }, "TELEGRAM_CHAT_ID"},
		{"allowed", func(c *core.Config) { c.Telegram.AllowedUserIDs = nil }, "TELEGRAM_ALLOWED_USER_IDS"},
		{"public url", func(c *core.Config) { c.PublicBaseURL = "not a url" }, "PUBLIC_BASE_URL"},
		{"imap", func(c *core.Config) { c.IMAP.Address = "imap.qq.com" }, "IMAP_ADDRESS"},
		{"proxy", func(c *core.Config) { c.IMAP.TrustedProxyCIDRs = []string{"0.0.0.0/0"} }, "TRUSTED_PROXY_CIDRS"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := runtimeConfig()
			c.mut(&cfg)
			err := validateRuntimeConfig(cfg)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want %s error, got %v", c.want, err)
			}
		})
	}
}
