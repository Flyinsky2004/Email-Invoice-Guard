package telegram

import (
	"context"
	"errors"
	"fmt"

	"email-invoice-guard/internal/core"
)

// DeliverOnce claims at most one due outbox job and delivers it. It returns
// true when a job was claimed (whether it succeeded, failed or was skipped) and
// false when the queue is empty. Delivery failures are persisted through
// FailDelivery and do not surface as errors.
func (b *Bot) DeliverOnce(ctx context.Context) (bool, error) {
	delivery, err := b.store.ClaimDelivery(ctx, b.now(), deliveryLease)
	if err != nil {
		return false, fmt.Errorf("telegram: claim delivery: %w", err)
	}
	if delivery == nil {
		return false, nil
	}
	b.deliver(ctx, delivery)
	return true, nil
}

func (b *Bot) deliver(ctx context.Context, delivery *core.Delivery) {
	parts, markup, invoiceID, ok := b.renderDelivery(ctx, delivery)
	if !ok {
		return
	}

	existing, err := b.store.DeliveryMessages(ctx, delivery.ID)
	if err != nil {
		b.failDelivery(ctx, delivery, false, "load delivered parts: "+sanitizeReason(err))
		return
	}
	alreadySent := make(map[int]bool, len(existing))
	all := make([]core.TelegramMessage, 0, len(existing)+len(parts))
	for _, m := range existing {
		alreadySent[m.Part] = true
		all = append(all, m)
	}

	lastPart := len(parts) - 1
	for i, text := range parts {
		if alreadySent[i] {
			continue
		}
		if err := b.limiter.wait(ctx, b.cfg.Telegram.ChatID); err != nil {
			b.failDelivery(ctx, delivery, true, "canceled before send")
			return
		}
		// Re-check the reminder's invoice immediately before each send. A
		// payment confirmed while this job waited for the rate limit must not
		// produce a stale reminder.
		if delivery.Payload.Kind == "reminder" {
			current, err := b.store.GetInvoice(ctx, delivery.Payload.InvoiceID)
			if err != nil {
				if errors.Is(err, core.ErrNotFound) {
					b.skipDelivery(ctx, delivery, "reminder invoice disappeared before send")
				} else {
					b.failDelivery(ctx, delivery, false, "reminder status recheck: "+sanitizeReason(err))
				}
				return
			}
			if current.Status != core.PendingConfirmation {
				b.skipDelivery(ctx, delivery, "reminder became stale before send")
				return
			}
		}
		var kb *inlineKeyboard
		if i == lastPart {
			kb = markup
		}
		msg, err := b.api.sendMessage(ctx, b.cfg.Telegram.ChatID, text, kb)
		if err != nil {
			var ae *apiError
			if errors.As(err, &ae) {
				if ae.RetryAfter > 0 {
					b.setDelay(ae.RetryAfter)
				}
				b.failDelivery(ctx, delivery, false, err.Error())
			} else {
				// Transport failures are ambiguous: the message may have been
				// delivered. Retry with at-least-once semantics.
				b.failDelivery(ctx, delivery, true, err.Error())
			}
			return
		}
		chatID := msg.Chat.ID
		if chatID == 0 {
			chatID = b.cfg.Telegram.ChatID
		}
		record := core.TelegramMessage{
			ID:        core.NewID(),
			OutboxID:  delivery.ID,
			InvoiceID: invoiceID,
			ChatID:    chatID,
			MessageID: msg.MessageID,
			Part:      i,
			CreatedAt: b.now(),
		}
		if err := b.store.RecordTelegramMessage(ctx, record); err != nil {
			// The part was acknowledged by Telegram. Persisting the ID failed,
			// so a retry may duplicate this part; never fail the delivery here.
			b.log.Error("telegram: record delivered part failed", "outbox", delivery.ID, "part", i, "error", err.Error())
		}
		all = append(all, record)
	}

	sctx, cancel := b.persistContext(ctx)
	defer cancel()
	if err := b.store.CompleteDelivery(sctx, delivery.ID, all, b.now()); err != nil {
		b.log.Error("telegram: complete delivery failed", "outbox", delivery.ID, "error", err.Error())
	}
}

