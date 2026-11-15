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

// Store satisfies the standalone manual-reminder repository without widening
// core.Repository or changing any existing signature.
var _ core.ManualReminderRepository = (*Store)(nil)

const (
	manualPlanColumns = `id, title, provider_name, asset_id, description, amount_minor, currency,
		billing_day, reminder_time, timezone, payment_url, notes, status, next_due_date,
		created_at, updated_at, cancelled_at`
	manualOccurrenceColumns = `id, plan_id, due_date, amount_minor_snapshot, currency_snapshot,
		payment_status, notified_at, confirmed_at, confirmed_by, created_at, updated_at`
	manualDispatchColumns = `id, plan_id, outbox_id, local_date, kind, status, created_at, sent_at`
	manualEventColumns    = `id, plan_id, occurrence_id, event_type, actor_id, event_at, metadata`
)

// manualNow normalizes a caller-supplied instant to UTC and defaults a zero
// value to the wall clock. Callers pass an explicit time for deterministic
// transitions; storage never invents a different one.
func manualNow(at time.Time) time.Time {
	if at.IsZero() {
		return time.Now().UTC()
	}
	return at.UTC()
}

func normalizeManualDate(d core.Date, field string) (core.Date, error) {
	if strings.TrimSpace(string(d)) == "" {
		return "", fmt.Errorf("%w: %s", core.ErrInvalid, field)
	}
	parsed, err := core.ParseDate(string(d))
	if err != nil {
		return "", fmt.Errorf("%w: %s", core.ErrInvalid, field)
	}
	return parsed, nil
}

func validManualPlanStatus(s core.ManualPlanStatus) bool {
	switch s {
	case core.ManualPlanActive, core.ManualPlanPaused, core.ManualPlanCancelled:
		return true
	}
	return false
}

func validManualOccurrenceStatus(s core.ManualOccurrenceStatus) bool {
	switch s {
	case core.ManualOccurrencePending, core.ManualOccurrencePaid, core.ManualOccurrenceCancelled:
		return true
	}
	return false
}

func validManualEventType(s string) bool {
	switch s {
	case core.ManualEventPlanCreated, core.ManualEventPlanUpdated, core.ManualEventPlanPaused,
		core.ManualEventPlanResumed, core.ManualEventPlanCancelled,
		core.ManualEventOccurrenceCreated, core.ManualEventReminderSent,
		core.ManualEventPaymentConfirmed, core.ManualEventPaymentConfirmationRevoked,
		core.ManualEventOccurrenceCancelled:
		return true
	}
	return false
}

// validateManualPlan checks the invariants the schema enforces plus the ones
// callers rely on (identity, canonical calendar dates, paired money).
func validateManualPlan(p core.ManualPlan) error {
	if strings.TrimSpace(p.Title) == "" {
		return fmt.Errorf("%w: plan title", core.ErrInvalid)
	}
	if p.BillingDay < 1 || p.BillingDay > 31 {
		return fmt.Errorf("%w: billing day", core.ErrInvalid)
	}
	if strings.TrimSpace(p.ReminderTime) == "" {
		return fmt.Errorf("%w: reminder time", core.ErrInvalid)
	}
	if strings.TrimSpace(p.Timezone) == "" {
		return fmt.Errorf("%w: timezone", core.ErrInvalid)
	}
	if !validManualPlanStatus(p.Status) {
		return fmt.Errorf("%w: plan status", core.ErrInvalid)
	}
	if _, err := normalizeManualDate(p.NextDueDate, "next due date"); err != nil {
		return err
	}
	if p.Amount != nil && strings.TrimSpace(p.Amount.Currency) == "" {
		return fmt.Errorf("%w: amount currency", core.ErrInvalid)
	}
	return nil
}

func validateManualOccurrence(o core.ManualOccurrence) error {
	if o.PlanID == "" {
		return fmt.Errorf("%w: occurrence plan", core.ErrInvalid)
	}
	if !validManualOccurrenceStatus(o.Status) {
		return fmt.Errorf("%w: occurrence status", core.ErrInvalid)
	}
	if o.Amount != nil && strings.TrimSpace(o.Amount.Currency) == "" {
		return fmt.Errorf("%w: occurrence amount currency", core.ErrInvalid)
	}
	return nil
}

