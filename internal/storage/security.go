package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"email-invoice-guard/internal/core"
)

// PutSession encrypts the session payload with purpose- and token-bound AAD and
// upserts it by token hash.
func (r *repo) PutSession(ctx context.Context, s core.SecureSession) error {
	if s.TokenHash == "" || s.Purpose == "" {
		return fmt.Errorf("%w: session identity", core.ErrInvalid)
	}
	if s.ExpiresAt.IsZero() {
		return fmt.Errorf("%w: session expiry", core.ErrInvalid)
	}
	blob, err := r.seal(s.Payload, sessionAAD(s.Purpose, s.TokenHash))
	if err != nil {
		return err
	}
	_, err = r.q.ExecContext(ctx, `INSERT INTO oauth_sessions (token_hash, purpose, encrypted_payload, expires_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(token_hash) DO UPDATE SET
			purpose = excluded.purpose,
			encrypted_payload = excluded.encrypted_payload,
			expires_at = excluded.expires_at`,
		s.TokenHash, s.Purpose, blob, fmtTime(s.ExpiresAt))
	return mapErr(err)
}

// GetSession reads and decrypts a session without consuming it. Expired
// sessions and unknown hashes return ErrNotFound; a purpose mismatch returns
// ErrUnauthorized.
func (r *repo) GetSession(ctx context.Context, tokenHash, purpose string, now time.Time) (core.SecureSession, error) {
	var storedPurpose, expiresAt string
	var blob []byte
	err := r.q.QueryRowContext(ctx, `SELECT purpose, encrypted_payload, expires_at FROM oauth_sessions WHERE token_hash = ?`, tokenHash).
		Scan(&storedPurpose, &blob, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return core.SecureSession{}, core.ErrNotFound
	}
	if err != nil {
		return core.SecureSession{}, err
	}
	if storedPurpose != purpose {
		return core.SecureSession{}, core.ErrUnauthorized
	}
	expiry := parseTime(expiresAt)
	if !expiry.After(now) {
		return core.SecureSession{}, core.ErrNotFound
	}
	plain, err := r.openSecret(blob, sessionAAD(storedPurpose, tokenHash))
	if err != nil {
		return core.SecureSession{}, err
	}
	return core.SecureSession{TokenHash: tokenHash, Purpose: storedPurpose, Payload: plain, ExpiresAt: expiry}, nil
}

// ConsumeSession atomically validates and deletes a session. A replay after a
// successful consume returns ErrNotFound.
func (r *repo) ConsumeSession(ctx context.Context, tokenHash, purpose string, now time.Time) (core.SecureSession, error) {
	var out core.SecureSession
	err := r.inTx(ctx, func(t *repo) error {
		var storedPurpose, expiresAt string
		var blob []byte
		err := t.q.QueryRowContext(ctx, `SELECT purpose, encrypted_payload, expires_at FROM oauth_sessions WHERE token_hash = ?`, tokenHash).
			Scan(&storedPurpose, &blob, &expiresAt)
		if errors.Is(err, sql.ErrNoRows) {
			return core.ErrNotFound
		}
		if err != nil {
			return err
		}
		if storedPurpose != purpose {
			return core.ErrUnauthorized
		}
		expiry := parseTime(expiresAt)
		if !expiry.After(now) {
			_, _ = t.q.ExecContext(ctx, `DELETE FROM oauth_sessions WHERE token_hash = ?`, tokenHash)
			return core.ErrNotFound
		}
		res, err := t.q.ExecContext(ctx, `DELETE FROM oauth_sessions WHERE token_hash = ? AND purpose = ? AND expires_at > ?`,
			tokenHash, purpose, fmtTime(now))
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return core.ErrNotFound
		}
		plain, err := t.openSecret(blob, sessionAAD(storedPurpose, tokenHash))
		if err != nil {
			return err
		}
		out = core.SecureSession{TokenHash: tokenHash, Purpose: storedPurpose, Payload: plain, ExpiresAt: expiry}
		return nil
	})
	return out, err
}

// GetSetting returns ErrNotFound when the key is absent so callers can fall
// back to configuration defaults.
func (r *repo) GetSetting(ctx context.Context, key string) (string, error) {
	var value string
	err := r.q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", core.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return value, nil
}

// SetSetting upserts a dynamic setting.
func (r *repo) SetSetting(ctx context.Context, key, value string) error {
	if key == "" {
		return fmt.Errorf("%w: setting key", core.ErrInvalid)
	}
	_, err := r.q.ExecContext(ctx, `INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, fmtTime(time.Now()))
	return mapErr(err)
}

// Status returns aggregate counts for diagnostics without exposing secrets.
func (r *repo) Status(ctx context.Context) (core.SystemStatus, error) {
	var st core.SystemStatus
	err := r.q.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM mail_accounts WHERE enabled = 1),
		(SELECT COUNT(*) FROM emails),
		(SELECT COUNT(*) FROM invoices WHERE status = 'PENDING_CONFIRMATION'),
		(SELECT COUNT(*) FROM delivery_outbox WHERE status IN ('PENDING', 'FAILED', 'UNKNOWN', 'SENDING'))`).
		Scan(&st.Accounts, &st.Emails, &st.PendingInvoices, &st.PendingDeliveries)
	if err != nil {
		return core.SystemStatus{}, err
	}
	return st, nil
}
