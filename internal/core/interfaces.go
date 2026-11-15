package core

import (
	"context"
	"time"
)

type MailSource interface {
	Type() ProviderType
	ListFolders(context.Context, MailAccount) ([]MailFolder, error)
	Sync(context.Context, MailAccount, MailFolder, SyncCursor) (SyncBatch, error)
}
type InvoiceParser interface {
	Vendor() string
	Version() string
	Match(NormalizedEmail) bool
	Parse(NormalizedEmail) (*Invoice, error)
}
type ParserRegistry interface {
	Parse(context.Context, NormalizedEmail) (*Invoice, error)
	Parsers() []ParserInfo
}
type AccountRepository interface {
	SaveAccount(context.Context, MailAccount) error
	GetAccount(context.Context, string) (MailAccount, error)
	ListAccounts(context.Context) ([]MailAccount, error)
	RemoveAccount(context.Context, string) error
	GetCredential(context.Context, string) (Credential, error)
	PutCredential(context.Context, Credential) error
	ListFolders(context.Context, string) ([]MailFolder, error)
	SaveFolder(context.Context, MailFolder) error
	GetCursor(context.Context, string, string) (SyncCursor, error)
	SaveCursor(context.Context, SyncCursor) error
}
type EmailRepository interface {
	// SaveEmail returns persisted canonical email and false if a remote identity/fingerprint already exists.
	SaveEmail(context.Context, NormalizedEmail) (NormalizedEmail, bool, error)
	GetEmail(context.Context, string) (NormalizedEmail, error)
	ListEmails(context.Context, EmailFilter) ([]NormalizedEmail, error)
	RecordParseFailure(context.Context, string, string, string) error
}
type InvoiceRepository interface {
	// Existing invoice retains its business status; each source mail is recorded separately.
	SaveInvoice(context.Context, Invoice) (Invoice, bool, error)
	GetInvoice(context.Context, string) (Invoice, error)
	ListInvoices(context.Context, InvoiceFilter) ([]Invoice, error)
	// Transition atomically compares status, writes confirmation fields and event. Same target is idempotent.
	TransitionInvoice(context.Context, string, InvoiceStatus, InvoiceStatus, int64, time.Time) (Invoice, error)
	InvoiceEvents(context.Context, string) ([]InvoiceEvent, error)
	CreateReminder(context.Context, InvoiceReminder, Delivery) (bool, error)
	ListReminders(context.Context, string) ([]InvoiceReminder, error)
}
type OutboxRepository interface {
	Enqueue(context.Context, Delivery) (bool, error)
	// ClaimDelivery atomically claims due jobs; expired leases become UNKNOWN and retry with at-least-once semantics.
	ClaimDelivery(context.Context, time.Time, time.Duration) (*Delivery, error)
	CompleteDelivery(context.Context, string, []TelegramMessage, time.Time) error
	FailDelivery(context.Context, string, time.Time, bool, string) error
	SkipDelivery(context.Context, string, time.Time) error
	ListTelegramMessages(context.Context, string) ([]TelegramMessage, error)
	DeliveryMessages(context.Context, string) ([]TelegramMessage, error)
	RecordTelegramMessage(context.Context, TelegramMessage) error
}
type AssetRepository interface {
	SaveAsset(context.Context, Asset) (Asset, error)
	ListAssets(context.Context, AssetFilter) ([]Asset, error)
	LinkAssetInvoice(context.Context, string, string) error
	AssetInvoices(context.Context, string) ([]Invoice, error)
}
type SecurityRepository interface {
	PutSession(context.Context, SecureSession) error
	GetSession(context.Context, string, string, time.Time) (SecureSession, error)
	ConsumeSession(context.Context, string, string, time.Time) (SecureSession, error)
	GetSetting(context.Context, string) (string, error)
	SetSetting(context.Context, string, string) error
	Status(context.Context) (SystemStatus, error)
}
type Repository interface {
	AccountRepository
	EmailRepository
	InvoiceRepository
	OutboxRepository
	AssetRepository
	SecurityRepository
}
type Store interface {
	Repository
	Transaction(context.Context, func(Repository) error) error
	Close() error
}
type PaymentConfirmationService interface {
	Confirm(context.Context, string, int64) (Invoice, error)
	Undo(context.Context, string, int64) (Invoice, error)
	Cancel(context.Context, string, int64) (Invoice, error)
}
type ReminderService interface {
	Tick(context.Context, time.Time) error
	Run(context.Context) error
}
type SyncService interface {
	SyncAll(context.Context) error
	SyncAccount(context.Context, string) error
	Run(context.Context) error
}
type AccountOnboarding interface {
	Begin(context.Context, int64) (string, error)
}

// AssetLinker participates in the caller's transaction; ambiguous matches stay unlinked.
type AssetLinker interface {
	Reconcile(context.Context, Repository, Invoice) error
}
