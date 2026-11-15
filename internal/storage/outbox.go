package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"email-invoice-guard/internal/core"
)

// Enqueue inserts a delivery unless its dedup key already exists. An existing
// row is never reset, so a successful delivery cannot be re-queued.
func (r *repo) Enqueue(ctx context.Context, d core.Delivery) (bool, error) {
	var inserted bool
	err := r.inTx(ctx, func(t *repo) error {
		_, ins, _, err := t.enqueue(ctx, d)
		inserted = ins
		return err
	})
	return inserted, err
}

// enqueue is the shared insert-or-reuse helper. It returns the outbox ID, the
// inserted flag and the effective status.
func (r *repo) enqueue(ctx context.Context, d core.Delivery) (string, bool, core.DeliveryStatus, error) {
	if d.DedupKey == "" {
		return "", false, "", fmt.Errorf("%w: delivery dedup key", core.ErrInvalid)
	}
	if d.Status == "" {
		d.Status = core.DeliveryPending
	}
	if !validDeliveryStatus(d.Status) {
		return "", false, "", fmt.Errorf("%w: delivery status", core.ErrInvalid)
	}
	now := time.Now().UTC()
	if d.ID == "" {
		d.ID = core.NewID()
	}
	if d.CreatedAt.IsZero() {
		d.CreatedAt = now
	}
	if d.AvailableAt.IsZero() {
		d.AvailableAt = d.CreatedAt
	}
	payload, err := marshalJSON(d.Payload)
	if err != nil {
		return "", false, "", err
	}
	res, err := r.q.ExecContext(ctx, `INSERT INTO delivery_outbox
		(id, dedup_key, payload_json, status, attempts, available_at, lease_until, last_error, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(dedup_key) DO NOTHING`,
		d.ID, d.DedupKey, payload, string(d.Status), d.Attempts, fmtTime(d.AvailableAt),
		nullTimePtr(d.LeaseUntil), d.LastError, fmtTime(d.CreatedAt))
	if err != nil {
		return "", false, "", mapErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return "", false, "", err
	}
	if n == 1 {
		return d.ID, true, d.Status, nil
	}
	var existingID, existingStatus string
	if err := r.q.QueryRowContext(ctx, `SELECT id, status FROM delivery_outbox WHERE dedup_key = ?`, d.DedupKey).
		Scan(&existingID, &existingStatus); err != nil {
		return "", false, "", err
	}
	return existingID, false, core.DeliveryStatus(existingStatus), nil
}

