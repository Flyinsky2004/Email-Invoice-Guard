package app

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"email-invoice-guard/internal/core"
)

// validateRuntimeConfig is defense in depth for callers that build core.Config
// programmatically instead of going through LoadConfig. It repeats only the
// invariants the runtime cannot function without and never inspects secrets.
func validateRuntimeConfig(cfg core.Config) error {
	var problems []error
	add := func(err error) {
		if err != nil {
			problems = append(problems, err)
		}
	}

	if strings.TrimSpace(cfg.DatabasePath) == "" {
		add(errors.New("DATABASE_PATH must not be empty"))
	}
	if len(cfg.EncryptionKey) != encryptionKeySize {
		add(fmt.Errorf("encryption key must be exactly %d bytes", encryptionKeySize))
	}
	if _, _, err := net.SplitHostPort(cfg.ListenAddress); err != nil {
		add(errors.New("LISTEN_ADDRESS must be host:port"))
	}
	if !strings.HasPrefix(cfg.HealthPath, "/") {
		add(errors.New("HEALTH_PATH must start with /"))
	}
	if _, err := time.LoadLocation(cfg.Timezone); err != nil {
		add(fmt.Errorf("TIMEZONE: unknown location %q", cfg.Timezone))
	}
	if _, err := time.Parse("15:04", cfg.ReminderTime); err != nil {
		add(errors.New("REMINDER_TIME: must be HH:MM"))
	}
	if cfg.SyncInterval <= 0 {
		add(errors.New("SYNC_INTERVAL must be positive"))
	}
	if cfg.ShutdownTimeout <= 0 {
		add(errors.New("SHUTDOWN_TIMEOUT must be positive"))
	}
	if strings.TrimSpace(cfg.Telegram.Token) == "" {
		add(errors.New("TELEGRAM_BOT_TOKEN is required"))
	}
	if cfg.Telegram.ChatID == 0 {
		add(errors.New("TELEGRAM_CHAT_ID is required"))
	}
	if len(cfg.Telegram.AllowedUserIDs) == 0 {
		add(errors.New("TELEGRAM_ALLOWED_USER_IDS is required"))
	}
	if cfg.PublicBaseURL == "" {
		add(errors.New("PUBLIC_BASE_URL is required"))
	} else if u, err := url.Parse(cfg.PublicBaseURL); err != nil || u.Host == "" {
		add(errors.New("PUBLIC_BASE_URL must be an absolute URL"))
	}
	if cfg.IMAP.Address != "" {
		if err := validateHostPort("IMAP_ADDRESS", cfg.IMAP.Address); err != nil {
			add(err)
		} else if host, _, _ := net.SplitHostPort(cfg.IMAP.Address); !strings.EqualFold(host, defaultIMAPServer) {
			add(fmt.Errorf("IMAP_ADDRESS: only %s is supported", defaultIMAPServer))
		}
	}
	if cfg.IMAP.ServerName != "" && !strings.EqualFold(strings.TrimSpace(cfg.IMAP.ServerName), defaultIMAPServer) {
		add(fmt.Errorf("IMAP_SERVER_NAME: only %s is supported", defaultIMAPServer))
	}
	add(validateDomainList("QQ_ALLOWED_DOMAINS", cfg.IMAP.AllowedDomains))
	if _, err := parseCIDRList("TRUSTED_PROXY_CIDRS", strings.Join(cfg.IMAP.TrustedProxyCIDRs, ",")); err != nil {
		add(err)
	}

	if len(problems) > 0 {
		return fmt.Errorf("invalid runtime configuration: %w", errors.Join(problems...))
	}
	return nil
}
