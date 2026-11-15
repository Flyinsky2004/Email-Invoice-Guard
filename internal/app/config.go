// Package app composes the runtime: strict configuration loading, service
// wiring and graceful lifecycle management.
package app

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"email-invoice-guard/internal/core"
)

// Defaults for optional settings. Required settings have no default.
const (
	defaultDatabasePath = "./data/invoice-guard.db"
	defaultListenAddr   = ":8080"
	defaultHealthPath   = "/healthz"
	defaultTimezone     = "Asia/Taipei"
	defaultReminderTime = "09:00"
	defaultSyncInterval = 60 * time.Second
	defaultShutdown     = 15 * time.Second
	defaultPollTimeout  = 25 * time.Second
	defaultRateInterval = time.Second
	defaultMSAuthority  = "https://login.microsoftonline.com/"
	defaultMSGraph      = "https://graph.microsoft.com/v1.0"
	defaultMSTenant     = "consumers"
	defaultMSSessionTTL = 10 * time.Minute
	defaultTGBaseURL    = "https://api.telegram.org"
	defaultIMAPAddr     = "imap.qq.com:993"
	defaultIMAPServer   = "imap.qq.com"
	defaultIMAPTimeout  = 30 * time.Second
	defaultIMAPSession  = 15 * time.Minute

	encryptionKeySize = 32
	microsoftCallback = "/oauth/microsoft/callback"
	qqSetupPath       = "/setup/qq"
)

// LoadConfig reads configuration from the process environment. It returns an
// error that names the offending variable without ever echoing secret values.
func LoadConfig() (core.Config, error) { return loadConfig(os.Getenv) }

type envLoader struct{ get func(string) string }

func (l envLoader) str(key string) string { return strings.TrimSpace(l.get(key)) }

func (l envLoader) strDefault(key, def string) string {
	if v := l.str(key); v != "" {
		return v
	}
	return def
}

func (l envLoader) duration(key string, def time.Duration) (time.Duration, error) {
	raw := l.str(key)
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid duration", key)
	}
	return d, nil
}

func loadConfig(get func(string) string) (core.Config, error) {
	l := envLoader{get: get}
	var cfg core.Config
	var problems []error
	add := func(err error) {
		if err != nil {
			problems = append(problems, err)
		}
	}

	allowInsecure := parseBool(l.str("ALLOW_INSECURE_HTTP"))

	// Public base URL is required: it builds the one-time QQ setup links.
	publicBase := l.str("PUBLIC_BASE_URL")
	var publicURL *url.URL
	if publicBase == "" {
		add(errors.New("PUBLIC_BASE_URL is required"))
	} else {
		u, err := parseBaseURL("PUBLIC_BASE_URL", publicBase, allowInsecure)
		if err != nil {
			add(err)
		} else {
			publicURL = u
			cfg.PublicBaseURL = strings.TrimSuffix(publicBase, "/")
		}
	}

	cfg.DatabasePath = l.strDefault("DATABASE_PATH", defaultDatabasePath)
	if err := validateDatabasePath(cfg.DatabasePath); err != nil {
		add(err)
	}

	key, err := loadEncryptionKey(l)
	if err != nil {
		add(err)
	} else {
		cfg.EncryptionKey = key
	}

	cfg.ListenAddress = l.strDefault("LISTEN_ADDRESS", defaultListenAddr)
	if err := validateListenAddress(cfg.ListenAddress); err != nil {
		add(err)
	}

	cfg.HealthPath = l.strDefault("HEALTH_PATH", defaultHealthPath)
	if !strings.HasPrefix(cfg.HealthPath, "/") {
		add(errors.New("HEALTH_PATH: must start with /"))
	} else if cfg.HealthPath == microsoftCallback || cfg.HealthPath == qqSetupPath {
		add(errors.New("HEALTH_PATH: conflicts with a reserved route"))
	}

	cfg.Timezone = l.strDefault("TIMEZONE", defaultTimezone)
	if _, err := time.LoadLocation(cfg.Timezone); err != nil {
		add(fmt.Errorf("TIMEZONE: unknown location %q", cfg.Timezone))
	}

	cfg.ReminderTime = l.strDefault("REMINDER_TIME", defaultReminderTime)
	if _, err := time.Parse("15:04", cfg.ReminderTime); err != nil {
		add(errors.New("REMINDER_TIME: must be HH:MM in 24h form"))
	}

	if cfg.SyncInterval, err = l.duration("SYNC_INTERVAL", defaultSyncInterval); err != nil {
		add(err)
	} else if cfg.SyncInterval <= 0 {
		add(errors.New("SYNC_INTERVAL: must be positive"))
	}

	if cfg.ShutdownTimeout, err = l.duration("SHUTDOWN_TIMEOUT", defaultShutdown); err != nil {
		add(err)
	} else if cfg.ShutdownTimeout <= 0 {
		add(errors.New("SHUTDOWN_TIMEOUT: must be positive"))
	}

	cfg.Telegram, err = loadTelegram(l, allowInsecure)
	add(err)

	cfg.Microsoft, err = loadMicrosoft(l, allowInsecure, publicURL)
	add(err)

	cfg.IMAP, err = loadIMAP(l, allowInsecure, cfg.PublicBaseURL)
	add(err)

	if len(problems) > 0 {
		return core.Config{}, fmt.Errorf("invalid configuration: %w", errors.Join(problems...))
	}
	return cfg, nil
}