// insertManualEvent appends an audit row. Metadata is bounded JSON; an empty
// value is stored as the documented empty object.
func (r *repo) insertManualEvent(ctx context.Context, ev core.ManualReminderEvent, at time.Time) error {
	if ev.PlanID == "" {
		return fmt.Errorf("%w: manual event plan", core.ErrInvalid)
	}
	if !validManualEventType(ev.EventType) {
		return fmt.Errorf("%w: manual event type", core.ErrInvalid)
	}
	if ev.Metadata == "" {
		ev.Metadata = "{}"
	}
	if len(ev.Metadata) > 4096 || !json.Valid([]byte(ev.Metadata)) {
		return fmt.Errorf("%w: manual event metadata", core.ErrInvalid)
	}
	if ev.ID == "" {
		ev.ID = core.NewID()
	}
	_, err := r.q.ExecContext(ctx, `INSERT INTO manual_reminder_events
		(id, plan_id, occurrence_id, event_type, actor_id, event_at, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ev.ID, ev.PlanID, nullString(ev.OccurrenceID), ev.EventType, ev.ActorID, fmtTime(at), ev.Metadata)
	return mapErr(err)
}

// CreatePlan inserts a plan and its creation event atomically.
func (r *repo) CreatePlan(ctx context.Context, plan core.ManualPlan, ev core.ManualReminderEvent) (core.ManualPlan, error) {
	var out core.ManualPlan
	err := r.inTx(ctx, func(t *repo) error {
		if plan.ID == "" {
			plan.ID = core.NewID()
		}
		if plan.Status == "" {
			plan.Status = core.ManualPlanActive
		}
		now := time.Now().UTC()
		if plan.CreatedAt.IsZero() {
			plan.CreatedAt = now
		}
		if plan.UpdatedAt.IsZero() {
			plan.UpdatedAt = plan.CreatedAt
		}
		plan.NextDueDate, _ = core.ParseDate(string(plan.NextDueDate))
		if err := validateManualPlan(plan); err != nil {
			return err
		}
		_, err := t.q.ExecContext(ctx, `INSERT INTO manual_reminder_plans
			(id, title, provider_name, asset_id, description, amount_minor, currency,
			 billing_day, reminder_time, timezone, payment_url, notes, status, next_due_date,
			 created_at, updated_at, cancelled_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			plan.ID, plan.Title, plan.ProviderName, nullString(plan.AssetID), plan.Description,
			nullMoneyPtr(plan.Amount), nullStringPtr(amountCurrency(plan.Amount)),
			plan.BillingDay, plan.ReminderTime, plan.Timezone, plan.PaymentURL, plan.Notes,
			string(plan.Status), string(plan.NextDueDate), fmtTime(plan.CreatedAt),
			fmtTime(plan.UpdatedAt), nullTimePtr(plan.CancelledAt))
		if err != nil {
			return mapErr(err)
		}
		ev.PlanID = plan.ID
		if ev.EventType == "" {
			ev.EventType = core.ManualEventPlanCreated
		}
		if ev.CreatedAt.IsZero() {
			ev.CreatedAt = now
		}
		if err := t.insertManualEvent(ctx, ev, ev.CreatedAt); err != nil {
			return err
		}
		out = plan
		return nil
	})
	return out, err
}

// amountCurrency returns the currency for a non-nil amount so the paired
// CHECK constraint can never be violated by a half-populated money value.
func amountCurrency(m *core.Money) *string {
	if m == nil {
		return nil
	}
	c := m.Currency
	return &c
}

