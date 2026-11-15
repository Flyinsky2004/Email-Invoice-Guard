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
	Address, ServerName, PublicBaseURL string
	Timeout, SessionTTL                time.Duration
}
