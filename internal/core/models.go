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
type DeliveryPayload struct{ Kind, EmailID, InvoiceID, ReminderID, Text string }
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