// UpdatePlan updates mutable plan fields and appends an event atomically. A
// missing plan is ErrNotFound; a cancelled plan is never resurrected.
func (r *repo) UpdatePlan(ctx context.Context, plan core.ManualPlan, ev core.ManualReminderEvent) (core.ManualPlan, error) {
	var out core.ManualPlan
	err := r.inTx(ctx, func(t *repo) error {
		if plan.ID == "" {
			return fmt.Errorf("%w: plan id", core.ErrInvalid)
		}
		existing, err := t.getManualPlan(ctx, plan.ID)
		if err != nil {
			return err
		}
		if existing.Status == core.ManualPlanCancelled {
			return fmt.Errorf("%w: cancelled plan cannot be updated", core.ErrConflict)
		}
		plan.Status = existing.Status
		plan.CreatedAt = existing.CreatedAt
		plan.CancelledAt = existing.CancelledAt
		plan.UpdatedAt = time.Now().UTC()
		plan.NextDueDate, _ = core.ParseDate(string(plan.NextDueDate))
		if err := validateManualPlan(plan); err != nil {
			return err
		}
		res, err := t.q.ExecContext(ctx, `UPDATE manual_reminder_plans
			SET title = ?, provider_name = ?, asset_id = ?, description = ?, amount_minor = ?,
			    currency = ?, billing_day = ?, reminder_time = ?, timezone = ?, payment_url = ?,
			    notes = ?, next_due_date = ?, updated_at = ?
			WHERE id = ? AND status <> 'CANCELLED'`,
			plan.Title, plan.ProviderName, nullString(plan.AssetID), plan.Description,
			nullMoneyPtr(plan.Amount), nullStringPtr(amountCurrency(plan.Amount)),
			plan.BillingDay, plan.ReminderTime, plan.Timezone, plan.PaymentURL, plan.Notes,
			string(plan.NextDueDate), fmtTime(plan.UpdatedAt), plan.ID)
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("%w: concurrent plan update", core.ErrConflict)
		}
		ev.PlanID = plan.ID
		if ev.EventType == "" {
			ev.EventType = core.ManualEventPlanUpdated
		}
		if ev.CreatedAt.IsZero() {
			ev.CreatedAt = plan.UpdatedAt
		}
		if err := t.insertManualEvent(ctx, ev, ev.CreatedAt); err != nil {
			return err
		}
		out = plan
		return nil
	})
	return out, err
}

// GetPlan returns a plan or ErrNotFound.
func (r *repo) GetPlan(ctx context.Context, id string) (core.ManualPlan, error) {
	return r.getManualPlan(ctx, id)
}

func (r *repo) getManualPlan(ctx context.Context, id string) (core.ManualPlan, error) {
	row := r.q.QueryRowContext(ctx, `SELECT `+manualPlanColumns+` FROM manual_reminder_plans WHERE id = ?`, id)
	p, err := scanManualPlan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return core.ManualPlan{}, core.ErrNotFound
	}
	return p, err
}

func scanManualPlan(sc rowScanner) (core.ManualPlan, error) {
	var p core.ManualPlan
	var assetID, currency, cancelledAt sql.NullString
	var amountMinor sql.NullInt64
	var billingDay int
	var status, nextDue, createdAt, updatedAt string
	if err := sc.Scan(&p.ID, &p.Title, &p.ProviderName, &assetID, &p.Description, &amountMinor,
		&currency, &billingDay, &p.ReminderTime, &p.Timezone, &p.PaymentURL, &p.Notes,
		&status, &nextDue, &createdAt, &updatedAt, &cancelledAt); err != nil {
		return core.ManualPlan{}, err
	}
	p.AssetID = assetID.String
	p.Amount = moneyPtr(amountMinor, currency.String)
	p.BillingDay = billingDay
	p.Status = core.ManualPlanStatus(status)
	p.NextDueDate = core.Date(nextDue)
	p.CreatedAt = parseTime(createdAt)
	p.UpdatedAt = parseTime(updatedAt)
	p.CancelledAt = timePtr(cancelledAt)
	return p, nil
}

// ListPlans filters by status and treats Limit <= 0 as unbounded.
func (r *repo) ListPlans(ctx context.Context, filter core.ManualPlanFilter) ([]core.ManualPlan, error) {
	query := `SELECT ` + manualPlanColumns + ` FROM manual_reminder_plans WHERE 1 = 1`
	var args []any
	if filter.Status != "" {
		if !validManualPlanStatus(filter.Status) {
			return nil, fmt.Errorf("%w: plan status", core.ErrInvalid)
		}
		query += ` AND status = ?`
		args = append(args, string(filter.Status))
	}
	query += ` ORDER BY next_due_date, id`
	if filter.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, capPositiveLimit(filter.Limit))
	}
	rows, err := r.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.ManualPlan
	for rows.Next() {
		p, err := scanManualPlan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PausePlan performs ACTIVE -> PAUSED.
func (r *repo) PausePlan(ctx context.Context, id string, actor int64, at time.Time) (core.ManualPlan, error) {
	at = manualNow(at)
	var out core.ManualPlan
	err := r.inTx(ctx, func(t *repo) error {
		p, err := t.getManualPlan(ctx, id)
		if err != nil {
			return err
		}
		if p.Status != core.ManualPlanActive {
			return fmt.Errorf("%w: plan %s is %s", core.ErrConflict, id, p.Status)
		}
		res, err := t.q.ExecContext(ctx, `UPDATE manual_reminder_plans
			SET status = 'PAUSED', updated_at = ? WHERE id = ? AND status = 'ACTIVE'`,
			fmtTime(at), id)
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("%w: concurrent plan pause", core.ErrConflict)
		}
		p.Status = core.ManualPlanPaused
		p.UpdatedAt = at
		if err := t.insertManualEvent(ctx, core.ManualReminderEvent{
			PlanID: id, EventType: core.ManualEventPlanPaused, ActorID: actor,
		}, at); err != nil {
			return err
		}
		out = p
		return nil
	})
	return out, err
}