func loadTelegram(l envLoader, allowInsecure bool) (core.TelegramConfig, error) {
	var cfg core.TelegramConfig
	var problems []error
	add := func(err error) {
		if err != nil {
			problems = append(problems, err)
		}
	}

	cfg.Token = l.str("TELEGRAM_BOT_TOKEN")
	if err := validateBotToken(cfg.Token); err != nil {
		add(err)
	}

	rawChat := l.str("TELEGRAM_CHAT_ID")
	if rawChat == "" {
		add(errors.New("TELEGRAM_CHAT_ID is required"))
	} else if id, err := strconv.ParseInt(rawChat, 10, 64); err != nil || id == 0 {
		add(errors.New("TELEGRAM_CHAT_ID: must be a non-zero integer"))
	} else {
		cfg.ChatID = id
	}

	allowed, err := parseInt64List("TELEGRAM_ALLOWED_USER_IDS", l.str("TELEGRAM_ALLOWED_USER_IDS"))
	if err != nil {
		add(err)
	} else {
		cfg.AllowedUserIDs = allowed
	}

	cfg.BaseURL = l.strDefault("TELEGRAM_BASE_URL", defaultTGBaseURL)
	if err := validateHTTPSURL("TELEGRAM_BASE_URL", cfg.BaseURL, allowInsecure); err != nil {
		add(err)
	}

	if cfg.PollTimeout, err = l.duration("TELEGRAM_POLL_TIMEOUT", defaultPollTimeout); err != nil {
		add(err)
	} else if cfg.PollTimeout <= 0 || cfg.PollTimeout >= time.Minute {
		add(errors.New("TELEGRAM_POLL_TIMEOUT: must be between 0s and 1m"))
	}

	if cfg.RateInterval, err = l.duration("TELEGRAM_RATE_INTERVAL", defaultRateInterval); err != nil {
		add(err)
	} else if cfg.RateInterval < 0 {
		add(errors.New("TELEGRAM_RATE_INTERVAL: must not be negative"))
	}

	if len(problems) > 0 {
		return core.TelegramConfig{}, errors.Join(problems...)
	}
	return cfg, nil
}

func loadMicrosoft(l envLoader, allowInsecure bool, publicURL *url.URL) (core.MicrosoftConfig, error) {
	clientID := l.str("MICROSOFT_CLIENT_ID")
	if clientID == "" {
		// Microsoft onboarding is optional; QQ keeps working without it.
		return core.MicrosoftConfig{}, nil
	}

	var cfg core.MicrosoftConfig
	var problems []error
	add := func(err error) {
		if err != nil {
			problems = append(problems, err)
		}
	}

	cfg.ClientID = clientID
	cfg.ClientSecret = l.str("MICROSOFT_CLIENT_SECRET")
	if cfg.ClientSecret == "" {
		add(errors.New("MICROSOFT_CLIENT_SECRET is required when MICROSOFT_CLIENT_ID is set"))
	}

	cfg.Tenant = l.strDefault("MICROSOFT_TENANT", defaultMSTenant)
	cfg.AuthorityURL = l.strDefault("MICROSOFT_AUTHORITY_URL", defaultMSAuthority)
	if err := validateHTTPSURL("MICROSOFT_AUTHORITY_URL", cfg.AuthorityURL, allowInsecure); err != nil {
		add(err)
	}
	cfg.GraphBaseURL = l.strDefault("MICROSOFT_GRAPH_BASE_URL", defaultMSGraph)
	if err := validateHTTPSURL("MICROSOFT_GRAPH_BASE_URL", cfg.GraphBaseURL, allowInsecure); err != nil {
		add(err)
	}

	redirect := l.str("MICROSOFT_REDIRECT_URL")
	if redirect == "" {
		add(errors.New("MICROSOFT_REDIRECT_URL is required when MICROSOFT_CLIENT_ID is set"))
	} else if publicURL != nil {
		if err := validateRedirectURL(redirect, publicURL); err != nil {
			add(err)
		} else {
			cfg.RedirectURL = redirect
		}
	}

	var err error
	if cfg.SessionTTL, err = l.duration("MICROSOFT_SESSION_TTL", defaultMSSessionTTL); err != nil {
		add(err)
	} else if cfg.SessionTTL <= 0 {
		add(errors.New("MICROSOFT_SESSION_TTL: must be positive"))
	}

	if len(problems) > 0 {
		return core.MicrosoftConfig{}, errors.Join(problems...)
	}
	return cfg, nil
}

