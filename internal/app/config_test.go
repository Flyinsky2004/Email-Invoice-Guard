package app

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testToken = "123456789:AAHfakeTokenValueForTests_123456"

func validKey() string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x2a}, encryptionKeySize))
}

// baseEnv installs a minimal valid environment and clears every optional
// variable so the developer's shell cannot leak into the assertions.
func baseEnv(t *testing.T) {
	t.Helper()
	t.Setenv("PUBLIC_BASE_URL", "https://invoice.example.com")
	t.Setenv("DATABASE_PATH", filepath.Join(t.TempDir(), "guard.db"))
	t.Setenv("ENCRYPTION_KEY", validKey())
	t.Setenv("ENCRYPTION_KEY_FILE", "")
	t.Setenv("TELEGRAM_BOT_TOKEN", testToken)
	t.Setenv("TELEGRAM_CHAT_ID", "-1001234567890")
	t.Setenv("TELEGRAM_ALLOWED_USER_IDS", "111,222")
	t.Setenv("LISTEN_ADDRESS", "")
	t.Setenv("HEALTH_PATH", "")
	t.Setenv("TIMEZONE", "")
	t.Setenv("REMINDER_TIME", "")
	t.Setenv("SYNC_INTERVAL", "")
	t.Setenv("SHUTDOWN_TIMEOUT", "")
	t.Setenv("TELEGRAM_BASE_URL", "")
	t.Setenv("TELEGRAM_POLL_TIMEOUT", "")
	t.Setenv("TELEGRAM_RATE_INTERVAL", "")
	t.Setenv("MICROSOFT_CLIENT_ID", "")
	t.Setenv("MICROSOFT_CLIENT_SECRET", "")
	t.Setenv("MICROSOFT_TENANT", "")
	t.Setenv("MICROSOFT_REDIRECT_URL", "")
	t.Setenv("MICROSOFT_AUTHORITY_URL", "")
	t.Setenv("MICROSOFT_GRAPH_BASE_URL", "")
	t.Setenv("MICROSOFT_SESSION_TTL", "")
	t.Setenv("IMAP_ADDRESS", "")
	t.Setenv("IMAP_SERVER_NAME", "")
	t.Setenv("IMAP_PUBLIC_BASE_URL", "")
	t.Setenv("IMAP_TIMEOUT", "")
	t.Setenv("IMAP_SESSION_TTL", "")
	t.Setenv("QQ_ALLOWED_DOMAINS", "")
	t.Setenv("TRUSTED_PROXY_CIDRS", "")
	t.Setenv("MANUAL_DEFAULT_TIME", "")
	t.Setenv("MANUAL_DEFAULT_TIMEZONE", "")
	t.Setenv("MANUAL_DAILY_RETRY", "")
	t.Setenv("MANUAL_DAILY_INTERVAL", "")
	t.Setenv("MANUAL_MONTH_END_POLICY", "")
	t.Setenv("MANUAL_SCHEDULER_INTERVAL", "")
	t.Setenv("MANUAL_WIZARD_TTL", "")
	t.Setenv("MANUAL_MAX_CATCHUP_MONTHS", "")
	t.Setenv("ALLOW_INSECURE_HTTP", "")
}