// ResumePlan performs PAUSED -> ACTIVE with the supplied next due date. Missed
// periods are never backfilled.
func (r *repo) ResumePlan(ctx context.Context, id string, nextDue core.Date, actor int64, at time.Time) (core.ManualPlan, error) {
	at = manualNow(at)
	next, err := normalizeManualDate(nextDue, "next due date")
	if err != nil {
		return core.ManualPlan{}, err
	}
	var out core.ManualPlan
	err = r.inTx(ctx, func(t *repo) error {
		p, err := t.getManualPlan(ctx, id)
		if err != nil {
			return err
		}
		if p.Status != core.ManualPlanPaused {
			return fmt.Errorf("%w: plan %s is %s", core.ErrConflict, id, p.Status)
		}
		res, err := t.q.ExecContext(ctx, `UPDATE manual_reminder_plans
			SET status = 'ACTIVE', next_due_date = ?, updated_at = ?
			WHERE id = ? AND status = 'PAUSED'`,
			string(next), fmtTime(at), id)
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("%w: concurrent plan resume", core.ErrConflict)
		}
		p.Status = core.ManualPlanActive
		p.NextDueDate = next
		p.UpdatedAt = at
		if err := t.insertManualEvent(ctx, core.ManualReminderEvent{
			PlanID: id, EventType: core.ManualEventPlanResumed, ActorID: actor,
		}, at); err != nil {
			return err
		}
		out = p
		return nil
	})
	return out, err
}

// CancelPlan cancels the plan, its pending occurrences and any queued manual
// outbox rows in one transaction.
func (r *repo) CancelPlan(ctx context.Context, id string, actor int64, at time.Time) (core.ManualPlan, error) {
	at = manualNow(at)
	var out core.ManualPlan
	err := r.inTx(ctx, func(t *repo) error {
		p, err := t.getManualPlan(ctx, id)
		if err != nil {
			return err
		}
		if p.Status == core.ManualPlanCancelled {
			return fmt.Errorf("%w: plan %s is already cancelled", core.ErrConflict, id)
		}
		res, err := t.q.ExecContext(ctx, `UPDATE manual_reminder_plans
			SET status = 'CANCELLED', cancelled_at = ?, updated_at = ?
			WHERE id = ? AND status <> 'CANCELLED'`,
			fmtTime(at), fmtTime(at), id)
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("%w: concurrent plan cancel", core.ErrConflict)
		}
		occurrenceIDs, err := t.manualOccurrenceIDs(ctx, `plan_id = ? AND payment_status = 'PENDING_CONFIRMATION'`, id)
		if err != nil {
			return err
		}
		for _, occID := range occurrenceIDs {
			if _, err := t.q.ExecContext(ctx, `UPDATE manual_reminder_occurrences
				SET payment_status = 'CANCELLED', updated_at = ?
				WHERE id = ? AND payment_status = 'PENDING_CONFIRMATION'`, fmtTime(at), occID); err != nil {
				return mapErr(err)
			}
			if err := t.insertManualEvent(ctx, core.ManualReminderEvent{
				PlanID: id, OccurrenceID: occID, EventType: core.ManualEventOccurrenceCancelled, ActorID: actor,
			}, at); err != nil {
				return err
			}
		}
		outboxIDs, err := t.manualDispatchOutboxIDs(ctx, id)
		if err != nil {
			return err
		}
		for _, outboxID := range outboxIDs {
			if _, err := t.q.ExecContext(ctx, `UPDATE delivery_outbox
				SET status = 'SKIPPED', lease_until = NULL
				WHERE id = ? AND status <> 'SENT'`, outboxID); err != nil {
				return mapErr(err)
			}
			if _, err := t.q.ExecContext(ctx, `UPDATE manual_reminder_dispatches
				SET status = 'SKIPPED' WHERE outbox_id = ? AND status <> 'SENT'`, outboxID); err != nil {
				return mapErr(err)
			}
		}
		if err := t.insertManualEvent(ctx, core.ManualReminderEvent{
			PlanID: id, EventType: core.ManualEventPlanCancelled, ActorID: actor,
		}, at); err != nil {
			return err
		}
		p.Status = core.ManualPlanCancelled
		p.CancelledAt = &at
		p.UpdatedAt = at
		out = p
		return nil
	})
	return out, err
}