func loadIMAP(l envLoader, allowInsecure bool, publicBase string) (core.IMAPConfig, error) {
	var cfg core.IMAPConfig
	var problems []error
	add := func(err error) {
		if err != nil {
			problems = append(problems, err)
		}
	}

	cfg.Address = l.strDefault("IMAP_ADDRESS", defaultIMAPAddr)
	if err := validateHostPort("IMAP_ADDRESS", cfg.Address); err != nil {
		add(err)
	}

	cfg.ServerName = l.strDefault("IMAP_SERVER_NAME", defaultIMAPServer)

	var err error
	// Trusted proxy peers are opt-in. An empty list means X-Forwarded-Proto is
	// never honoured and the QQ setup page requires a direct TLS connection.
	cfg.TrustedProxyCIDRs, err = parseCIDRList("TRUSTED_PROXY_CIDRS", l.str("TRUSTED_PROXY_CIDRS"))
	add(err)

	cfg.PublicBaseURL = l.strDefault("IMAP_PUBLIC_BASE_URL", publicBase)
	if cfg.PublicBaseURL != "" {
		if err := validateHTTPSURL("IMAP_PUBLIC_BASE_URL", cfg.PublicBaseURL, allowInsecure); err != nil {
			add(err)
		}
	}

	if cfg.Timeout, err = l.duration("IMAP_TIMEOUT", defaultIMAPTimeout); err != nil {
		add(err)
	} else if cfg.Timeout <= 0 {
		add(errors.New("IMAP_TIMEOUT: must be positive"))
	}

	if cfg.SessionTTL, err = l.duration("IMAP_SESSION_TTL", defaultIMAPSession); err != nil {
		add(err)
	} else if cfg.SessionTTL <= 0 {
		add(errors.New("IMAP_SESSION_TTL: must be positive"))
	}

	if len(problems) > 0 {
		return core.IMAPConfig{}, errors.Join(problems...)
	}
	return cfg, nil
}

func loadEncryptionKey(l envLoader) ([]byte, error) {
	fromEnv := l.str("ENCRYPTION_KEY")
	fromFile := l.str("ENCRYPTION_KEY_FILE")
	switch {
	case fromEnv != "" && fromFile != "":
		return nil, errors.New("ENCRYPTION_KEY and ENCRYPTION_KEY_FILE are mutually exclusive")
	case fromEnv != "":
		return decodeKey("ENCRYPTION_KEY", fromEnv)
	case fromFile != "":
		info, err := os.Stat(fromFile)
		if err != nil {
			return nil, fmt.Errorf("ENCRYPTION_KEY_FILE: %w", err)
		}
		if !info.Mode().IsRegular() {
			return nil, errors.New("ENCRYPTION_KEY_FILE: not a regular file")
		}
		if info.Mode().Perm()&0o077 != 0 {
			return nil, errors.New("ENCRYPTION_KEY_FILE: permissions must be owner-only (0600)")
		}
		raw, err := os.ReadFile(fromFile)
		if err != nil {
			return nil, fmt.Errorf("ENCRYPTION_KEY_FILE: %w", err)
		}
		return decodeKey("ENCRYPTION_KEY_FILE", strings.TrimSpace(string(raw)))
	default:
		return nil, errors.New("ENCRYPTION_KEY or ENCRYPTION_KEY_FILE is required")
	}
}