func TestLoadConfigDefaults(t *testing.T) {
	baseEnv(t)
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.ListenAddress != ":8080" || cfg.HealthPath != "/healthz" {
		t.Fatalf("listen/health defaults: %q %q", cfg.ListenAddress, cfg.HealthPath)
	}
	if cfg.Timezone != "Asia/Taipei" || cfg.ReminderTime != "09:00" {
		t.Fatalf("schedule defaults: %q %q", cfg.Timezone, cfg.ReminderTime)
	}
	if cfg.SyncInterval != 60*time.Second || cfg.ShutdownTimeout != 15*time.Second {
		t.Fatalf("durations: %v %v", cfg.SyncInterval, cfg.ShutdownTimeout)
	}
	if cfg.PublicBaseURL != "https://invoice.example.com" {
		t.Fatalf("public base: %q", cfg.PublicBaseURL)
	}
	if len(cfg.EncryptionKey) != encryptionKeySize {
		t.Fatalf("key length: %d", len(cfg.EncryptionKey))
	}
	if cfg.Telegram.ChatID != -1001234567890 || len(cfg.Telegram.AllowedUserIDs) != 2 {
		t.Fatalf("telegram: %+v", cfg.Telegram)
	}
	if cfg.Telegram.BaseURL != "https://api.telegram.org" || cfg.Telegram.PollTimeout != 25*time.Second {
		t.Fatalf("telegram defaults: %+v", cfg.Telegram)
	}
	if cfg.IMAP.Address != "imap.qq.com:993" || cfg.IMAP.PublicBaseURL != cfg.PublicBaseURL {
		t.Fatalf("imap defaults: %+v", cfg.IMAP)
	}
	if len(cfg.IMAP.AllowedDomains) != 2 || cfg.IMAP.AllowedDomains[0] != "qq.com" || cfg.IMAP.AllowedDomains[1] != "foxmail.com" {
		t.Fatalf("allowed domains: %v", cfg.IMAP.AllowedDomains)
	}
	if cfg.Microsoft.ClientID != "" || cfg.Microsoft.RedirectURL != "" {
		t.Fatalf("microsoft must be disabled without a client id: %+v", cfg.Microsoft)
	}
}

func TestLoadConfigAllowedIDsDeduped(t *testing.T) {
	baseEnv(t)
	t.Setenv("TELEGRAM_ALLOWED_USER_IDS", " 7, 7 ,9,")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.Telegram.AllowedUserIDs) != 2 || cfg.Telegram.AllowedUserIDs[0] != 7 || cfg.Telegram.AllowedUserIDs[1] != 9 {
		t.Fatalf("allowed ids: %v", cfg.Telegram.AllowedUserIDs)
	}
}

func TestLoadConfigRejectsMissingRequired(t *testing.T) {
	cases := []struct {
		name, key, value, want string
	}{
		{"public url", "PUBLIC_BASE_URL", "", "PUBLIC_BASE_URL"},
		{"token", "TELEGRAM_BOT_TOKEN", "", "TELEGRAM_BOT_TOKEN"},
		{"chat", "TELEGRAM_CHAT_ID", "", "TELEGRAM_CHAT_ID"},
		{"allowed", "TELEGRAM_ALLOWED_USER_IDS", "", "TELEGRAM_ALLOWED_USER_IDS"},
		{"key", "ENCRYPTION_KEY", "", "ENCRYPTION_KEY"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			baseEnv(t)
			t.Setenv(c.key, c.value)
			_, err := LoadConfig()
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want %s error, got %v", c.want, err)
			}
		})
	}
}

func TestLoadConfigRejectsBadEncryptionKeys(t *testing.T) {
	cases := []struct {
		name, value string
	}{
		{"not base64", "%%%not-base64%%%"},
		{"short", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 16))},
		{"long", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 33))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			baseEnv(t)
			t.Setenv("ENCRYPTION_KEY", c.value)
			_, err := LoadConfig()
			if err == nil || !strings.Contains(err.Error(), "ENCRYPTION_KEY") {
				t.Fatalf("want key error, got %v", err)
			}
			if strings.Contains(err.Error(), c.value) {
				t.Fatal("error leaked the key material")
			}
		})
	}
}

func TestLoadConfigKeyFile(t *testing.T) {
	baseEnv(t)
	t.Setenv("ENCRYPTION_KEY", "")
	dir := t.TempDir()
	path := filepath.Join(dir, "enc.key")
	if err := os.WriteFile(path, []byte(validKey()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENCRYPTION_KEY_FILE", path)
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.EncryptionKey) != encryptionKeySize {
		t.Fatalf("key length: %d", len(cfg.EncryptionKey))
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "owner-only") {
		t.Fatalf("want permission error, got %v", err)
	}
}

func TestLoadConfigRejectsBothKeySources(t *testing.T) {
	baseEnv(t)
	t.Setenv("ENCRYPTION_KEY_FILE", filepath.Join(t.TempDir(), "enc.key"))
	_, err := LoadConfig()
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("want mutual exclusion error, got %v", err)
	}
}

