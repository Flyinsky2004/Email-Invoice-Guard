-- Schema v2: operator-declared manual recurring reminders (additive only).
-- No v1 table, index or statement is changed. Timestamps are fixed-width UTC
-- text; calendar dates are YYYY-MM-DD; money is signed integer minor units.

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
