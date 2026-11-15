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

// InvoiceURLValidator is an optional merchant policy implemented by parsers
// and delegated by registries. Adding a vendor never needs Telegram changes.
type InvoiceURLValidator interface{ ValidateInvoiceURL(Invoice) bool }

// ManualReminderRepository persists operator-declared recurring plans and their
// occurrences. It is standalone on purpose: storage.Store satisfies it without
// widening Repository or changing any existing signature.
type ManualReminderRepository interface {
	CreatePlan(context.Context, ManualPlan, ManualReminderEvent) (ManualPlan, error)
	UpdatePlan(context.Context, ManualPlan, ManualReminderEvent) (ManualPlan, error)
	GetPlan(context.Context, string) (ManualPlan, error)
	ListPlans(context.Context, ManualPlanFilter) ([]ManualPlan, error)
	PausePlan(context.Context, string, int64, time.Time) (ManualPlan, error)
	// ResumePlan sets PAUSED -> ACTIVE and the new next_due_date atomically.
	// Missed periods are never backfilled.
	ResumePlan(context.Context, string, Date, int64, time.Time) (ManualPlan, error)
	// CancelPlan cancels pending occurrences and skips queued manual outbox
	// rows atomically.
	CancelPlan(context.Context, string, int64, time.Time) (ManualPlan, error)

	// CreateOccurrence inserts the occurrence, CAS-advances next_due_date from
	// occ.DueDate to the supplied next date, and writes OCCURRENCE_CREATED
	// atomically. It returns false when the plan is not ACTIVE, the occurrence
	// already exists, or next_due_date no longer matches occ.DueDate.
	CreateOccurrence(context.Context, ManualOccurrence, Date) (bool, error)
	// CreateDispatch atomically inserts one summary dispatch, its join rows and
	// the outbox delivery. It returns false when no supplied occurrence is still
	// pending or a dispatch for (planID, localDate, kind) already exists.
	CreateDispatch(context.Context, string, []string, ManualDispatch, Delivery) (bool, error)

	GetOccurrence(context.Context, string) (ManualOccurrence, error)
	ListOccurrences(context.Context, ManualOccurrenceFilter) ([]ManualOccurrence, error)
	TransitionOccurrence(context.Context, string, ManualOccurrenceStatus, ManualOccurrenceStatus, int64, time.Time) (ManualOccurrence, error)

	GetDispatch(context.Context, string) (ManualDispatch, error)
	ListPlanDispatches(context.Context, string) ([]ManualDispatch, error)
	ListManualMessages(context.Context, string) ([]TelegramMessage, error)
	ManualEvents(context.Context, string) ([]ManualReminderEvent, error)
}

// ManualReminderService is the user-facing manual reminder domain. It embeds
// ReminderService so the application can run it beside the invoice scheduler.
type ManualReminderService interface {
	ReminderService

	CreatePlan(context.Context, int64, ManualReminderInput) (ManualPlan, error)
	UpdatePlan(context.Context, int64, string, ManualReminderInput) (ManualPlan, error)
	PausePlan(context.Context, int64, string) (ManualPlan, error)
	ResumePlan(context.Context, int64, string) (ManualPlan, error)
	CancelPlan(context.Context, int64, string) (ManualPlan, error)
	ConfirmOccurrence(context.Context, int64, string) (ManualOccurrence, error)
	RevokeOccurrence(context.Context, int64, string) (ManualOccurrence, error)

	GetPlan(context.Context, string) (ManualPlan, error)
	ListPlans(context.Context, ManualPlanFilter) ([]ManualPlan, error)
	GetOccurrence(context.Context, string) (ManualOccurrence, error)
	ListOccurrences(context.Context, ManualOccurrenceFilter) ([]ManualOccurrence, error)
	ListPlanMessages(context.Context, string) ([]TelegramMessage, error)
	Events(context.Context, string) ([]ManualReminderEvent, error)
}
