package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"email-invoice-guard/internal/core"
)

const invoiceColumns = `id, account_id, provider_id, number, source_email_id, status, currency, amount_due,
	due_date, created_date, subtotal, credit, total, account_balance, parser_version, url,
	invoice_json, confirmed_by, confirmed_at, created_at, updated_at`

type rowScanner interface {
	Scan(...any) error
}

// SaveInvoice inserts an invoice with its items, initial event and source-mail
// link. A duplicate business key (account, provider, number) preserves the
// existing invoice state and only records the additional source link.
func (r *repo) SaveInvoice(ctx context.Context, inv core.Invoice) (core.Invoice, bool, error) {
	var out core.Invoice
	var created bool
	err := r.inTx(ctx, func(t *repo) error {
		var err error
		out, created, err = t.saveInvoice(ctx, inv)
		return err
	})
	return out, created, err
}

func (r *repo) saveInvoice(ctx context.Context, inv core.Invoice) (core.Invoice, bool, error) {
	if inv.AccountID == "" || inv.ProviderID == "" {
		return core.Invoice{}, false, fmt.Errorf("%w: invoice account/provider", core.ErrInvalid)
	}
	if strings.TrimSpace(inv.Number) == "" {
		return core.Invoice{}, false, fmt.Errorf("%w: invoice number", core.ErrInvalid)
	}
	if inv.Status == "" {
		inv.Status = core.PendingConfirmation
	}
	if !validInvoiceStatus(inv.Status) {
		return core.Invoice{}, false, fmt.Errorf("%w: invoice status", core.ErrInvalid)
	}
	if inv.DueDate != "" {
		due, err := core.ParseDate(string(inv.DueDate))
		if err != nil {
			return core.Invoice{}, false, err
		}
		inv.DueDate = due
	}
	if inv.CreatedDate != nil {
		created, err := core.ParseDate(string(*inv.CreatedDate))
		if err != nil {
			return core.Invoice{}, false, err
		}
		inv.CreatedDate = &created
	}

	var existingID string
	err := r.q.QueryRowContext(ctx, `SELECT id FROM invoices WHERE account_id = ? AND provider_id = ? AND number = ?`,
		inv.AccountID, inv.ProviderID, inv.Number).Scan(&existingID)
	if err == nil {
		if err := r.linkInvoiceEmail(ctx, existingID, inv.SourceEmailID); err != nil {
			return core.Invoice{}, false, err
		}
		existing, err := r.getInvoice(ctx, existingID)
		if err != nil {
			return core.Invoice{}, false, err
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return core.Invoice{}, false, err
	}

	now := time.Now().UTC()
	if inv.ID == "" {
		inv.ID = core.NewID()
	}
	if inv.CreatedAt.IsZero() {
		inv.CreatedAt = now
	}
	inv.UpdatedAt = now
	for i := range inv.Items {
		if inv.Items[i].ID == "" {
			inv.Items[i].ID = core.NewID()
		}
		inv.Items[i].InvoiceID = inv.ID
	}
	js, err := marshalJSON(inv)
	if err != nil {
		return core.Invoice{}, false, err
	}
	var due any
	if inv.DueDate != "" {
		due = string(inv.DueDate)
	}
	_, err = r.q.ExecContext(ctx, `INSERT INTO invoices
		(id, account_id, provider_id, number, source_email_id, status, currency, amount_due,
		 due_date, created_date, subtotal, credit, total, account_balance, parser_version, url,
		 invoice_json, confirmed_by, confirmed_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		inv.ID, inv.AccountID, inv.ProviderID, inv.Number, nullString(inv.SourceEmailID), string(inv.Status),
		inv.Currency, inv.AmountDue.Minor, due, nullDatePtr(inv.CreatedDate), nullMoneyPtr(inv.SubTotal),
		nullMoneyPtr(inv.Credit), nullMoneyPtr(inv.Total), nullMoneyPtr(inv.AccountBalance),
		inv.ParserVersion, inv.URL, js, nullInt64Ptr(inv.ConfirmedBy), nullTimePtr(inv.ConfirmedAt),
		fmtTime(inv.CreatedAt), fmtTime(inv.UpdatedAt))
	if err != nil {
		return core.Invoice{}, false, mapErr(err)
	}
	for _, item := range inv.Items {
		itemJSON, err := marshalJSON(item)
		if err != nil {
			return core.Invoice{}, false, err
		}
		if _, err := r.q.ExecContext(ctx, `INSERT INTO invoice_items (id, invoice_id, item_json, hostname, product_id) VALUES (?, ?, ?, ?, ?)`,
			item.ID, inv.ID, itemJSON, nullStringPtr(item.Hostname), nullStringPtr(item.ProductID)); err != nil {
			return core.Invoice{}, false, mapErr(err)
		}
	}
	if err := r.insertInvoiceEvent(ctx, inv.ID, "CREATED", 0, now); err != nil {
		return core.Invoice{}, false, err
	}
	if err := r.linkInvoiceEmail(ctx, inv.ID, inv.SourceEmailID); err != nil {
		return core.Invoice{}, false, err
	}
	return inv, true, nil
}

func (r *repo) linkInvoiceEmail(ctx context.Context, invoiceID, emailID string) error {
	if emailID == "" {
		return nil
	}
	_, err := r.q.ExecContext(ctx, `INSERT OR IGNORE INTO invoice_email_links (invoice_id, email_id) VALUES (?, ?)`, invoiceID, emailID)
	return mapErr(err)
}

func (r *repo) insertInvoiceEvent(ctx context.Context, invoiceID, action string, actor int64, at time.Time) error {
	_, err := r.q.ExecContext(ctx, `INSERT INTO invoice_events (id, invoice_id, action, actor_user_id, created_at) VALUES (?, ?, ?, ?, ?)`,
		core.NewID(), invoiceID, action, actor, fmtTime(at))
	return mapErr(err)
}

// GetInvoice returns the invoice with items loaded from their normalized rows.
func (r *repo) GetInvoice(ctx context.Context, id string) (core.Invoice, error) {
	return r.getInvoice(ctx, id)
}

func (r *repo) getInvoice(ctx context.Context, id string) (core.Invoice, error) {
	row := r.q.QueryRowContext(ctx, `SELECT `+invoiceColumns+` FROM invoices WHERE id = ?`, id)
	inv, err := scanInvoice(row)
	if errors.Is(err, sql.ErrNoRows) {
		return core.Invoice{}, core.ErrNotFound
	}
	if err != nil {
		return core.Invoice{}, err
	}
	items, err := r.invoiceItems(ctx, id)
	if err != nil {
		return core.Invoice{}, err
	}
	inv.Items = items
	return inv, nil
}

func scanInvoice(sc rowScanner) (core.Invoice, error) {
	var inv core.Invoice
	var sourceEmail, dueDate, createdDate, confirmedAt sql.NullString
	var subtotal, credit, total, balance, confirmedBy sql.NullInt64
	var status, currency, parserVersion, url, js, createdAt, updatedAt string
	var amountDue int64
	if err := sc.Scan(&inv.ID, &inv.AccountID, &inv.ProviderID, &inv.Number, &sourceEmail, &status, &currency,
		&amountDue, &dueDate, &createdDate, &subtotal, &credit, &total, &balance, &parserVersion, &url,
		&js, &confirmedBy, &confirmedAt, &createdAt, &updatedAt); err != nil {
		return core.Invoice{}, err
	}
	if err := json.Unmarshal([]byte(js), &inv); err != nil {
		return core.Invoice{}, fmt.Errorf("decode invoice: %w", err)
	}
	// Authoritative scalar state always wins over the JSON snapshot.
	inv.SourceEmailID = sourceEmail.String
	inv.Status = core.InvoiceStatus(status)
	inv.Currency = currency
	inv.AmountDue = core.Money{Minor: amountDue, Currency: currency}
	if dueDate.Valid {
		inv.DueDate = core.Date(dueDate.String)
	} else {
		inv.DueDate = ""
	}
	inv.CreatedDate = datePtr(createdDate)
	inv.SubTotal = moneyPtr(subtotal, currency)
	inv.Credit = moneyPtr(credit, currency)
	inv.Total = moneyPtr(total, currency)
	inv.AccountBalance = moneyPtr(balance, currency)
	inv.ParserVersion = parserVersion
	inv.URL = url
	inv.ConfirmedBy = int64Ptr(confirmedBy)
	inv.ConfirmedAt = timePtr(confirmedAt)
	inv.CreatedAt = parseTime(createdAt)
	inv.UpdatedAt = parseTime(updatedAt)
	return inv, nil
}

func (r *repo) invoiceItems(ctx context.Context, invoiceID string) ([]core.InvoiceItem, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT item_json FROM invoice_items WHERE invoice_id = ? ORDER BY id`, invoiceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.InvoiceItem
	for rows.Next() {
		var js string
		if err := rows.Scan(&js); err != nil {
			return nil, err
		}
		var item core.InvoiceItem
		if err := json.Unmarshal([]byte(js), &item); err != nil {
			return nil, fmt.Errorf("decode invoice item: %w", err)
		}
		item.InvoiceID = invoiceID
		out = append(out, item)
	}
	return out, rows.Err()
}

// ListInvoices filters by status, account, provider and limit.
func (r *repo) ListInvoices(ctx context.Context, filter core.InvoiceFilter) ([]core.Invoice, error) {
	query := `SELECT ` + invoiceColumns + ` FROM invoices WHERE 1 = 1`
	var args []any
	if filter.Status != "" {
		query += ` AND status = ?`
		args = append(args, string(filter.Status))
	}
	if filter.AccountID != "" {
		query += ` AND account_id = ?`
		args = append(args, filter.AccountID)
	}
	if filter.ProviderID != "" {
		query += ` AND provider_id = ?`
		args = append(args, filter.ProviderID)
	}
	query += ` ORDER BY created_at DESC, id`
	// Limit <= 0 is an internal full-scan request (for example the reminder
	// scheduler); a positive limit is capped to a safe maximum.
	if filter.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, capPositiveLimit(filter.Limit))
	}
	rows, err := r.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var out []core.Invoice
	for rows.Next() {
		inv, err := scanInvoice(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, inv)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	// Close before loading child rows so a single-connection pool (in-memory
	// databases) cannot deadlock on an open statement.
	rows.Close()
	for i := range out {
		items, err := r.invoiceItems(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Items = items
	}
	return out, nil
}

// TransitionInvoice performs a compare-and-swap on status inside a transaction.
// The confirmation metadata and event are written atomically; a same-target
// call is idempotent and adds no event.
func (r *repo) TransitionInvoice(ctx context.Context, id string, from, to core.InvoiceStatus, actor int64, at time.Time) (core.Invoice, error) {
	if !validInvoiceStatus(from) || !validInvoiceStatus(to) {
		return core.Invoice{}, fmt.Errorf("%w: invoice status", core.ErrInvalid)
	}
	if at.IsZero() {
		at = time.Now().UTC()
	} else {
		at = at.UTC()
	}
	var out core.Invoice
	err := r.inTx(ctx, func(t *repo) error {
		inv, err := t.getInvoice(ctx, id)
		if err != nil {
			return err
		}
		if inv.Status == to {
			out = inv
			return nil
		}
		if inv.Status != from {
			return fmt.Errorf("%w: invoice %s is %s, want %s", core.ErrConflict, id, inv.Status, from)
		}
		inv.Status = to
		switch to {
		case core.PaidConfirmed:
			v := actor
			inv.ConfirmedBy = &v
			inv.ConfirmedAt = &at
		case core.PendingConfirmation:
			inv.ConfirmedBy = nil
			inv.ConfirmedAt = nil
		}
		inv.UpdatedAt = at
		js, err := marshalJSON(inv)
		if err != nil {
			return err
		}
		res, err := t.q.ExecContext(ctx, `UPDATE invoices
			SET status = ?, confirmed_by = ?, confirmed_at = ?, updated_at = ?, invoice_json = ?
			WHERE id = ? AND status = ?`,
			string(to), nullInt64Ptr(inv.ConfirmedBy), nullTimePtr(inv.ConfirmedAt), fmtTime(at), js, id, string(from))
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("%w: concurrent invoice transition", core.ErrConflict)
		}
		if err := t.insertInvoiceEvent(ctx, id, string(to), actor, at); err != nil {
			return err
		}
		out = inv
		return nil
	})
	return out, err
}

// InvoiceEvents returns the audit trail for an invoice.
func (r *repo) InvoiceEvents(ctx context.Context, invoiceID string) ([]core.InvoiceEvent, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT id, invoice_id, action, actor_user_id, created_at
		FROM invoice_events WHERE invoice_id = ? ORDER BY rowid`, invoiceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.InvoiceEvent
	for rows.Next() {
		var ev core.InvoiceEvent
		var createdAt string
		if err := rows.Scan(&ev.ID, &ev.InvoiceID, &ev.Action, &ev.ActorUserID, &createdAt); err != nil {
			return nil, err
		}
		ev.CreatedAt = parseTime(createdAt)
		out = append(out, ev)
	}
	return out, rows.Err()
}

// CreateReminder creates a reminder together with its outbox row in one
// transaction. Only pending invoices receive reminders; a duplicate reminder
// date is a no-op and never leaves an orphan outbox row.
func (r *repo) CreateReminder(ctx context.Context, rem core.InvoiceReminder, del core.Delivery) (bool, error) {
	var created bool
	err := r.inTx(ctx, func(t *repo) error {
		inv, err := t.getInvoice(ctx, rem.InvoiceID)
		if err != nil {
			return err
		}
		if inv.Status != core.PendingConfirmation {
			return nil
		}
		if rem.ReminderDate == "" {
			return fmt.Errorf("%w: reminder date", core.ErrInvalid)
		}
		reminderDate, err := core.ParseDate(string(rem.ReminderDate))
		if err != nil {
			return err
		}
		rem.ReminderDate = reminderDate
		var exists int
		err = t.q.QueryRowContext(ctx, `SELECT 1 FROM invoice_reminders WHERE invoice_id = ? AND reminder_date = ?`,
			rem.InvoiceID, string(rem.ReminderDate)).Scan(&exists)
		if err == nil {
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if del.DedupKey == "" {
			del.DedupKey = "reminder:" + rem.InvoiceID + ":" + string(rem.ReminderDate)
		}
		now := time.Now().UTC()
		outboxID, _, status, err := t.enqueue(ctx, del)
		if err != nil {
			return err
		}
		if rem.ID == "" {
			rem.ID = core.NewID()
		}
		if rem.CreatedAt.IsZero() {
			rem.CreatedAt = now
		}
		rem.OutboxID = outboxID
		rem.Status = status
		if status == core.DeliverySent {
			rem.SentAt = &now
		}
		_, err = t.q.ExecContext(ctx, `INSERT INTO invoice_reminders (id, invoice_id, reminder_date, status, outbox_id, created_at, sent_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			rem.ID, rem.InvoiceID, string(rem.ReminderDate), string(rem.Status), rem.OutboxID,
			fmtTime(rem.CreatedAt), nullTimePtr(rem.SentAt))
		if err != nil {
			return mapErr(err)
		}
		created = true
		return nil
	})
	return created, err
}

// ListReminders returns reminders for an invoice.
func (r *repo) ListReminders(ctx context.Context, invoiceID string) ([]core.InvoiceReminder, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT id, invoice_id, reminder_date, status, outbox_id, created_at, sent_at
		FROM invoice_reminders WHERE invoice_id = ? ORDER BY reminder_date, id`, invoiceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.InvoiceReminder
	for rows.Next() {
		var rem core.InvoiceReminder
		var reminderDate, status, createdAt string
		var sentAt sql.NullString
		if err := rows.Scan(&rem.ID, &rem.InvoiceID, &reminderDate, &status, &rem.OutboxID, &createdAt, &sentAt); err != nil {
			return nil, err
		}
		rem.ReminderDate = core.Date(reminderDate)
		rem.Status = core.DeliveryStatus(status)
		rem.CreatedAt = parseTime(createdAt)
		rem.SentAt = timePtr(sentAt)
		out = append(out, rem)
	}
	return out, rows.Err()
}

func validInvoiceStatus(s core.InvoiceStatus) bool {
	switch s {
	case core.PendingConfirmation, core.PaidConfirmed, core.ReviewRequired, core.Cancelled:
		return true
	}
	return false
}

func nullStringPtr(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}