func decodeKey(name, encoded string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("%s: must be standard base64", name)
	}
	if len(key) != encryptionKeySize {
		return nil, fmt.Errorf("%s: must decode to exactly %d bytes", name, encryptionKeySize)
	}
	return key, nil
}

func validateBotToken(token string) error {
	if token == "" {
		return errors.New("TELEGRAM_BOT_TOKEN is required")
	}
	id, secret, ok := strings.Cut(token, ":")
	if !ok || secret == "" {
		return errors.New("TELEGRAM_BOT_TOKEN: malformed token")
	}
	if _, err := strconv.ParseUint(id, 10, 64); err != nil {
		return errors.New("TELEGRAM_BOT_TOKEN: malformed token")
	}
	for _, r := range secret {
		if !(r == '-' || r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			return errors.New("TELEGRAM_BOT_TOKEN: malformed token")
		}
	}
	return nil
}

func parseInt64List(key, raw string) ([]int64, error) {
	if raw == "" {
		return nil, fmt.Errorf("%s is required", key)
	}
	seen := make(map[int64]struct{})
	var out []int64
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("%s: must be comma-separated positive integers", key)
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: at least one user ID is required", key)
	}
	return out, nil
}

// parseCIDRList validates a comma-separated list of proxy networks. The
// network address must be written explicitly (no host bits) and the catch-all
// prefixes are rejected so a proxy trust boundary can never become "everyone".
func parseCIDRList(key, raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	seen := make(map[string]struct{})
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		ip, network, err := net.ParseCIDR(part)
		if err != nil {
			return nil, fmt.Errorf("%s: invalid CIDR %q", key, part)
		}
		ones, bits := network.Mask.Size()
		if ones == 0 {
			return nil, fmt.Errorf("%s: %q trusts every address", key, part)
		}
		if bits == 32 && ones < 8 || bits == 128 && ones < 16 {
			return nil, fmt.Errorf("%s: %q is too broad", key, part)
		}
		if !ip.Equal(network.IP) {
			return nil, fmt.Errorf("%s: %q must be a network address, e.g. 172.28.0.0/24", key, part)
		}
		canonical := network.String()
		if _, dup := seen[canonical]; dup {
			continue
		}
		seen[canonical] = struct{}{}
		out = append(out, canonical)
	}
	return out, nil
}

func validateDatabasePath(path string) error {
	if path == "" {
		return errors.New("DATABASE_PATH must not be empty")
	}
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return errors.New("DATABASE_PATH: points to a directory")
	}
	return nil
}

func validateListenAddress(addr string) error {
	if addr == "" {
		return errors.New("LISTEN_ADDRESS must not be empty")
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return errors.New("LISTEN_ADDRESS: must be host:port")
	}
	return nil
}

func validateHostPort(key, addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return fmt.Errorf("%s: must be host:port", key)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("%s: invalid port", key)
	}
	return nil
}

func parseBaseURL(key, raw string, allowInsecure bool) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("%s: must be an absolute URL", key)
	}
	if u.User != nil {
		return nil, fmt.Errorf("%s: userinfo is not allowed", key)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("%s: query and fragment are not allowed", key)
	}
	if u.Path != "" && u.Path != "/" {
		return nil, fmt.Errorf("%s: must not include a path", key)
	}
	if err := checkScheme(key, u, allowInsecure); err != nil {
		return nil, err
	}
	return u, nil
}

func validateHTTPSURL(key, raw string, allowInsecure bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%s: must be an absolute URL", key)
	}
	if u.User != nil {
		return fmt.Errorf("%s: userinfo is not allowed", key)
	}
	return checkScheme(key, u, allowInsecure)
}

func checkScheme(key string, u *url.URL, allowInsecure bool) error {
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if allowInsecure && isLoopbackHost(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("%s: https is required", key)
	default:
		return fmt.Errorf("%s: unsupported scheme", key)
	}
}

func validateRedirectURL(raw string, public *url.URL) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errors.New("MICROSOFT_REDIRECT_URL: must be an absolute URL")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("MICROSOFT_REDIRECT_URL: userinfo, query and fragment are not allowed")
	}
	if !strings.EqualFold(u.Scheme, public.Scheme) || !strings.EqualFold(u.Host, public.Host) {
		return errors.New("MICROSOFT_REDIRECT_URL: must share the PUBLIC_BASE_URL origin")
	}
	if u.Path != microsoftCallback {
		return fmt.Errorf("MICROSOFT_REDIRECT_URL: path must be %s", microsoftCallback)
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func parseBool(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
