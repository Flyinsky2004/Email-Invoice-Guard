-- Schema v1 for email-invoice-guard. Applied atomically by the storage package.
-- Timestamps are fixed-width UTC RFC3339Nano text; calendar dates are YYYY-MM-DD.
-- Money is signed integer minor units; booleans are 0/1.

CREATE TABLE IF NOT EXISTS mail_accounts (
    id            TEXT PRIMARY KEY,
    provider      TEXT NOT NULL CHECK (provider IN ('microsoft', 'qq')),
    address       TEXT NOT NULL,
    owner_user_id INTEGER NOT NULL,
    enabled       INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    last_error    TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    UNIQUE (provider, address)
);

CREATE TABLE IF NOT EXISTS mail_credentials (
    account_id TEXT PRIMARY KEY REFERENCES mail_accounts(id) ON DELETE CASCADE,
    encrypted  BLOB NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS mail_folders (
    account_id TEXT NOT NULL REFERENCES mail_accounts(id),
    id         TEXT NOT NULL,
    name       TEXT NOT NULL DEFAULT '',
    role       TEXT NOT NULL DEFAULT '',
    enabled    INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
    PRIMARY KEY (account_id, id)
);

CREATE TABLE IF NOT EXISTS sync_cursors (
    account_id        TEXT NOT NULL,
    folder_id         TEXT NOT NULL,
    value             TEXT NOT NULL DEFAULT '',
    uid_validity      INTEGER NOT NULL DEFAULT 0,
    last_uid          INTEGER NOT NULL DEFAULT 0,
    baseline_at       TEXT NOT NULL DEFAULT '',
    baseline_complete INTEGER NOT NULL DEFAULT 0 CHECK (baseline_complete IN (0, 1)),
    updated_at        TEXT NOT NULL,
    PRIMARY KEY (account_id, folder_id),
    FOREIGN KEY (account_id, folder_id) REFERENCES mail_folders(account_id, id)
);

CREATE TABLE IF NOT EXISTS emails (
    id                  TEXT PRIMARY KEY,
    account_id          TEXT NOT NULL REFERENCES mail_accounts(id),
    provider            TEXT NOT NULL,
    provider_message_id TEXT NOT NULL DEFAULT '',
    folder_id           TEXT NOT NULL DEFAULT '',
    internet_message_id TEXT NOT NULL DEFAULT '',
    fingerprint         TEXT NOT NULL DEFAULT '',
    received_at         TEXT NOT NULL,
    subject             TEXT NOT NULL DEFAULT '',
    text_body           TEXT NOT NULL DEFAULT '',
    html_body           TEXT NOT NULL DEFAULT '',
    classification      TEXT NOT NULL DEFAULT '',
    normalized_json     TEXT NOT NULL,
    created_at          TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_emails_account_received ON emails(account_id, received_at);
CREATE INDEX IF NOT EXISTS idx_emails_fingerprint ON emails(account_id, fingerprint);
CREATE INDEX IF NOT EXISTS idx_emails_message_id ON emails(account_id, internet_message_id);

CREATE TABLE IF NOT EXISTS email_identities (
    account_id      TEXT NOT NULL REFERENCES mail_accounts(id),
    remote_identity TEXT NOT NULL,
    email_id        TEXT NOT NULL REFERENCES emails(id),
    UNIQUE (account_id, remote_identity)
);
CREATE INDEX IF NOT EXISTS idx_email_identities_email ON email_identities(email_id);

CREATE TABLE IF NOT EXISTS email_attachments (
    id           TEXT PRIMARY KEY,
    email_id     TEXT NOT NULL REFERENCES emails(id),
    filename     TEXT NOT NULL DEFAULT '',
    content_type TEXT NOT NULL DEFAULT '',
    size         INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_email_attachments_email ON email_attachments(email_id);

CREATE TABLE IF NOT EXISTS email_parse_failures (
    id          TEXT PRIMARY KEY,
    email_id    TEXT NOT NULL REFERENCES emails(id),
    provider_id TEXT NOT NULL DEFAULT '',
    reason      TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_email_parse_failures_email ON email_parse_failures(email_id);

CREATE TABLE IF NOT EXISTS providers (
    id   TEXT PRIMARY KEY,
    name TEXT NOT NULL
);
INSERT OR IGNORE INTO providers (id, name) VALUES ('rfchost', 'RFCHOST');

CREATE TABLE IF NOT EXISTS provider_rules (
    id          TEXT PRIMARY KEY,
    provider_id TEXT NOT NULL REFERENCES providers(id),
    version     TEXT NOT NULL,
    rule_json   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS invoices (
    id              TEXT PRIMARY KEY,
    account_id      TEXT NOT NULL REFERENCES mail_accounts(id),
    provider_id     TEXT NOT NULL REFERENCES providers(id),
    number          TEXT NOT NULL,
    source_email_id TEXT REFERENCES emails(id),
    status          TEXT NOT NULL CHECK (status IN ('PENDING_CONFIRMATION', 'PAID_CONFIRMED', 'REVIEW_REQUIRED', 'CANCELLED')),
    currency        TEXT NOT NULL DEFAULT '',
    amount_due      INTEGER NOT NULL DEFAULT 0,
    due_date        TEXT,
    created_date    TEXT,
    subtotal        INTEGER,
    credit          INTEGER,
    total           INTEGER,
    account_balance INTEGER,
    parser_version  TEXT NOT NULL DEFAULT '',
    url             TEXT NOT NULL DEFAULT '',
    invoice_json    TEXT NOT NULL,
    confirmed_by    INTEGER,
    confirmed_at    TEXT,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    UNIQUE (account_id, provider_id, number)
);
CREATE INDEX IF NOT EXISTS idx_invoices_status_due ON invoices(status, due_date);
CREATE INDEX IF NOT EXISTS idx_invoices_account ON invoices(account_id);

CREATE TABLE IF NOT EXISTS invoice_email_links (
    invoice_id TEXT NOT NULL REFERENCES invoices(id),
    email_id   TEXT NOT NULL REFERENCES emails(id),
    PRIMARY KEY (invoice_id, email_id)
);

CREATE TABLE IF NOT EXISTS invoice_items (
    id         TEXT PRIMARY KEY,
    invoice_id TEXT NOT NULL REFERENCES invoices(id),
    item_json  TEXT NOT NULL,
    hostname   TEXT,
    product_id TEXT
);
CREATE INDEX IF NOT EXISTS idx_invoice_items_invoice ON invoice_items(invoice_id);

CREATE TABLE IF NOT EXISTS invoice_events (
    id            TEXT PRIMARY KEY,
    invoice_id    TEXT NOT NULL REFERENCES invoices(id),
    action        TEXT NOT NULL,
    actor_user_id INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_invoice_events_invoice ON invoice_events(invoice_id, created_at);

CREATE TABLE IF NOT EXISTS delivery_outbox (
    id           TEXT PRIMARY KEY,
    dedup_key    TEXT NOT NULL UNIQUE,
    payload_json TEXT NOT NULL,
    status       TEXT NOT NULL CHECK (status IN ('PENDING', 'SENDING', 'SENT', 'FAILED', 'UNKNOWN', 'SKIPPED')),
    attempts     INTEGER NOT NULL DEFAULT 0,
    available_at TEXT NOT NULL,
    lease_until  TEXT,
    last_error   TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_delivery_outbox_due ON delivery_outbox(status, available_at);

CREATE TABLE IF NOT EXISTS invoice_reminders (
    id            TEXT PRIMARY KEY,
    invoice_id    TEXT NOT NULL REFERENCES invoices(id),
    reminder_date TEXT NOT NULL,
    status        TEXT NOT NULL CHECK (status IN ('PENDING', 'SENDING', 'SENT', 'FAILED', 'UNKNOWN', 'SKIPPED')),
    outbox_id     TEXT NOT NULL REFERENCES delivery_outbox(id),
    created_at    TEXT NOT NULL,
    sent_at       TEXT,
    UNIQUE (invoice_id, reminder_date)
);
CREATE INDEX IF NOT EXISTS idx_invoice_reminders_outbox ON invoice_reminders(outbox_id);

CREATE TABLE IF NOT EXISTS assets (
    id              TEXT PRIMARY KEY,
    provider_id     TEXT NOT NULL REFERENCES providers(id),
    account_id      TEXT NOT NULL REFERENCES mail_accounts(id),
    hostname        TEXT,
    product_id      TEXT,
    asset_json      TEXT NOT NULL,
    last_invoice_id TEXT REFERENCES invoices(id),
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_assets_hostname ON assets(provider_id, account_id, hostname) WHERE hostname IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS ux_assets_product ON assets(provider_id, account_id, product_id) WHERE product_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS asset_invoice_links (
    asset_id   TEXT NOT NULL REFERENCES assets(id),
    invoice_id TEXT NOT NULL REFERENCES invoices(id),
    PRIMARY KEY (asset_id, invoice_id)
);

CREATE TABLE IF NOT EXISTS telegram_messages (
    id         TEXT PRIMARY KEY,
    outbox_id  TEXT NOT NULL REFERENCES delivery_outbox(id),
    invoice_id TEXT REFERENCES invoices(id),
    chat_id    INTEGER NOT NULL,
    message_id INTEGER NOT NULL,
    part       INTEGER NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE (outbox_id, part),
    UNIQUE (chat_id, message_id)
);
CREATE INDEX IF NOT EXISTS idx_telegram_messages_invoice ON telegram_messages(invoice_id);

CREATE TABLE IF NOT EXISTS oauth_sessions (
    token_hash       TEXT PRIMARY KEY,
    purpose          TEXT NOT NULL,
    encrypted_payload BLOB NOT NULL,
    expires_at       TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_oauth_sessions_expires ON oauth_sessions(expires_at);

CREATE TABLE IF NOT EXISTS settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
