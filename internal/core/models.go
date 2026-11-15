// Package core contains frozen provider-independent domain contracts.
package core

import "time"

type ProviderType string

const (
	Microsoft ProviderType = "microsoft"
	QQ        ProviderType = "qq"
)

type InvoiceStatus string

const (
	PendingConfirmation InvoiceStatus = "PENDING_CONFIRMATION"
	PaidConfirmed       InvoiceStatus = "PAID_CONFIRMED"
	ReviewRequired      InvoiceStatus = "REVIEW_REQUIRED"
	Cancelled           InvoiceStatus = "CANCELLED"
)

type DeliveryStatus string

const (
	DeliveryPending DeliveryStatus = "PENDING"
	DeliverySending DeliveryStatus = "SENDING"
	DeliverySent    DeliveryStatus = "SENT"
	DeliveryFailed  DeliveryStatus = "FAILED"
	DeliveryUnknown DeliveryStatus = "UNKNOWN"
	DeliverySkipped DeliveryStatus = "SKIPPED"
)

type MailAccount struct {
	ID                   string
	Provider             ProviderType
	Address              string
	OwnerUserID          int64
	Enabled              bool
	LastError            string
	CreatedAt, UpdatedAt time.Time
}
type Credential struct {
	AccountID                                    string
	AccessToken, RefreshToken, AuthorizationCode string
	ExpiresAt                                    time.Time
}
type MailFolder struct {
	ID, AccountID, Name, Role string
	Enabled                   bool
}

// Date is a validated calendar date, serialized YYYY-MM-DD, never an instant.
type Date string
type Money struct {
	Minor    int64
	Currency string
}
type Address struct{ Name, Address string }
type Attachment struct {
	ID, Filename, ContentType string
	Size                      int64
}
type SyncMetadata struct {
	UIDValidity, UID uint32
	Fingerprint      string
	Historical       bool
	Phase            string
}
type NormalizedEmail struct {
	InternalEmailID, AccountID                     string
	ProviderType                                   ProviderType
	ProviderMessageID, InternetMessageID, FolderID string
	From                                           Address
	To, Cc                                         []Address
	Subject                                        string
	ReceivedAt                                     time.Time
	TextBody, HTMLBody                             string
	Attachments                                    []Attachment
	Headers                                        map[string][]string
	Links                                          []string
	SyncMetadata                                   SyncMetadata
	Classification                                 Classification
	CreatedAt                                      time.Time
}
type Classification struct{ ProviderID, Category string }
type SyncCursor struct {
	AccountID, FolderID, Value string
	UIDValidity, LastUID       uint32
	BaselineAt                 time.Time
	BaselineComplete           bool
	UpdatedAt                  time.Time
}
type SyncBatch struct {
	Emails     []NormalizedEmail
	NextCursor SyncCursor
	HasMore    bool
}
type Invoice struct {
	ID, AccountID, ProviderID, Number, SourceEmailID, ParserVersion string
	CreatedDate                                                     *Date
	DueDate                                                         Date
	PaymentMethod                                                   *string
	Currency                                                        string
	AmountDue                                                       Money
	SubTotal, Credit, Total, AccountBalance                         *Money
	URL                                                             string
	Status                                                          InvoiceStatus
	ReviewReasons                                                   []string
	Items                                                           []InvoiceItem
	ConfirmedBy                                                     *int64
	ConfirmedAt                                                     *time.Time
	CreatedAt, UpdatedAt                                            time.Time
}
type InvoiceItem struct {
	ID, InvoiceID                             string
	ProductModel, Hostname, OperatingSystem   *string
	ServiceStart, ServiceEnd                  *Date
	OriginalPrice, DiscountAmount, FinalPrice *Money
	PromotionalCode, DiscountType             *string
	DiscountBasisPoints                       *int64
	ProductID                                 *string
}
type InvoiceEvent struct {
	ID, InvoiceID, Action string
	ActorUserID           int64
	CreatedAt             time.Time
}
type InvoiceFilter struct {
	Status                InvoiceStatus
	AccountID, ProviderID string
	Limit                 int
}
type EmailFilter struct {
	AccountID, Search string
	Limit             int
}
type InvoiceReminder struct {
	ID, InvoiceID string
	ReminderDate  Date
	Status        DeliveryStatus
	OutboxID      string
	CreatedAt     time.Time
	SentAt        *time.Time
}
type Asset struct {
	ID, ProviderID, AccountID                          string
	ProductModel, Hostname, OperatingSystem, ProductID *string
	LastKnownServiceStart, LastKnownServiceEnd         *Date
	LastInvoiceID                                      *string
	CreatedAt, UpdatedAt                               time.Time
}
type AssetFilter struct {
	ProviderID, AccountID string
	Limit                 int
}
type DeliveryPayload struct {
	Kind, EmailID, InvoiceID, ReminderID, Text string

	// Manual fields are additive: an empty ManualPlanID means the payload is a
	// mail or invoice delivery and must render exactly as before.
	ManualPlanID        string
	ManualOccurrenceIDs []string
}
type Delivery struct {
	ID, DedupKey string
	Payload      DeliveryPayload
	Status       DeliveryStatus
	Attempts     int
	AvailableAt  time.Time
	LeaseUntil   *time.Time
	LastError    string
	CreatedAt    time.Time
}
type TelegramMessage struct {
	ID, OutboxID, InvoiceID string
	ChatID, MessageID       int64
	Part                    int
	CreatedAt               time.Time
}