func TestLoadConfigRejectsBadSchedule(t *testing.T) {
	cases := []struct{ name, key, value string }{
		{"timezone", "TIMEZONE", "Mars/Olympus"},
		{"reminder", "REMINDER_TIME", "25:99"},
		{"sync", "SYNC_INTERVAL", "0s"},
		{"sync format", "SYNC_INTERVAL", "sixty"},
		{"shutdown", "SHUTDOWN_TIMEOUT", "-1s"},
		{"health path", "HEALTH_PATH", "healthz"},
		{"health collision", "HEALTH_PATH", "/setup/qq"},
		{"listen", "LISTEN_ADDRESS", "8080"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			baseEnv(t)
			t.Setenv(c.key, c.value)
			_, err := LoadConfig()
			if err == nil || !strings.Contains(err.Error(), c.key) {
				t.Fatalf("want %s error, got %v", c.key, err)
			}
		})
	}
}

func TestLoadConfigManualDefaults(t *testing.T) {
	baseEnv(t)
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	m := cfg.Manual
	if m.DefaultTime != "09:00" || m.DefaultTimezone != "Asia/Taipei" {
		t.Fatalf("manual clock defaults: %+v", m)
	}
	if !m.DailyRetry {
		t.Fatal("manual daily retry must default to true")
	}
	if m.DailyInterval != 24*time.Hour || m.SchedulerInterval != 30*time.Second || m.WizardTTL != 10*time.Minute {
		t.Fatalf("manual duration defaults: %+v", m)
	}
	if m.MonthEndPolicy != "last_day" || m.MaxCatchUpMonths != 24 {
		t.Fatalf("manual policy defaults: %+v", m)
	}
}

func TestLoadConfigManualOverrides(t *testing.T) {
	baseEnv(t)
	t.Setenv("MANUAL_DEFAULT_TIME", "07:45")
	t.Setenv("MANUAL_DEFAULT_TIMEZONE", "Europe/Berlin")
	t.Setenv("MANUAL_DAILY_RETRY", "false")
	t.Setenv("MANUAL_DAILY_INTERVAL", "12h")
	t.Setenv("MANUAL_MONTH_END_POLICY", "last_day")
	t.Setenv("MANUAL_SCHEDULER_INTERVAL", "5s")
	t.Setenv("MANUAL_WIZARD_TTL", "2m")
	t.Setenv("MANUAL_MAX_CATCHUP_MONTHS", "6")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	m := cfg.Manual
	if m.DefaultTime != "07:45" || m.DefaultTimezone != "Europe/Berlin" {
		t.Fatalf("manual clock overrides: %+v", m)
	}
	if m.DailyRetry {
		t.Fatal("MANUAL_DAILY_RETRY=false was ignored")
	}
	if m.DailyInterval != 12*time.Hour || m.SchedulerInterval != 5*time.Second || m.WizardTTL != 2*time.Minute {
		t.Fatalf("manual duration overrides: %+v", m)
	}
	if m.MonthEndPolicy != "last_day" || m.MaxCatchUpMonths != 6 {
		t.Fatalf("manual policy overrides: %+v", m)
	}
}

func TestLoadConfigRejectsBadManual(t *testing.T) {
	cases := []struct{ name, key, value string }{
		{"time", "MANUAL_DEFAULT_TIME", "9am"},
		{"time hours", "MANUAL_DEFAULT_TIME", "24:00"},
		{"timezone", "MANUAL_DEFAULT_TIMEZONE", "Mars/Olympus"},
		{"retry", "MANUAL_DAILY_RETRY", "sometimes"},
		{"daily interval", "MANUAL_DAILY_INTERVAL", "0s"},
		{"daily interval format", "MANUAL_DAILY_INTERVAL", "daily"},
		{"month end", "MANUAL_MONTH_END_POLICY", "first_day"},
		{"scheduler", "MANUAL_SCHEDULER_INTERVAL", "-1s"},
		{"wizard ttl", "MANUAL_WIZARD_TTL", "0s"},
		{"catch up zero", "MANUAL_MAX_CATCHUP_MONTHS", "0"},
		{"catch up high", "MANUAL_MAX_CATCHUP_MONTHS", "121"},
		{"catch up format", "MANUAL_MAX_CATCHUP_MONTHS", "many"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			baseEnv(t)
			t.Setenv(c.key, c.value)
			_, err := LoadConfig()
			if err == nil || !strings.Contains(err.Error(), c.key) {
				t.Fatalf("want %s error, got %v", c.key, err)
			}
		})
	}
}