// renderDelivery resolves the payload to its formatted parts. It returns false
// when the job was skipped because its source is gone or a reminder is stale.
func (b *Bot) renderDelivery(ctx context.Context, delivery *core.Delivery) (parts []string, markup *inlineKeyboard, invoiceID string, ok bool) {
	payload := delivery.Payload
	if payload.Kind == "manual" || payload.ManualPlanID != "" {
		return b.renderManualDelivery(ctx, delivery)
	}
	invoiceDelivery := payload.Kind == "invoice" || payload.Kind == "reminder" ||
		(payload.InvoiceID != "" && payload.EmailID == "")
	if invoiceDelivery {
		inv, err := b.store.GetInvoice(ctx, payload.InvoiceID)
		if err != nil {
			if errors.Is(err, core.ErrNotFound) {
				b.skipDelivery(ctx, delivery, "invoice unavailable")
			} else {
				// A transient store failure must be retried, not discarded.
				b.failDelivery(ctx, delivery, false, "load invoice: "+sanitizeReason(err))
			}
			return nil, nil, "", false
		}
		if payload.Kind == "reminder" && inv.Status != core.PendingConfirmation {
			// The invoice was paid or cancelled after the reminder was queued.
			b.skipDelivery(ctx, delivery, "reminder is stale")
			return nil, nil, "", false
		}
		account, err := b.deliveryAccount(ctx, inv.AccountID, core.Microsoft)
		if err != nil {
			b.failDelivery(ctx, delivery, false, "load account: "+sanitizeReason(err))
			return nil, nil, "", false
		}
		parts := formatInvoiceAt(inv, account, b.location(ctx))
		if payload.Kind == "reminder" {
			// Recompute against the current local date instead of trusting the
			// scheduling-time payload text.
			parts = appendEscapedLine(parts, b.reminderNote(ctx, inv))
		}
		// Prefer the mail that triggered this notification so a second mail for
		// the same invoice still opens its own full text. The persisted invoice
		// is never mutated.
		keyboardInvoice := inv
		if payload.EmailID != "" {
			keyboardInvoice.SourceEmailID = payload.EmailID
		}
		return parts, b.invoiceKeyboard(keyboardInvoice), inv.ID, true
	}

	email, err := b.store.GetEmail(ctx, payload.EmailID)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			b.skipDelivery(ctx, delivery, "email unavailable")
		} else {
			b.failDelivery(ctx, delivery, false, "load email: "+sanitizeReason(err))
		}
		return nil, nil, "", false
	}
	account, err := b.deliveryAccount(ctx, email.AccountID, email.ProviderType)
	if err != nil {
		b.failDelivery(ctx, delivery, false, "load account: "+sanitizeReason(err))
		return nil, nil, "", false
	}
	return formatEmailAt(email, account, b.location(ctx)), nil, "", true
}