// ClaimDelivery atomically leases one due delivery. Expired SENDING leases are
// first demoted to UNKNOWN so an ambiguous send is retried at-least-once.
func (r *repo) ClaimDelivery(ctx context.Context, now time.Time, lease time.Duration) (*core.Delivery, error) {
	if lease <= 0 {
		lease = time.Minute
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	var out *core.Delivery
	err := r.inTx(ctx, func(t *repo) error {
		if err := t.expireLeases(ctx, now); err != nil {
			return err
		}
		for attempt := 0; attempt < 8; attempt++ {
			var d core.Delivery
			var payload, status, availableAt, createdAt string
			var leaseUntil, lastError sql.NullString
			var attempts int
			err := t.q.QueryRowContext(ctx, `SELECT id, dedup_key, payload_json, status, attempts, available_at, lease_until, last_error, created_at
				FROM delivery_outbox
				WHERE status IN ('PENDING', 'FAILED', 'UNKNOWN') AND available_at <= ?
				ORDER BY available_at, created_at LIMIT 1`, fmtTime(now)).
				Scan(&d.ID, &d.DedupKey, &payload, &status, &attempts, &availableAt, &leaseUntil, &lastError, &createdAt)
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			leaseUntilTime := now.Add(lease)
			res, err := t.q.ExecContext(ctx, `UPDATE delivery_outbox
				SET status = 'SENDING', attempts = attempts + 1, lease_until = ?, last_error = ''
				WHERE id = ? AND status IN ('PENDING', 'FAILED', 'UNKNOWN')`,
				fmtTime(leaseUntilTime), d.ID)
			if err != nil {
				return mapErr(err)
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			if n != 1 {
				continue // another claimant won; try the next row
			}
			if err := json.Unmarshal([]byte(payload), &d.Payload); err != nil {
				return fmt.Errorf("decode delivery payload: %w", err)
			}
			d.Status = core.DeliverySending
			d.Attempts = attempts + 1
			d.AvailableAt = parseTime(availableAt)
			d.LeaseUntil = &leaseUntilTime
			d.LastError = ""
			d.CreatedAt = parseTime(createdAt)
			if _, err := t.q.ExecContext(ctx, `UPDATE invoice_reminders SET status = 'SENDING' WHERE outbox_id = ?`, d.ID); err != nil {
				return mapErr(err)
			}
			out = &d
			return nil
		}
		return nil
	})
	return out, err
}

func (r *repo) expireLeases(ctx context.Context, now time.Time) error {
	rows, err := r.q.QueryContext(ctx, `SELECT id FROM delivery_outbox
		WHERE status = 'SENDING' AND lease_until IS NOT NULL AND lease_until <= ?`, fmtTime(now))
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, id := range ids {
		if _, err := r.q.ExecContext(ctx, `UPDATE delivery_outbox
			SET status = 'UNKNOWN', lease_until = NULL, last_error = 'lease expired'
			WHERE id = ? AND status = 'SENDING'`, id); err != nil {
			return mapErr(err)
		}
		if _, err := r.q.ExecContext(ctx, `UPDATE invoice_reminders SET status = 'UNKNOWN' WHERE outbox_id = ?`, id); err != nil {
			return mapErr(err)
		}
	}
	return nil
}

// CompleteDelivery atomically upserts acknowledged parts and marks the outbox
// and linked reminder SENT.
func (r *repo) CompleteDelivery(ctx context.Context, outboxID string, messages []core.TelegramMessage, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	} else {
		at = at.UTC()
	}
	return r.inTx(ctx, func(t *repo) error {
		for _, m := range messages {
			if m.ID == "" {
				m.ID = core.NewID()
			}
			m.OutboxID = outboxID
			if m.CreatedAt.IsZero() {
				m.CreatedAt = at
			}
			if _, err := t.q.ExecContext(ctx, `INSERT INTO telegram_messages
				(id, outbox_id, invoice_id, chat_id, message_id, part, created_at)
				VALUES (?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(outbox_id, part) DO UPDATE SET
					invoice_id = excluded.invoice_id,
					chat_id = excluded.chat_id,
					message_id = excluded.message_id`,
				m.ID, m.OutboxID, nullString(m.InvoiceID), m.ChatID, m.MessageID, m.Part, fmtTime(m.CreatedAt)); err != nil {
				return mapErr(err)
			}
		}
		res, err := t.q.ExecContext(ctx, `UPDATE delivery_outbox
			SET status = 'SENT', lease_until = NULL, last_error = ''
			WHERE id = ?`, outboxID)
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return core.ErrNotFound
		}
		if _, err := t.q.ExecContext(ctx, `UPDATE invoice_reminders SET status = 'SENT', sent_at = ? WHERE outbox_id = ?`,
			fmtTime(at), outboxID); err != nil {
			return mapErr(err)
		}
		return nil
	})
}

// FailDelivery records a bounded failure. unknown distinguishes an ambiguous
// send (retryable UNKNOWN) from a known FAILED result.
func (r *repo) FailDelivery(ctx context.Context, outboxID string, at time.Time, unknown bool, code string) error {
	if at.IsZero() {
		at = time.Now().UTC()
	} else {
		at = at.UTC()
	}
	status := core.DeliveryFailed
	if unknown {
		status = core.DeliveryUnknown
	}
	code = sanitizeDiagnostic(code, 512)
	return r.inTx(ctx, func(t *repo) error {
		res, err := t.q.ExecContext(ctx, `UPDATE delivery_outbox
			SET status = ?, lease_until = NULL, last_error = ?, available_at = ?
			WHERE id = ? AND status = 'SENDING'`, string(status), code, fmtTime(at), outboxID)
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			var exists int
			err := t.q.QueryRowContext(ctx, `SELECT 1 FROM delivery_outbox WHERE id = ?`, outboxID).Scan(&exists)
			if errors.Is(err, sql.ErrNoRows) {
				return core.ErrNotFound
			}
			if err != nil {
				return err
			}
			return nil // stale: the sender should recheck status
		}
		if _, err := t.q.ExecContext(ctx, `UPDATE invoice_reminders SET status = ? WHERE outbox_id = ?`, string(status), outboxID); err != nil {
			return mapErr(err)
		}
		return nil
	})
}