// manualOccurrenceIDs collects matching IDs before any dependent write runs, so
// an open *sql.Rows never coexists with a subquery on a single connection.
func (r *repo) manualOccurrenceIDs(ctx context.Context, where string, args ...any) ([]string, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT id FROM manual_reminder_occurrences WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	return ids, nil
}

func (r *repo) manualDispatchOutboxIDs(ctx context.Context, planID string) ([]string, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT outbox_id FROM manual_reminder_dispatches
		WHERE plan_id = ? AND status IN ('PENDING', 'SENDING', 'FAILED', 'UNKNOWN')`, planID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	return ids, nil
}

// CreateOccurrence inserts the occurrence, CAS-advances next_due_date from
// occ.DueDate to next and writes OCCURRENCE_CREATED atomically. It returns
// false when the plan is not ACTIVE, the occurrence exists, or next_due_date
// no longer matches.
func (r *repo) CreateOccurrence(ctx context.Context, occ core.ManualOccurrence, next core.Date) (bool, error) {
	var created bool
	err := r.inTx(ctx, func(t *repo) error {
		if occ.PlanID == "" {
			return fmt.Errorf("%w: occurrence plan", core.ErrInvalid)
		}
		due, err := normalizeManualDate(occ.DueDate, "occurrence due date")
		if err != nil {
			return err
		}
		occ.DueDate = due
		nextDue, err := normalizeManualDate(next, "next due date")
		if err != nil {
			return err
		}
		if occ.Status == "" {
			occ.Status = core.ManualOccurrencePending
		}
		if err := validateManualOccurrence(occ); err != nil {
			return err
		}
		plan, err := t.getManualPlan(ctx, occ.PlanID)
		if err != nil {
			return err
		}
		// Snapshot the plan amount so later plan edits cannot rewrite the
		// financial history of an existing occurrence.
		if occ.Amount == nil && plan.Amount != nil {
			amount := *plan.Amount
			occ.Amount = &amount
		}
		if plan.Status != core.ManualPlanActive {
			return nil
		}
		if plan.NextDueDate != occ.DueDate {
			return nil
		}
		var exists int
		err = t.q.QueryRowContext(ctx, `SELECT 1 FROM manual_reminder_occurrences WHERE plan_id = ? AND due_date = ?`,
			occ.PlanID, string(occ.DueDate)).Scan(&exists)
		if err == nil {
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		now := time.Now().UTC()
		res, err := t.q.ExecContext(ctx, `UPDATE manual_reminder_plans
			SET next_due_date = ?, updated_at = ?
			WHERE id = ? AND status = 'ACTIVE' AND next_due_date = ?`,
			string(nextDue), fmtTime(now), occ.PlanID, string(occ.DueDate))
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return nil
		}
		if occ.ID == "" {
			occ.ID = core.NewID()
		}
		if occ.CreatedAt.IsZero() {
			occ.CreatedAt = now
		}
		occ.UpdatedAt = now
		_, err = t.q.ExecContext(ctx, `INSERT INTO manual_reminder_occurrences
			(id, plan_id, due_date, amount_minor_snapshot, currency_snapshot, payment_status,
			 notified_at, confirmed_at, confirmed_by, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			occ.ID, occ.PlanID, string(occ.DueDate), nullMoneyPtr(occ.Amount),
			nullStringPtr(amountCurrency(occ.Amount)), string(occ.Status), nullTimePtr(occ.NotifiedAt),
			nullTimePtr(occ.ConfirmedAt), nullInt64Ptr(occ.ConfirmedBy),
			fmtTime(occ.CreatedAt), fmtTime(occ.UpdatedAt))
		if err != nil {
			return mapErr(err)
		}
		if err := t.insertManualEvent(ctx, core.ManualReminderEvent{
			PlanID: occ.PlanID, OccurrenceID: occ.ID, EventType: core.ManualEventOccurrenceCreated,
		}, now); err != nil {
			return err
		}
		created = true
		return nil
	})
	return created, err
}

