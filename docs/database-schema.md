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