// SkipDelivery permanently skips a delivery and synchronizes the reminder.
func (r *repo) SkipDelivery(ctx context.Context, outboxID string, at time.Time) error {
	return r.inTx(ctx, func(t *repo) error {
		res, err := t.q.ExecContext(ctx, `UPDATE delivery_outbox
			SET status = 'SKIPPED', lease_until = NULL
			WHERE id = ? AND status <> 'SENT'`, outboxID)
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			var exists int
			err := t.q.QueryRowContext(ctx, `SELECT 1 FROM delivery_outbox WHERE id = ?`, outboxID).Scan(&exists)
			if errors.Is(err, sql.ErrNoRows) {
				return core.ErrNotFound
			}
			if err != nil {
				return err
			}
			return nil
		}
		if _, err := t.q.ExecContext(ctx, `UPDATE invoice_reminders SET status = 'SKIPPED' WHERE outbox_id = ?`, outboxID); err != nil {
			return mapErr(err)
		}
		return nil
	})
}

// RecordTelegramMessage persists one acknowledged part immediately so a retry
// can skip it.
func (r *repo) RecordTelegramMessage(ctx context.Context, m core.TelegramMessage) error {
	if m.OutboxID == "" {
		return fmt.Errorf("%w: telegram outbox id", core.ErrInvalid)
	}
	if m.ID == "" {
		m.ID = core.NewID()
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	_, err := r.q.ExecContext(ctx, `INSERT INTO telegram_messages
		(id, outbox_id, invoice_id, chat_id, message_id, part, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(outbox_id, part) DO UPDATE SET
			invoice_id = excluded.invoice_id,
			chat_id = excluded.chat_id,
			message_id = excluded.message_id`,
		m.ID, m.OutboxID, nullString(m.InvoiceID), m.ChatID, m.MessageID, m.Part, fmtTime(m.CreatedAt))
	return mapErr(err)
}

// DeliveryMessages returns the parts already recorded for an outbox row.
func (r *repo) DeliveryMessages(ctx context.Context, outboxID string) ([]core.TelegramMessage, error) {
	return r.telegramMessages(ctx, `outbox_id = ?`, outboxID)
}

// ListTelegramMessages returns every message recorded for an invoice.
func (r *repo) ListTelegramMessages(ctx context.Context, invoiceID string) ([]core.TelegramMessage, error) {
	return r.telegramMessages(ctx, `invoice_id = ?`, invoiceID)
}

func (r *repo) telegramMessages(ctx context.Context, where string, arg any) ([]core.TelegramMessage, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT id, outbox_id, COALESCE(invoice_id, ''), chat_id, message_id, part, created_at
		FROM telegram_messages WHERE `+where+` ORDER BY part, id`, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.TelegramMessage
	for rows.Next() {
		var m core.TelegramMessage
		var createdAt string
		if err := rows.Scan(&m.ID, &m.OutboxID, &m.InvoiceID, &m.ChatID, &m.MessageID, &m.Part, &createdAt); err != nil {
			return nil, err
		}
		m.CreatedAt = parseTime(createdAt)
		out = append(out, m)
	}
	return out, rows.Err()
}

func validDeliveryStatus(s core.DeliveryStatus) bool {
	switch s {
	case core.DeliveryPending, core.DeliverySending, core.DeliverySent, core.DeliveryFailed, core.DeliveryUnknown, core.DeliverySkipped:
		return true
	}
	return false
}