func TestLoadConfigRejectsBadPublicURL(t *testing.T) {
	cases := []struct{ name, value string }{
		{"plain http", "http://invoice.example.com"},
		{"userinfo", "https://user:pass@invoice.example.com"},
		{"path", "https://invoice.example.com/app"},
		{"query", "https://invoice.example.com/?x=1"},
		{"relative", "/invoice"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			baseEnv(t)
			t.Setenv("PUBLIC_BASE_URL", c.value)
			_, err := LoadConfig()
			if err == nil || !strings.Contains(err.Error(), "PUBLIC_BASE_URL") {
				t.Fatalf("want PUBLIC_BASE_URL error, got %v", err)
			}
		})
	}
}

func TestLoadConfigLoopbackHTTPRequiresOptIn(t *testing.T) {
	baseEnv(t)
	t.Setenv("PUBLIC_BASE_URL", "http://localhost:8080")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "https is required") {
		t.Fatalf("want https error, got %v", err)
	}
	t.Setenv("ALLOW_INSECURE_HTTP", "true")
	if _, err := LoadConfig(); err != nil {
		t.Fatalf("loopback http should be allowed when opted in: %v", err)
	}
}

func TestLoadConfigMicrosoftOptionalAndValidated(t *testing.T) {
	t.Run("disabled without client id", func(t *testing.T) {
		baseEnv(t)
		t.Setenv("MICROSOFT_REDIRECT_URL", "https://elsewhere.example.com/oauth/microsoft/callback")
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if cfg.Microsoft.ClientID != "" || cfg.Microsoft.RedirectURL != "" {
			t.Fatalf("microsoft should stay disabled: %+v", cfg.Microsoft)
		}
	})

	t.Run("enabled requires secret and matching redirect", func(t *testing.T) {
		baseEnv(t)
		t.Setenv("MICROSOFT_CLIENT_ID", "client-id")
		t.Setenv("MICROSOFT_REDIRECT_URL", "https://invoice.example.com/oauth/microsoft/callback")
		if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "MICROSOFT_CLIENT_SECRET") {
			t.Fatalf("want secret error, got %v", err)
		}

		t.Setenv("MICROSOFT_CLIENT_SECRET", "shh")
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if cfg.Microsoft.Tenant != "consumers" || cfg.Microsoft.GraphBaseURL != defaultMSGraph {
			t.Fatalf("microsoft defaults: %+v", cfg.Microsoft)
		}

		t.Setenv("MICROSOFT_REDIRECT_URL", "https://evil.example.com/oauth/microsoft/callback")
		if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "PUBLIC_BASE_URL origin") {
			t.Fatalf("want origin error, got %v", err)
		}

		t.Setenv("MICROSOFT_REDIRECT_URL", "https://invoice.example.com/wrong")
		if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "path must be") {
			t.Fatalf("want path error, got %v", err)
		}
	})
}

func TestLoadConfigRejectsBadTelegramTokenWithoutLeaking(t *testing.T) {
	baseEnv(t)
	bad := "not-a-token"
	t.Setenv("TELEGRAM_BOT_TOKEN", bad)
	_, err := LoadConfig()
	if err == nil || !strings.Contains(err.Error(), "TELEGRAM_BOT_TOKEN") {
		t.Fatalf("want token error, got %v", err)
	}
	if strings.Contains(err.Error(), bad) {
		t.Fatal("error leaked the token")
	}
}

