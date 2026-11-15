// Package reminder schedules one local-calendar-day payment reminder per
// pending invoice.
//
// The scheduler is deliberately idempotent: the persistent
// UNIQUE(invoice_id, reminder_date) constraint is the final guard, and every
// candidate invoice is re-read inside the write transaction so a payment
// confirmation or cancellation racing with the tick cannot produce a stale
// reminder. Outbox retries are owned by the delivery layer; a successful
// reminder task is never created twice.
package reminder

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"email-invoice-guard/internal/core"
)

const (
	defaultTimezone     = "Asia/Taipei"
	defaultReminderTime = "09:00"
	defaultInterval     = 30 * time.Second
)

// Scheduler creates daily reminders for pending invoices at a local time.
type Scheduler struct {
	store    core.Store
	cfg      core.Config
	logger   *slog.Logger
	now      func() time.Time
	interval time.Duration
}

// New returns a scheduler. Configuration errors are reported by Tick/Run so a
// bad persisted setting never panics the process.
func New(store core.Store, cfg core.Config, logger *slog.Logger) *Scheduler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{
		store:    store,
		cfg:      cfg,
		logger:   logger,
		now:      time.Now,
		interval: defaultInterval,
	}
}

// Tick creates due reminders for the local date containing now. It is safe to
// call repeatedly; duplicate work is rejected by the persistent unique key.
func (s *Scheduler) Tick(ctx context.Context, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sc, err := s.resolve(ctx)
	if err != nil {
		return err
	}
	local := now.In(sc.loc)
	if beforeSchedule(local, sc.hour, sc.minute) {
		return nil
	}
	today := core.LocalDate(now, sc.loc)
	invoices, err := s.store.ListInvoices(ctx, core.InvoiceFilter{Status: core.PendingConfirmation})
	if err != nil {
		return fmt.Errorf("list pending invoices: %w", err)
	}
	var errs []error
	for _, invoice := range invoices {
		if err := ctx.Err(); err != nil {
			return err
		}
		if invoice.ID == "" || invoice.Status != core.PendingConfirmation {
			continue
		}
		// The initial enhanced notification already covers the creation day.
		if core.LocalDate(invoice.CreatedAt, sc.loc) == today {
			continue
		}
		if err := s.scheduleOne(ctx, invoice, sc, today, now); err != nil {
			errs = append(errs, fmt.Errorf("invoice %s: %w", invoice.ID, err))
		}
	}
	return errors.Join(errs...)
}

// Run ticks until ctx is cancelled. Invalid persisted configuration stops the
// loop with an error; transient failures are logged and retried next tick.
func (s *Scheduler) Run(ctx context.Context) error {
	if err := s.tick(ctx); err != nil {
		return err
	}
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := s.tick(ctx); err != nil {
				return err
			}
		}
	}
}

func (s *Scheduler) tick(ctx context.Context) error {
	err := s.Tick(ctx, s.now())
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return nil
	case errors.Is(err, core.ErrInvalid):
		return err
	default:
		s.logger.Warn("reminder tick failed", "error", err)
		return nil
	}
}

func (s *Scheduler) scheduleOne(ctx context.Context, invoice core.Invoice, sc schedule, today core.Date, now time.Time) error {
	reminder := core.InvoiceReminder{
		ID:           core.NewID(),
		InvoiceID:    invoice.ID,
		ReminderDate: today,
		// Creation never marks a reminder sent; delivery owns that transition.
		Status:    core.DeliveryPending,
		CreatedAt: now.UTC(),
	}
	delivery := core.Delivery{
		ID:       core.NewID(),
		DedupKey: "reminder:" + invoice.ID + ":" + string(today),
		Payload: core.DeliveryPayload{
			Kind:       "reminder",
			InvoiceID:  invoice.ID,
			ReminderID: reminder.ID,
			Text:       reminderText(invoice, today),
		},
		Status:      core.DeliveryPending,
		AvailableAt: now.UTC(),
		CreatedAt:   now.UTC(),
	}
	return s.store.Transaction(ctx, func(repo core.Repository) error {
		current, err := repo.GetInvoice(ctx, invoice.ID)
		if err != nil {
			return fmt.Errorf("reload invoice: %w", err)
		}
		if current.Status != core.PendingConfirmation {
			return nil
		}
		if core.LocalDate(current.CreatedAt, sc.loc) == today {
			return nil
		}
		reminder.OutboxID = delivery.ID
		created, err := repo.CreateReminder(ctx, reminder, delivery)
		if err != nil {
			return fmt.Errorf("create reminder: %w", err)
		}
		if !created {
			s.logger.Debug("reminder already exists",
				"invoice_id", invoice.ID, "reminder_date", today)
		}
		return nil
	})
}

type schedule struct {
	loc    *time.Location
	hour   int
	minute int
}

func (s *Scheduler) resolve(ctx context.Context) (schedule, error) {
	zone, err := s.setting(ctx, "timezone", s.cfg.Timezone, defaultTimezone)
	if err != nil {
		return schedule{}, err
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return schedule{}, fmt.Errorf("%w: timezone %q", core.ErrInvalid, zone)
	}
	raw, err := s.setting(ctx, "reminder_time", s.cfg.ReminderTime, defaultReminderTime)
	if err != nil {
		return schedule{}, err
	}
	at, err := time.Parse("15:04", raw)
	if err != nil {
		return schedule{}, fmt.Errorf("%w: reminder_time %q", core.ErrInvalid, raw)
	}
	return schedule{loc: loc, hour: at.Hour(), minute: at.Minute()}, nil
}

// setting reads a dynamic setting on every tick. The configuration value is a
// fallback only when the setting is absent, so Telegram /settings changes take
// effect without a restart.
func (s *Scheduler) setting(ctx context.Context, key, fallback, def string) (string, error) {
	value, err := s.store.GetSetting(ctx, key)
	switch {
	case err == nil:
		value = strings.TrimSpace(value)
		if value == "" {
			return "", fmt.Errorf("%w: empty %s setting", core.ErrInvalid, key)
		}
		return value, nil
	case errors.Is(err, core.ErrNotFound):
		value = strings.TrimSpace(fallback)
		if value == "" {
			return def, nil
		}
		return value, nil
	default:
		return "", fmt.Errorf("read %s setting: %w", key, err)
	}
}

func beforeSchedule(local time.Time, hour, minute int) bool {
	if local.Hour() != hour {
		return local.Hour() < hour
	}
	return local.Minute() < minute
}

func reminderText(invoice core.Invoice, today core.Date) string {
	text := fmt.Sprintf("Invoice %s for %s is due on %s.",
		invoice.Number, core.FormatMoney(invoice.AmountDue), invoice.DueDate)
	// Overdue is derived from the calendar due date, never from invoice status.
	if days, err := core.DaysUntil(invoice.DueDate, today); err == nil {
		switch {
		case days < 0:
			text += fmt.Sprintf(" Overdue by %d day(s).", -days)
		case days == 0:
			text += " Due today."
		}
	}
	return text
}