// Session IDs are SHA-256 hashes of random bearer tokens; Payload is encrypted by storage.
type SecureSession struct {
	TokenHash, Purpose string
	Payload            []byte
	ExpiresAt          time.Time
}
type SystemStatus struct{ Accounts, Emails, PendingInvoices, PendingDeliveries int64 }
type ParserInfo struct{ Vendor, Version string }

// ManualPlanStatus is the lifecycle of an operator-declared recurring bill.
// A plan is independent of mail: only an explicit user action changes it.
type ManualPlanStatus string

const (
	ManualPlanActive    ManualPlanStatus = "ACTIVE"
	ManualPlanPaused    ManualPlanStatus = "PAUSED"
	ManualPlanCancelled ManualPlanStatus = "CANCELLED"
)

// ManualOccurrenceStatus is the payment state of one billing period. Only an
// authorized user confirmation can move an occurrence to PAID_CONFIRMED.
type ManualOccurrenceStatus string

const (
	ManualOccurrencePending   ManualOccurrenceStatus = "PENDING_CONFIRMATION"
	ManualOccurrencePaid      ManualOccurrenceStatus = "PAID_CONFIRMED"
	ManualOccurrenceCancelled ManualOccurrenceStatus = "CANCELLED"
)

// ManualDispatchDailySummary is the only dispatch kind in v1: one message per
// plan per local calendar day, regardless of how many occurrences are due.
const ManualDispatchDailySummary = "DAILY_SUMMARY"

// Manual reminder event types. They are stored verbatim and never re-derived.
const (
	ManualEventPlanCreated                = "PLAN_CREATED"
	ManualEventPlanUpdated                = "PLAN_UPDATED"
	ManualEventPlanPaused                 = "PLAN_PAUSED"
	ManualEventPlanResumed                = "PLAN_RESUMED"
	ManualEventPlanCancelled              = "PLAN_CANCELLED"
	ManualEventOccurrenceCreated          = "OCCURRENCE_CREATED"
	ManualEventReminderSent               = "REMINDER_SENT"
	ManualEventPaymentConfirmed           = "PAYMENT_CONFIRMED"
	ManualEventPaymentConfirmationRevoked = "PAYMENT_CONFIRMATION_REVOKED"
	ManualEventOccurrenceCancelled        = "OCCURRENCE_CANCELLED"
)

// ManualPlan is an operator-declared recurring bill. BillingDay is clamped to
// the last day of short months; NextDueDate is the first date still owed.
type ManualPlan struct {
	ID, Title, ProviderName, AssetID, Description string
	Amount                                        *Money
	BillingDay                                    int
	ReminderTime, Timezone, PaymentURL, Notes     string
	Status                                        ManualPlanStatus
	NextDueDate                                   Date
	CreatedAt, UpdatedAt                          time.Time
	CancelledAt                                   *time.Time
}

// ManualOccurrence is one billing period of a plan. Amount is the plan amount
// snapshot at creation so later edits cannot rewrite financial history.
type ManualOccurrence struct {
	ID, PlanID              string
	DueDate                 Date
	Amount                  *Money
	Status                  ManualOccurrenceStatus
	NotifiedAt, ConfirmedAt *time.Time
	ConfirmedBy             *int64
	CreatedAt, UpdatedAt    time.Time
}

// ManualDispatch links one summary outbox delivery to the occurrences it
// covered. Status mirrors the outbox row for the same outbox ID.
type ManualDispatch struct {
	ID, PlanID, OutboxID string
	LocalDate            Date
	Kind                 string
	OccurrenceIDs        []string
	Status               DeliveryStatus
	CreatedAt            time.Time
	SentAt               *time.Time
}

// ManualReminderEvent is an append-only audit record. Metadata is bounded JSON.
type ManualReminderEvent struct {
	ID, PlanID, OccurrenceID, EventType string
	ActorID                             int64
	CreatedAt                           time.Time
	Metadata                            string
}

type ManualPlanFilter struct {
	Status ManualPlanStatus
	Limit  int
}
type ManualOccurrenceFilter struct {
	PlanID        string
	Status        ManualOccurrenceStatus
	DueOnOrBefore Date
	Limit         int
}
type ManualReminderInput struct {
	Title, ProviderName, AssetID, Description string
	Amount                                    *Money
	BillingDay                                int
	ReminderTime, Timezone, PaymentURL, Notes string
}
