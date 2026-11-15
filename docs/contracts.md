# Frozen contracts v1

Authoritative compilable types: internal/core/*.go. Workers cannot change these files. Errors use errors.Is and core sentinels. Context cancellation is honored. Optional fields use pointers; no invented values. Constructors below are the required cross-module public API (additional private helpers allowed).

- microsoft.New(repo core.Repository, cfg core.MicrosoftConfig, client *http.Client) *Source. Source implements core.MailSource and core.AccountOnboarding (Begin(ctx,actorID) (string,error)); also CompleteAuthorization(ctx,state,code) (core.MailAccount,error). Runtime defaults to consumers Microsoft authority, Graph v1.0, request Mail.Read + offline_access (User.Read only if explicitly needed for /me identity). Bind resulting account owner to session creator, validate /me address, never use ID token without verification. HTTP injected for mocks; reject untrusted pagination/redirect origins before attaching credentials.
- oauth.New(source *microsoft.Source) http.Handler; mounted at /oauth/microsoft/callback.
- imap.New(repo core.Repository,cfg core.IMAPConfig) *Source implements MailSource (Type QQ). QQ endpoint defaults imap.qq.com:993; cfg supports fake TLS server in tests using injected Dial function or client. Expose Source.Validate(ctx,address,authorizationCode) error for onboarding connectivity check.
- qq.NewOnboarding(repo core.Repository,cfg core.IMAPConfig,source *imap.Source) *Onboarding implements AccountOnboarding and http.Handler; mounted /setup/qq. Page GET validates hashed bearer token without consuming; successful POST consumes token exactly once and creates account+credential transaction where Store available. Failed authentication never stores a working account. Reject HTTP credential submission; reverse-proxy HTTPS trust must be explicitly configured and documented.
- mailproc.Normalize(core.NormalizedEmail) core.NormalizedEmail, mailproc.Classify(core.NormalizedEmail) core.Classification. Export HTMLToText(string) string for independent use; parser may implement its own HTML extraction to remain independently compilable.
- parser.New() *Registry registers RFCHOST. Registry implements core.ParserRegistry and Register(core.InvoiceParser) error. Registry.Parse returns nil,ErrNoMatch for ordinary mail; invalid matched invoice returns review invoice + ErrReviewRequired when feasible, or error with no invoice when mandatory identity fields are absent. RFCHOST exported NewRFCHOST() core.InvoiceParser.
- assets.New() *Registry implements core.AssetLinker.Reconcile(ctx,repo,invoice) error. Transaction supplied by pipeline; no nested transaction. Only explicit provider/account/hostname or explicit product identity associates an asset. Older invoice must not overwrite newer known service period.
- sync.New(store core.Store,sources map[core.ProviderType]core.MailSource,parsers core.ParserRegistry,linker core.AssetLinker,cfg core.Config,logger *slog.Logger) *Engine implements core.SyncService. Package name syncengine to avoid stdlib collision. Optional nil linker allowed in tests only. Run owns periodic loop; SyncAll parallel bounded per account and account mutex prevents overlap.
- lifecycle.New(store core.Store,allowed []int64) *Service implements core.PaymentConfirmationService. Confirm: pending->paid; Undo: paid->pending; Cancel: pending/review->cancelled. Unauthorized calls have no effects. Same-target replay idempotent; illegal transition ErrConflict.
- reminder.New(store core.Store,cfg core.Config,logger *slog.Logger) *Scheduler implements core.ReminderService. Tick(ctx,now) testable; Run ticks with cancellation. Validates timezone/time on construction via lazy Tick error if no error return. No daily reminder on invoice CreatedAt local day; pending only; transactional unique create.
- storage.Open(ctx context.Context,path string,key []byte) (*Store,error). Store implements core.Store. DB() *sql.DB for diagnostics/tests is allowed. Backup(ctx,destination string) error produces consistent protected snapshot; never copy live WAL db naively. All actual mutations and cursor/outbox writes can run through Transaction callback repository.
- telegram.New(store core.Store,payment core.PaymentConfirmationService,syncer core.SyncService,parsers core.ParserRegistry,onboarding map[core.ProviderType]core.AccountOnboarding,cfg core.Config,client *http.Client,logger *slog.Logger) *Bot. Bot.Run(ctx) error runs polling AND outbox loop; DeliverOnce(ctx) (bool,error) for deterministic integration tests. HTTP timeout exceeds long poll timeout. No other modules import telegram internals. Export FormatEmail(core.NormalizedEmail,core.MailAccount) []string and FormatInvoice(core.Invoice,core.MailAccount) []string for acceptance. Every returned text part <=4096 UTF-16 units and independently valid escaped HTML.
- httpserver/server.New(cfg core.Config,store core.Store,ms *microsoft.Source,qq *qq.Onboarding) *http.Server mounts exact paths + configurable health path. Health only status/no secrets. Request limits/timeouts.
- app.LoadConfig() (core.Config,error); app.Run(ctx context.Context,cfg core.Config,logger *slog.Logger) error. cmd/server handles signals and exits nonzero on invalid config. T08 stage A configuration/deploy/docs first; stage B wiring after all module merges.

## Repository semantics

Repository interfaces can be faked by embedding core.Repository in a fake and overriding called methods. Store.Transaction callback receives a transaction-scoped Repository; returning error rolls back all writes. No external network calls inside SQLite transaction. Get methods return ErrNotFound. IDs are random lowercase 32 hex chars, safe for short callback data. Unknown callbacks fail closed. Stable invoice uniqueness is account/provider/number and preserves paid state. SaveEmail remote identity + conservative cross-folder fingerprint dedup; message-id alone is insufficient, include sender/time/body fingerprint. Message aliases must persist for different folder identities even when deduped.

Enqueue uses DedupKey and never resets a successful delivery. Claim retries failed/unknown and expired sending leases; Complete atomically marks delivery and linked reminder SENT plus Telegram IDs. Fail persists actual known failure vs unknown. Skip persists SKIPPED and prevents further sending. Reminders created together with outbox, status always synchronized. Transition writes event+confirmation metadata atomically (even when called outside Store.Transaction); duplicate transition adds no event.

Session token is random 256-bit bearer; only hash as primary key. Session payload encrypted with purpose-bound AAD; expiration and one-time consumption enforced in SQL transaction. Key length exactly 32 bytes. Credential JSON encrypted per account with AAD and random nonce; never stringify credential in logs/errors. Removal disables account and removes credentials/cursors/sessions associated to account, retaining historical email/invoice/assets. Disabled account is never synced.

Per-part delivery extension (v1 frozen before dispatch): RecordTelegramMessage(ctx,message) persists each acknowledged part immediately; DeliveryMessages(ctx,outboxID) retrieves parts so retries skip known successful sends. CompleteDelivery accepts retained+new part IDs and idempotently UPSERTs. ListTelegramMessages(ctx,invoiceID) retrieves invoice messages for editing. EmailRepository.RecordParseFailure(ctx,emailID,providerID,reason) records failures in same transaction as saved mail; reason must be bounded sanitized diagnostics.

Dynamic settings keys: timezone, reminder_time, sync_interval_seconds. Scheduler reads timezone/reminder_time on each Tick (fallback cfg when ErrNotFound), pipeline Run may read sync interval between passes; Telegram validates before SetSetting. Folder setting syntax /settings folder <accountID> <folderID> on|off uses SaveFolder after locating owned account.

## Approved change CR001 (orchestrator, before integration)

IMAPConfig.TrustedProxyCIDRs []string authorizes exact proxy network peers to assert X-Forwarded-Proto:https for QQ setup. Default empty means Request.TLS is mandatory. Parse RemoteAddr IP and verify CIDR membership; never trust forwarded headers from arbitrary peers. T08 loads TRUSTED_PROXY_CIDRS (comma-separated validated CIDRs), documents isolated Caddy network/subnet and direct HTTP rejection. T02 checks this field; configured PublicBaseURL HTTPS alone never proves transport security. No other public APIs changed.

## Integration clarifications

DeliveryPayload.Kind values are email, invoice, reminder (not worker-specific synonyms). A newly received email referencing an existing invoice is delivered once as invoice with persisted current status; it does not reset payment state. RecordParseFailure arguments are emailID, providerID, bounded reason. InvoiceFilter/AssetFilter Limit<=0 means all matching internal records (reminder and asset reconciliation must not silently cap100); positive limits are bounded for user command listings. EmailFilter remains bounded by default.

## Approved change CR002 — vendor URL policy (Codex integration)

Added optional core.InvoiceURLValidator { ValidateInvoiceURL(Invoice) bool }. No existing interface or constructor signatures change. Merchant parsers own their URL policy; Registry delegates by matching Vendor/ProviderID; Telegram performs generic HTTPS checks and consults the injected registry policy, failing closed without one. RFCHOST enforces exact host/path/single matching invoice id. A future parser can enable its trusted payment links by implementing the optional policy without modifying Telegram or pipeline code.

## Approved change CR003 — confirmed QQ-hosted address aliases

IMAPConfig.AllowedDomains []string and QQ_ALLOWED_DOMAINS permit an explicit operator-maintained address-domain allowlist. Defaults qq.com,foxmail.com. IMAP Source applies it unless a test-only explicit exported override is provided; credentials are still verified against QQ IMAP over TLS before storage. This enables known QQ-hosted aliases without accepting arbitrary IMAP hosts or QQ login passwords. Domain configuration is not proof of access: live authentication must succeed.

## Approved change CR004 — manual recurring reminders

Additive only. `core.ManualReminderRepository` and `core.ManualReminderService` are standalone interfaces; they are deliberately NOT embedded in `Repository`, `Store` or `ReminderService`, so `storage.Store` and every existing implementation keep compiling unchanged. New types (`ManualPlan`, `ManualOccurrence`, `ManualDispatch`, `ManualReminderEvent`, the three filters/input structs), the status/event constants and `core.ManualReminderConfig` (appended to `Config` as `Manual`) are new declarations. `DeliveryPayload` gains two appended fields, `ManualPlanID` and `ManualOccurrenceIDs`; all existing literals are keyed, and an empty `ManualPlanID` keeps the previous mail/invoice rendering path.

Manual deliveries use `DeliveryPayload.Kind: "manual"` and the dedup key `manual-reminder:<planID>:<localDate>`. The dispatch row mirrors the outbox status, so `ClaimDelivery`, `expireLeases`, `CompleteDelivery`, `FailDelivery` and `SkipDelivery` must update the manual dispatch mirror alongside the existing `invoice_reminders` statements without altering them.

Constructors:

- `manual.NewService(repo core.ManualReminderRepository, cfg core.Config, logger *slog.Logger) *Service` implements `core.ManualReminderService`, including `Tick(ctx,now)` and `Run(ctx)`.
- `telegram.NewWithManual(store core.Store, payment core.PaymentConfirmationService, syncer core.SyncService, parsers core.ParserRegistry, onboarding map[core.ProviderType]core.AccountOnboarding, manual core.ManualReminderService, cfg core.Config, client *http.Client, logger *slog.Logger) *Bot` adds the manual service after onboarding.
- `telegram.New` keeps its exact current signature and is a nil-passing wrapper over `NewWithManual`. A nil manual service disables manual commands and the manual rendering branch.

`internal/core` remains frozen after this CR: downstream lanes must not change these declarations, and any further interface need is a new CR.