func TestLoadConfigRejectsBadIMAPAddress(t *testing.T) {
	baseEnv(t)
	t.Setenv("IMAP_ADDRESS", "imap.qq.com")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "IMAP_ADDRESS") {
		t.Fatalf("want IMAP_ADDRESS error, got %v", err)
	}
}

func TestLoadConfigPinsQQIMAPHost(t *testing.T) {
	t.Run("address host", func(t *testing.T) {
		baseEnv(t)
		t.Setenv("IMAP_ADDRESS", "imap.example.com:993")
		if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "only imap.qq.com") {
			t.Fatalf("want pinned host error, got %v", err)
		}
	})
	t.Run("server name", func(t *testing.T) {
		baseEnv(t)
		t.Setenv("IMAP_SERVER_NAME", "imap.example.com")
		if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "IMAP_SERVER_NAME") {
			t.Fatalf("want IMAP_SERVER_NAME error, got %v", err)
		}
	})
}

func TestLoadConfigQQAllowedDomains(t *testing.T) {
	t.Run("custom list is lowercased and deduped", func(t *testing.T) {
		baseEnv(t)
		t.Setenv("QQ_ALLOWED_DOMAINS", "QQ.com, foxmail.com ,my-alias.qq.com,qq.com")
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		want := []string{"qq.com", "foxmail.com", "my-alias.qq.com"}
		if len(cfg.IMAP.AllowedDomains) != len(want) {
			t.Fatalf("allowed domains: %v", cfg.IMAP.AllowedDomains)
		}
		for i := range want {
			if cfg.IMAP.AllowedDomains[i] != want[i] {
				t.Fatalf("allowed domains: %v", cfg.IMAP.AllowedDomains)
			}
		}
	})

	t.Run("empty keeps defaults", func(t *testing.T) {
		baseEnv(t)
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if len(cfg.IMAP.AllowedDomains) != 2 || cfg.IMAP.AllowedDomains[0] != "qq.com" {
			t.Fatalf("allowed domains: %v", cfg.IMAP.AllowedDomains)
		}
	})

	for _, bad := range []string{
		"https://qq.com", "qq.com/path", "*.qq.com", "qq.com:993",
		"user@qq.com", "localhost", "qq..com", "-qq.com", "qq.com.",
		"127.0.0.1", "qq.123",
	} {
		t.Run("reject "+bad, func(t *testing.T) {
			baseEnv(t)
			t.Setenv("QQ_ALLOWED_DOMAINS", bad)
			if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "QQ_ALLOWED_DOMAINS") {
				t.Fatalf("want QQ_ALLOWED_DOMAINS error, got %v", err)
			}
		})
	}
}

func TestLoadConfigTrustedProxyCIDRs(t *testing.T) {
	t.Run("empty means no proxy trust", func(t *testing.T) {
		baseEnv(t)
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if len(cfg.IMAP.TrustedProxyCIDRs) != 0 {
			t.Fatalf("want no trusted proxies, got %v", cfg.IMAP.TrustedProxyCIDRs)
		}
	})

	t.Run("canonicalised and deduped", func(t *testing.T) {
		baseEnv(t)
		t.Setenv("TRUSTED_PROXY_CIDRS", " 172.28.0.0/24, 10.20.0.0/16 ,172.28.0.0/24")
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		got := cfg.IMAP.TrustedProxyCIDRs
		if len(got) != 2 || got[0] != "172.28.0.0/24" || got[1] != "10.20.0.0/16" {
			t.Fatalf("trusted proxies: %v", got)
		}
	})

	for _, bad := range []string{"not-a-cidr", "0.0.0.0/0", "::/0", "172.28.1.5/24", "10.0.0.0/4"} {
		t.Run("reject "+bad, func(t *testing.T) {
			baseEnv(t)
			t.Setenv("TRUSTED_PROXY_CIDRS", bad)
			if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "TRUSTED_PROXY_CIDRS") {
				t.Fatalf("want TRUSTED_PROXY_CIDRS error, got %v", err)
			}
		})
	}
}
