# Database schema contract v1

Storage worker owns SQL migrations under internal/storage/migrations. Apply ordered embedded migrations atomically and maintain schema_migrations(version primary key, applied_at). Open every connection with foreign_keys=ON, busy_timeout=5000; WAL on database. Bound pool prevents connection PRAGMA drift. SQLite transactions serialize writes; no network inside transaction.

Timestamp representation: UTC RFC3339Nano or Unix nanoseconds consistently; calendar dates TEXT YYYY-MM-DD. IDs TEXT primary keys. Boolean CHECK 0/1. Money INTEGER, never REAL. JSON for structured domain payloads may supplement indexed scalar columns; must not replace relational identity/state constraints. Secrets AES-256-GCM encrypted BLOB including version+nonce; domain/account/purpose as AAD. Key never stored in DB.

| Table | Required scalar columns and constraints |
|---|---|
| mail_accounts | id PK, provider CHECK microsoft/qq, address, owner_user_id, enabled, last_error, created_at, updated_at, UNIQUE(provider,address) |
| mail_credentials | account_id PK FK mail_accounts, encrypted BLOB, updated_at |
| mail_folders | account_id FK, id, name, role, enabled, composite PK(account_id,id) |
| sync_cursors | account_id,folder_id composite PK/FK mail_folders, value,uid_validity,last_uid,baseline_at,baseline_complete,updated_at |
| emails | id PK,account_id FK,provider,provider_message_id,folder_id,internet_message_id,fingerprint,received_at,subject,text_body,html_body,classification,normalized_json,created_at; INDEX(account_id,received_at), search indexes as appropriate |
| email_identities | account_id,remote_identity composite UNIQUE; email_id FK; MS remote immutable id; IMAP folder+uidvalidity+uid; alias retained even when canonical email exists |
| email_attachments | id PK,email_id FK,filename,content_type,size; no auto-downloaded bytes |
| email_parse_failures | id PK,email_id FK,provider_id,reason,created_at; no raw credential data |
| providers | id PK,name; RFCHOST seed only; no fake implemented parsers |
| provider_rules | id PK,provider_id FK,version,rule_json |
| invoices | id PK,account_id FK,provider_id FK,number NOT NULL,source_email_id FK,status CHECK domain enum,currency,amount_due INTEGER,due_date,created_date nullable,subtotal/credit/total/account_balance nullable INTEGER,parser_version,url,invoice_json,confirmed_by nullable,confirmed_at nullable,created_at,updated_at, UNIQUE(account_id,provider_id,number); INDEX(status,due_date) |
| invoice_email_links | invoice_id,email_id FK, composite PK |
| invoice_items | id PK,invoice_id FK,item_json plus indexed hostname/product identifier if needed |
| invoice_events | id PK,invoice_id FK,action,actor_user_id,created_at |
| invoice_reminders | id PK,invoice_id FK,reminder_date,status CHECK outbox enum,outbox_id FK delivery_outbox,created_at,sent_at nullable, UNIQUE(invoice_id,reminder_date) |
| assets | id PK,provider_id FK,account_id FK,hostname nullable,product_id nullable,asset_json,last_invoice_id nullable FK,created_at,updated_at; conservative unique explicit identity per account/provider, nullable unknown identity stays unassociated |
| asset_invoice_links | asset_id,invoice_id FK, composite PK |
| delivery_outbox | id PK,dedup_key UNIQUE,payload_json,status CHECK enum,attempts,available_at,lease_until nullable,last_error,created_at; INDEX(status,available_at) |
| telegram_messages | id PK,outbox_id FK,invoice_id nullable FK,chat_id,message_id,part,created_at, UNIQUE(outbox_id,part), UNIQUE(chat_id,message_id) |
| oauth_sessions | token_hash PK,purpose,encrypted_payload,expires_at; expiration index; one-time atomic consume/delete |
| settings | key PK,value,updated_at |

Foreign keys reference retained historical account rows. RemoveAccount soft-disables, erases encrypted credentials and live cursors; it must not cascade-delete historical invoices. Emails and parse failure diagnostics survive parser errors. Persist diagnostic reasons via review invoice and/or email normalized metadata/failure row. Migration tests cover empty DB, idempotent reopen, rollback, unique/FK violation, concurrency, encrypted blobs, backup/restore, and WAL recovery.

## Manual recurring reminders (v2, additive)

Migration `0002_manual_reminders.sql` adds five tables. It is additive: no v1 table, index or statement changes. Conventions match v1 — timestamps fixed-width UTC `2006-01-02T15:04:05.000000000Z`, calendar dates `YYYY-MM-DD`, money integer minor units, IDs 32 lowercase hex, booleans 0/1.

