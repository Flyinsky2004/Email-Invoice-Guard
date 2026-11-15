package core

import "time"

type Config struct {
	DatabasePath                             string
	EncryptionKey                            []byte
	ListenAddress, PublicBaseURL, HealthPath string
	Timezone, ReminderTime                   string
	SyncInterval, ShutdownTimeout            time.Duration
	Telegram                                 TelegramConfig
	Microsoft                                MicrosoftConfig
	IMAP                                     IMAPConfig
	Manual                                   ManualReminderConfig
}
type TelegramConfig struct {
	Token, BaseURL            string
	ChatID                    int64
	AllowedUserIDs            []int64
	PollTimeout, RateInterval time.Duration
}
type MicrosoftConfig struct {
	ClientID, ClientSecret, Tenant, RedirectURL, AuthorityURL, GraphBaseURL string
	SessionTTL                                                              time.Duration
}
type IMAPConfig struct {
	AllowedDomains []string // Explicit QQ-hosted aliases; authentication is still against verified QQ TLS.

	TrustedProxyCIDRs []string // Only these remote peers may assert X-Forwarded-Proto: https.

	Address, ServerName, PublicBaseURL string
	Timeout, SessionTTL                time.Duration
}

// ManualReminderConfig configures operator-declared recurring reminders. The
// zero value is not usable: app.LoadConfig fills every field with a default.
type ManualReminderConfig struct {
	DefaultTime       string        // "09:00"
	DefaultTimezone   string        // "Asia/Taipei"
	DailyRetry        bool          // true
	DailyInterval     time.Duration // 24h
	MonthEndPolicy    string        // "last_day" only in v1
	SchedulerInterval time.Duration // default 30s
	WizardTTL         time.Duration // default 10m
	MaxCatchUpMonths  int           // default 24
}