// CreateDispatch inserts one summary dispatch, its join rows and the outbox
// delivery atomically. It returns false when no supplied occurrence is still
// pending or the (plan, local date, kind) dispatch already exists. The
// occurrences are marked notified only when the delivery completes.
func (r *repo) CreateDispatch(ctx context.Context, planID string, occurrenceIDs []string, d core.ManualDispatch, del core.Delivery) (bool, error) {
	var created bool
	err := r.inTx(ctx, func(t *repo) error {
		if planID == "" {
			return fmt.Errorf("%w: dispatch plan", core.ErrInvalid)
		}
		if _, err := t.getManualPlan(ctx, planID); err != nil {
			return err
		}
		if d.Kind == "" {
			d.Kind = core.ManualDispatchDailySummary
		}
		if d.Kind != core.ManualDispatchDailySummary {
			return fmt.Errorf("%w: dispatch kind", core.ErrInvalid)
		}
		localDate, err := normalizeManualDate(d.LocalDate, "dispatch local date")
		if err != nil {
			return err
		}
		d.LocalDate = localDate
		var exists int
		err = t.q.QueryRowContext(ctx, `SELECT 1 FROM manual_reminder_dispatches
			WHERE plan_id = ? AND local_date = ? AND kind = ?`,
			planID, string(d.LocalDate), d.Kind).Scan(&exists)
		if err == nil {
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		pending, err := t.pendingManualOccurrenceIDs(ctx, planID, occurrenceIDs)
		if err != nil {
			return err
		}
		if len(pending) == 0 {
			return nil
		}
		if del.DedupKey == "" {
			del.DedupKey = "manual-reminder:" + planID + ":" + string(d.LocalDate)
		}
		now := time.Now().UTC()
		outboxID, _, status, err := t.enqueue(ctx, del)
		if err != nil {
			return err
		}
		if d.ID == "" {
			d.ID = core.NewID()
		}
		if d.CreatedAt.IsZero() {
			d.CreatedAt = now
		}
		d.PlanID = planID
		d.OutboxID = outboxID
		d.Status = status
		if status == core.DeliverySent {
			sent := now
			d.SentAt = &sent
		} else {
			d.SentAt = nil
		}
		if _, err := t.q.ExecContext(ctx, `INSERT INTO manual_reminder_dispatches
			(id, plan_id, outbox_id, local_date, kind, status, created_at, sent_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			d.ID, d.PlanID, d.OutboxID, string(d.LocalDate), d.Kind, string(d.Status),
			fmtTime(d.CreatedAt), nullTimePtr(d.SentAt)); err != nil {
			return mapErr(err)
		}
		for _, occID := range pending {
			if _, err := t.q.ExecContext(ctx, `INSERT INTO manual_reminder_dispatch_occurrences
				(dispatch_id, occurrence_id) VALUES (?, ?)`, d.ID, occID); err != nil {
				return mapErr(err)
			}
		}
		d.OccurrenceIDs = pending
		created = true
		return nil
	})
	return created, err
}

// pendingManualOccurrenceIDs keeps only supplied IDs that still belong to the
// plan and are PENDING_CONFIRMATION, preserving caller order and dropping
// duplicates.
func (r *repo) pendingManualOccurrenceIDs(ctx context.Context, planID string, ids []string) ([]string, error) {
	seen := make(map[string]bool, len(ids))
	var out []string
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		var exists int
		err := r.q.QueryRowContext(ctx, `SELECT 1 FROM manual_reminder_occurrences
			WHERE id = ? AND plan_id = ? AND payment_status = 'PENDING_CONFIRMATION'`,
			id, planID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, nil
}

// GetOccurrence returns an occurrence or ErrNotFound.
func (r *repo) GetOccurrence(ctx context.Context, id string) (core.ManualOccurrence, error) {
	return r.getManualOccurrence(ctx, id)
}

func (r *repo) getManualOccurrence(ctx context.Context, id string) (core.ManualOccurrence, error) {
	row := r.q.QueryRowContext(ctx, `SELECT `+manualOccurrenceColumns+` FROM manual_reminder_occurrences WHERE id = ?`, id)
	o, err := scanManualOccurrence(row)
	if errors.Is(err, sql.ErrNoRows) {
		return core.ManualOccurrence{}, core.ErrNotFound
	}
	return o, err
}

func scanManualOccurrence(sc rowScanner) (core.ManualOccurrence, error) {
	var o core.ManualOccurrence
	var amountMinor, confirmedBy sql.NullInt64
	var currency, notifiedAt, confirmedAt sql.NullString
	var dueDate, status, createdAt, updatedAt string
	if err := sc.Scan(&o.ID, &o.PlanID, &dueDate, &amountMinor, &currency, &status, &notifiedAt,
		&confirmedAt, &confirmedBy, &createdAt, &updatedAt); err != nil {
		return core.ManualOccurrence{}, err
	}
	o.DueDate = core.Date(dueDate)
	o.Amount = moneyPtr(amountMinor, currency.String)
	o.Status = core.ManualOccurrenceStatus(status)
	o.NotifiedAt = timePtr(notifiedAt)
	o.ConfirmedAt = timePtr(confirmedAt)
	o.ConfirmedBy = int64Ptr(confirmedBy)
	o.CreatedAt = parseTime(createdAt)
	o.UpdatedAt = parseTime(updatedAt)
	return o, nil
}

// ListOccurrences filters by plan, status, due-on-or-before and limit.
func (r *repo) ListOccurrences(ctx context.Context, filter core.ManualOccurrenceFilter) ([]core.ManualOccurrence, error) {
	query := `SELECT ` + manualOccurrenceColumns + ` FROM manual_reminder_occurrences WHERE 1 = 1`
	var args []any
	if filter.PlanID != "" {
		query += ` AND plan_id = ?`
		args = append(args, filter.PlanID)
	}
	if filter.Status != "" {
		if !validManualOccurrenceStatus(filter.Status) {
			return nil, fmt.Errorf("%w: occurrence status", core.ErrInvalid)
		}
		query += ` AND payment_status = ?`
		args = append(args, string(filter.Status))
	}
	if filter.DueOnOrBefore != "" {
		due, err := normalizeManualDate(filter.DueOnOrBefore, "due date filter")
		if err != nil {
			return nil, err
		}
		query += ` AND due_date <= ?`
		args = append(args, string(due))
	}
	query += ` ORDER BY due_date, id`
	if filter.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, capPositiveLimit(filter.Limit))
	}
	rows, err := r.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.ManualOccurrence
	for rows.Next() {
		o, err := scanManualOccurrence(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// TransitionOccurrence is a compare-and-swap on payment_status. A same-target
// replay is idempotent and adds no event; confirmation metadata is written only
// for PAID_CONFIRMED.
func (r *repo) TransitionOccurrence(ctx context.Context, id string, from, to core.ManualOccurrenceStatus, actor int64, at time.Time) (core.ManualOccurrence, error) {
	if !validManualOccurrenceStatus(from) || !validManualOccurrenceStatus(to) {
		return core.ManualOccurrence{}, fmt.Errorf("%w: occurrence status", core.ErrInvalid)
	}
	at = manualNow(at)
	var out core.ManualOccurrence
	err := r.inTx(ctx, func(t *repo) error {
		occ, err := t.getManualOccurrence(ctx, id)
		if err != nil {
			return err
		}
		if occ.Status == to {
			out = occ
			return nil
		}
		if occ.Status != from {
			return fmt.Errorf("%w: occurrence %s is %s, want %s", core.ErrConflict, id, occ.Status, from)
		}
		var confirmedBy *int64
		var confirmedAt *time.Time
		if to == core.ManualOccurrencePaid {
			actorCopy := actor
			confirmedBy = &actorCopy
			confirmedAt = &at
		}
		res, err := t.q.ExecContext(ctx, `UPDATE manual_reminder_occurrences
			SET payment_status = ?, confirmed_by = ?, confirmed_at = ?, updated_at = ?
			WHERE id = ? AND payment_status = ?`,
			string(to), nullInt64Ptr(confirmedBy), nullTimePtr(confirmedAt), fmtTime(at),
			id, string(from))
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("%w: concurrent occurrence transition", core.ErrConflict)
		}
		occ.Status = to
		occ.ConfirmedBy = confirmedBy
		occ.ConfirmedAt = confirmedAt
		occ.UpdatedAt = at
		var eventType string
		switch to {
		case core.ManualOccurrencePaid:
			eventType = core.ManualEventPaymentConfirmed
		case core.ManualOccurrencePending:
			eventType = core.ManualEventPaymentConfirmationRevoked
		case core.ManualOccurrenceCancelled:
			eventType = core.ManualEventOccurrenceCancelled
		}
		if eventType != "" {
			if err := t.insertManualEvent(ctx, core.ManualReminderEvent{
				PlanID: occ.PlanID, OccurrenceID: occ.ID, EventType: eventType, ActorID: actor,
			}, at); err != nil {
				return err
			}
		}
		out = occ
		return nil
	})
	return out, err
}

// GetDispatch returns a dispatch with its linked occurrence IDs or ErrNotFound.
func (r *repo) GetDispatch(ctx context.Context, id string) (core.ManualDispatch, error) {
	row := r.q.QueryRowContext(ctx, `SELECT `+manualDispatchColumns+` FROM manual_reminder_dispatches WHERE id = ?`, id)
	d, err := scanManualDispatch(row)
	if errors.Is(err, sql.ErrNoRows) {
		return core.ManualDispatch{}, core.ErrNotFound
	}
	if err != nil {
		return core.ManualDispatch{}, err
	}
	ids, err := r.manualDispatchOccurrenceIDs(ctx, id)
	if err != nil {
		return core.ManualDispatch{}, err
	}
	d.OccurrenceIDs = ids
	return d, nil
}

func scanManualDispatch(sc rowScanner) (core.ManualDispatch, error) {
	var d core.ManualDispatch
	var localDate, status, createdAt string
	var sentAt sql.NullString
	if err := sc.Scan(&d.ID, &d.PlanID, &d.OutboxID, &localDate, &d.Kind, &status, &createdAt, &sentAt); err != nil {
		return core.ManualDispatch{}, err
	}
	d.LocalDate = core.Date(localDate)
	d.Status = core.DeliveryStatus(status)
	d.CreatedAt = parseTime(createdAt)
	d.SentAt = timePtr(sentAt)
	return d, nil
}

func (r *repo) manualDispatchOccurrenceIDs(ctx context.Context, dispatchID string) ([]string, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT occurrence_id FROM manual_reminder_dispatch_occurrences
		WHERE dispatch_id = ? ORDER BY occurrence_id`, dispatchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ListPlanDispatches returns a plan's dispatches ordered by local date and id.
func (r *repo) ListPlanDispatches(ctx context.Context, planID string) ([]core.ManualDispatch, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT `+manualDispatchColumns+`
		FROM manual_reminder_dispatches WHERE plan_id = ? ORDER BY local_date, id`, planID)
	if err != nil {
		return nil, err
	}
	var out []core.ManualDispatch
	for rows.Next() {
		d, err := scanManualDispatch(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	// Close before loading join rows so a single-connection pool cannot deadlock.
	rows.Close()
	for i := range out {
		ids, err := r.manualDispatchOccurrenceIDs(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].OccurrenceIDs = ids
	}
	return out, nil
}

// ListManualMessages returns Telegram parts linked through the plan's
// dispatches, ordered by part then id.
func (r *repo) ListManualMessages(ctx context.Context, planID string) ([]core.TelegramMessage, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT tm.id, tm.outbox_id, COALESCE(tm.invoice_id, ''),
			tm.chat_id, tm.message_id, tm.part, tm.created_at
		FROM telegram_messages tm
		JOIN manual_reminder_dispatches d ON d.outbox_id = tm.outbox_id
		WHERE d.plan_id = ?
		ORDER BY tm.part, tm.id`, planID)
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

// ManualEvents returns a plan's audit trail in insertion order (rowid), not
// caller-supplied event time.
func (r *repo) ManualEvents(ctx context.Context, planID string) ([]core.ManualReminderEvent, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT `+manualEventColumns+`
		FROM manual_reminder_events WHERE plan_id = ? ORDER BY rowid`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.ManualReminderEvent
	for rows.Next() {
		var ev core.ManualReminderEvent
		var occurrenceID sql.NullString
		var eventAt string
		if err := rows.Scan(&ev.ID, &ev.PlanID, &occurrenceID, &ev.EventType, &ev.ActorID, &eventAt, &ev.Metadata); err != nil {
			return nil, err
		}
		ev.OccurrenceID = occurrenceID.String
		ev.CreatedAt = parseTime(eventAt)
		out = append(out, ev)
	}
	return out, rows.Err()
}