```sql
CREATE TABLE IF NOT EXISTS manual_reminder_plans (
    id TEXT PRIMARY KEY, title TEXT NOT NULL, provider_name TEXT NOT NULL DEFAULT '',
    asset_id TEXT REFERENCES assets(id), description TEXT NOT NULL DEFAULT '',
    amount_minor INTEGER, currency TEXT,
    billing_day INTEGER NOT NULL CHECK (billing_day BETWEEN 1 AND 31),
    reminder_time TEXT NOT NULL, timezone TEXT NOT NULL,
    payment_url TEXT NOT NULL DEFAULT '', notes TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL CHECK (status IN ('ACTIVE','PAUSED','CANCELLED')),
    next_due_date TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
    cancelled_at TEXT,
    CHECK ((amount_minor IS NULL AND currency IS NULL)
        OR (amount_minor IS NOT NULL AND currency IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS idx_manual_plans_status_due
    ON manual_reminder_plans(status, next_due_date);

CREATE TABLE IF NOT EXISTS manual_reminder_occurrences (
    id TEXT PRIMARY KEY, plan_id TEXT NOT NULL REFERENCES manual_reminder_plans(id),
    due_date TEXT NOT NULL, amount_minor_snapshot INTEGER, currency_snapshot TEXT,
    payment_status TEXT NOT NULL CHECK (payment_status IN
        ('PENDING_CONFIRMATION','PAID_CONFIRMED','CANCELLED')),
    notified_at TEXT, confirmed_at TEXT, confirmed_by INTEGER,
    created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
    UNIQUE (plan_id, due_date)
);
CREATE INDEX IF NOT EXISTS idx_manual_occ_status
    ON manual_reminder_occurrences(payment_status, due_date);

CREATE TABLE IF NOT EXISTS manual_reminder_dispatches (
    id TEXT PRIMARY KEY, plan_id TEXT NOT NULL REFERENCES manual_reminder_plans(id),
    outbox_id TEXT NOT NULL REFERENCES delivery_outbox(id), local_date TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('DAILY_SUMMARY')),
    status TEXT NOT NULL CHECK (status IN
        ('PENDING','SENDING','SENT','FAILED','UNKNOWN','SKIPPED')),
    created_at TEXT NOT NULL, sent_at TEXT,
    UNIQUE (outbox_id), UNIQUE (plan_id, local_date, kind)
);
CREATE INDEX IF NOT EXISTS idx_manual_dispatches_plan
    ON manual_reminder_dispatches(plan_id, local_date);

CREATE TABLE IF NOT EXISTS manual_reminder_dispatch_occurrences (
    dispatch_id TEXT NOT NULL REFERENCES manual_reminder_dispatches(id),
    occurrence_id TEXT NOT NULL REFERENCES manual_reminder_occurrences(id),
    PRIMARY KEY (dispatch_id, occurrence_id)
);
CREATE INDEX IF NOT EXISTS idx_manual_dispatch_occ_occurrence
    ON manual_reminder_dispatch_occurrences(occurrence_id);

CREATE TABLE IF NOT EXISTS manual_reminder_events (
    id TEXT PRIMARY KEY, plan_id TEXT NOT NULL REFERENCES manual_reminder_plans(id),
    occurrence_id TEXT REFERENCES manual_reminder_occurrences(id),
    event_type TEXT NOT NULL CHECK (event_type IN (
        'PLAN_CREATED','PLAN_UPDATED','PLAN_PAUSED','PLAN_RESUMED','PLAN_CANCELLED',
        'OCCURRENCE_CREATED','REMINDER_SENT','PAYMENT_CONFIRMED',
        'PAYMENT_CONFIRMATION_REVOKED','OCCURRENCE_CANCELLED')),
    actor_id INTEGER NOT NULL DEFAULT 0, event_at TEXT NOT NULL,
    metadata TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS idx_manual_events_plan
    ON manual_reminder_events(plan_id, event_at);
```

Outbox mirror rule: `manual_reminder_dispatches.status` mirrors the linked `delivery_outbox` row. The existing claim, lease-expiry, complete, fail and skip operations must update the manual dispatch mirror in the same transaction as the outbox row, exactly as they already synchronize `invoice_reminders`. The `invoice_reminders` statements are not modified; `UNIQUE(outbox_id)` makes the mirror update a single-row, idempotent write. `sent_at` is set only when the outbox reaches SENT.