// renderManualDelivery renders a daily summary for an operator-declared plan.
// A nil manual service disables the branch entirely; a missing source is
// skipped, while a transient read failure is retried.
func (b *Bot) renderManualDelivery(ctx context.Context, delivery *core.Delivery) (parts []string, markup *inlineKeyboard, invoiceID string, ok bool) {
	if b.manual == nil {
		b.skipDelivery(ctx, delivery, "manual reminders are disabled")
		return nil, nil, "", false
	}
	payload := delivery.Payload
	if payload.ManualPlanID == "" {
		b.skipDelivery(ctx, delivery, "manual payload has no plan id")
		return nil, nil, "", false
	}
	plan, err := b.manual.GetPlan(ctx, payload.ManualPlanID)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			b.skipDelivery(ctx, delivery, "manual plan unavailable")
		} else {
			b.failDelivery(ctx, delivery, false, "load manual plan: "+sanitizeReason(err))
		}
		return nil, nil, "", false
	}
	occurrences := make([]core.ManualOccurrence, 0, len(payload.ManualOccurrenceIDs))
	for _, id := range payload.ManualOccurrenceIDs {
		occ, err := b.manual.GetOccurrence(ctx, id)
		if err != nil {
			if errors.Is(err, core.ErrNotFound) {
				b.skipDelivery(ctx, delivery, "manual occurrence unavailable")
			} else {
				b.failDelivery(ctx, delivery, false, "load manual occurrence: "+sanitizeReason(err))
			}
			return nil, nil, "", false
		}
		occurrences = append(occurrences, occ)
	}
	if len(occurrences) == 0 {
		b.skipDelivery(ctx, delivery, "manual summary has no occurrences")
		return nil, nil, "", false
	}
	return formatManualSummaryAt(plan, occurrences), b.manualSummaryKeyboard(plan, occurrences), "", true
}

// reminderNote renders the due-distance line at delivery time so a reminder
// queued earlier still reports the correct number of days.
func (b *Bot) reminderNote(ctx context.Context, inv core.Invoice) string {
	if inv.DueDate == "" {
		return "Reminder: due date unknown."
	}
	today := core.LocalDate(b.now(), b.location(ctx))
	days, err := core.DaysUntil(inv.DueDate, today)
	if err != nil {
		return "Reminder: due " + string(inv.DueDate) + "."
	}
	switch {
	case days > 0:
		return fmt.Sprintf("Reminder: due in %d day(s) (%s).", days, inv.DueDate)
	case days == 0:
		return fmt.Sprintf("Reminder: due today (%s).", inv.DueDate)
	default:
		return fmt.Sprintf("Reminder: overdue by %d day(s) (%s).", -days, inv.DueDate)
	}
}

// appendEscapedLine adds one escaped line to the final part when it fits,
// otherwise it starts a new part.
func appendEscapedLine(parts []string, line string) []string {
	escaped := escape(line)
	if n := len(parts); n > 0 && utf16Len(parts[n-1])+1+utf16Len(escaped) <= maxMessageUnits {
		parts[n-1] += "\n" + escaped
		return parts
	}
	return append(parts, escaped)
}

// deliveryAccount loads the account for rendering. A missing account is
// tolerated (historical mail survives soft removal), but a transient store
// failure is returned so the delivery is retried rather than dropped.
func (b *Bot) deliveryAccount(ctx context.Context, accountID string, provider core.ProviderType) (core.MailAccount, error) {
	account, err := b.store.GetAccount(ctx, accountID)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			return core.MailAccount{ID: accountID, Provider: provider}, nil
		}
		return core.MailAccount{}, err
	}
	return account, nil
}

func (b *Bot) failDelivery(ctx context.Context, delivery *core.Delivery, unknown bool, reason string) {
	sctx, cancel := b.persistContext(ctx)
	defer cancel()
	if err := b.store.FailDelivery(sctx, delivery.ID, b.now(), unknown, sanitizeReason(errors.New(reason))); err != nil {
		b.log.Error("telegram: persist delivery failure failed", "outbox", delivery.ID, "error", err.Error())
	}
	b.log.Warn("telegram: delivery attempt failed", "outbox", delivery.ID, "unknown", unknown, "reason", sanitizeReason(errors.New(reason)))
}

func (b *Bot) skipDelivery(ctx context.Context, delivery *core.Delivery, reason string) {
	sctx, cancel := b.persistContext(ctx)
	defer cancel()
	if err := b.store.SkipDelivery(sctx, delivery.ID, b.now()); err != nil {
		b.log.Error("telegram: persist delivery skip failed", "outbox", delivery.ID, "error", err.Error())
	}
	b.log.Warn("telegram: delivery skipped", "outbox", delivery.ID, "reason", reason)
}
