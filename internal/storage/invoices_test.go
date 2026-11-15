package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"email-invoice-guard/internal/core"
)

func TestInvoiceRoundTripPreservesEveryField(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustAccount(t, s, "acc-inv", core.Microsoft, "inv@example.com")
	email := mustEmail(t, s, sampleEmail("acc-inv", "inv-mail"))
	want := sampleInvoice("acc-inv", "INV-1001")
	want.SourceEmailID = email.InternalEmailID
	saved := mustInvoice(t, s, want)

	got, err := s.GetInvoice(ctx, saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccountID != want.AccountID || got.ProviderID != want.ProviderID || got.Number != want.Number {
		t.Fatalf("identity mismatch: %+v", got)
	}
	if got.SourceEmailID != email.InternalEmailID || got.ParserVersion != want.ParserVersion || got.URL != want.URL {
		t.Fatalf("metadata mismatch: %+v", got)
	}
	if got.Status != core.PendingConfirmation || got.Currency != "USD" || got.AmountDue.Minor != 684 {
		t.Fatalf("state mismatch: %+v", got)
	}
	if got.CreatedDate == nil || *got.CreatedDate != *want.CreatedDate || got.DueDate != want.DueDate {
		t.Fatalf("dates mismatch: %+v", got)
	}
	if got.PaymentMethod == nil || *got.PaymentMethod != *want.PaymentMethod {
		t.Fatalf("payment method mismatch: %+v", got.PaymentMethod)
	}
	if got.SubTotal == nil || got.SubTotal.Minor != 700 || got.Credit == nil || got.Credit.Minor != 16 {
		t.Fatalf("money mismatch: %+v", got)
	}
	if got.Total == nil || got.Total.Minor != 684 || got.AccountBalance == nil || got.AccountBalance.Minor != 0 {
		t.Fatalf("totals mismatch: %+v", got)
	}
	if len(got.ReviewReasons) != 1 || got.ReviewReasons[0] != "manual check" {
		t.Fatalf("review reasons mismatch: %+v", got.ReviewReasons)
	}
	if len(got.Items) != 1 {
		t.Fatalf("items = %d", len(got.Items))
	}
	item := got.Items[0]
	if item.ID == "" || item.InvoiceID != saved.ID {
		t.Fatalf("item ids not normalized: %+v", item)
	}
	if item.Hostname == nil || *item.Hostname != "node-1.rfchost.example" || item.ProductModel == nil || *item.ProductModel != "VPS-2" {
		t.Fatalf("item mismatch: %+v", item)
	}
	if item.ServiceStart == nil || *item.ServiceStart != *want.Items[0].ServiceStart {
		t.Fatalf("item service start mismatch: %+v", item.ServiceStart)
	}
	if item.FinalPrice == nil || item.FinalPrice.Minor != 684 {
		t.Fatalf("item price mismatch: %+v", item.FinalPrice)
	}
	if got.ConfirmedBy != nil || got.ConfirmedAt != nil {
		t.Fatalf("new invoice already confirmed: %+v", got)
	}
}

func TestDuplicateInvoicePreservesPaidStateAndAddsSourceLinks(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustAccount(t, s, "acc-dup", core.Microsoft, "dup@example.com")
	firstMail := mustEmail(t, s, sampleEmail("acc-dup", "dup-mail-1"))
	secondMail := mustEmail(t, s, sampleEmail("acc-dup", "dup-mail-2"))

	invoice := sampleInvoice("acc-dup", "INV-DUP")
	invoice.SourceEmailID = firstMail.InternalEmailID
	saved := mustInvoice(t, s, invoice)

	paid, err := s.TransitionInvoice(ctx, saved.ID, core.PendingConfirmation, core.PaidConfirmed, 9, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if paid.Status != core.PaidConfirmed || paid.ConfirmedBy == nil || *paid.ConfirmedBy != 9 {
		t.Fatalf("transition failed: %+v", paid)
	}

	duplicate := sampleInvoice("acc-dup", "INV-DUP")
	duplicate.SourceEmailID = secondMail.InternalEmailID
	duplicate.Status = core.ReviewRequired
	got, created, err := s.SaveInvoice(ctx, duplicate)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("duplicate business key created a second invoice")
	}
	if got.ID != saved.ID {
		t.Fatalf("duplicate returned different invoice: %s", got.ID)
	}
	if got.Status != core.PaidConfirmed {
		t.Fatalf("duplicate overwrote paid state with %s", got.Status)
	}
	if got.ConfirmedBy == nil || *got.ConfirmedBy != 9 {
		t.Fatalf("duplicate cleared confirmation: %+v", got.ConfirmedBy)
	}
	var links int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM invoice_email_links WHERE invoice_id = ?`, saved.ID).Scan(&links); err != nil {
		t.Fatal(err)
	}
	if links != 2 {
		t.Fatalf("source links = %d, want 2", links)
	}
	events, err := s.InvoiceEvents(ctx, saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want CREATED + PAID_CONFIRMED", len(events))
	}
}

func TestTransitionCompareAndSwapIsIdempotent(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustAccount(t, s, "acc-trans", core.Microsoft, "trans@example.com")
	invoice := mustInvoice(t, s, sampleInvoice("acc-trans", "INV-TRANS"))

	at := time.Date(2026, 11, 2, 3, 4, 5, 6, time.UTC)
	paid, err := s.TransitionInvoice(ctx, invoice.ID, core.PendingConfirmation, core.PaidConfirmed, 11, at)
	if err != nil {
		t.Fatal(err)
	}
	if paid.ConfirmedBy == nil || *paid.ConfirmedBy != 11 || paid.ConfirmedAt == nil || !paid.ConfirmedAt.Equal(at) {
		t.Fatalf("confirmation metadata: %+v", paid)
	}
	replay, err := s.TransitionInvoice(ctx, invoice.ID, core.PaidConfirmed, core.PaidConfirmed, 99, at.Add(time.Hour))
	if err != nil {
		t.Fatalf("idempotent transition: %v", err)
	}
	if replay.ConfirmedBy == nil || *replay.ConfirmedBy != 11 {
		t.Fatalf("idempotent transition changed confirmation: %+v", replay.ConfirmedBy)
	}
	events, err := s.InvoiceEvents(ctx, invoice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2 (duplicate adds none)", len(events))
	}
	if events[0].Action != "CREATED" || events[1].Action != "PAID_CONFIRMED" {
		t.Fatalf("event actions = %+v", events)
	}

	// A different target whose expected source does not match the current
	// status must conflict rather than silently overwrite.
	if _, err := s.TransitionInvoice(ctx, invoice.ID, core.ReviewRequired, core.Cancelled, 1, at); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("wrong source status = %v, want ErrConflict", err)
	}
}

func TestTransitionUndoClearsConfirmation(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustAccount(t, s, "acc-undo", core.Microsoft, "undo@example.com")
	invoice := mustInvoice(t, s, sampleInvoice("acc-undo", "INV-UNDO"))
	if _, err := s.TransitionInvoice(ctx, invoice.ID, core.PendingConfirmation, core.PaidConfirmed, 3, time.Now()); err != nil {
		t.Fatal(err)
	}
	undone, err := s.TransitionInvoice(ctx, invoice.ID, core.PaidConfirmed, core.PendingConfirmation, 3, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if undone.Status != core.PendingConfirmation {
		t.Fatalf("status = %s", undone.Status)
	}
	if undone.ConfirmedBy != nil || undone.ConfirmedAt != nil {
		t.Fatalf("undo did not clear confirmation: %+v", undone)
	}
	// The JSON snapshot must agree with the authoritative scalar state.
	got, err := s.GetInvoice(ctx, invoice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != core.PendingConfirmation || got.ConfirmedBy != nil {
		t.Fatalf("json/scalar drift: %+v", got)
	}
}

func TestSaveInvoiceValidation(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustAccount(t, s, "acc-val", core.Microsoft, "val@example.com")
	noNumber := sampleInvoice("acc-val", "")
	if _, _, err := s.SaveInvoice(ctx, noNumber); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("empty number = %v", err)
	}
	badStatus := sampleInvoice("acc-val", "INV-BAD")
	badStatus.Status = core.InvoiceStatus("BOGUS")
	if _, _, err := s.SaveInvoice(ctx, badStatus); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("bad status = %v", err)
	}
	missingAccount := sampleInvoice("missing", "INV-MISS")
	if _, _, err := s.SaveInvoice(ctx, missingAccount); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("missing account = %v", err)
	}
	badDate := sampleInvoice("acc-val", "INV-DATE")
	badDate.DueDate = core.Date("2026-13-40")
	if _, _, err := s.SaveInvoice(ctx, badDate); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("invalid due date = %v", err)
	}
	valid := mustInvoice(t, s, sampleInvoice("acc-val", "INV-VALID"))
	badReminder := core.InvoiceReminder{InvoiceID: valid.ID, ReminderDate: core.Date("not-a-date")}
	if _, err := s.CreateReminder(ctx, badReminder, core.Delivery{DedupKey: "bad-date"}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("invalid reminder date = %v", err)
	}
}

func TestCreateReminderOnlyForPendingAndUnique(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustAccount(t, s, "acc-rem", core.Microsoft, "rem@example.com")
	invoice := mustInvoice(t, s, sampleInvoice("acc-rem", "INV-REM"))

	rem := core.InvoiceReminder{InvoiceID: invoice.ID, ReminderDate: core.Date("2026-10-20")}
	del := core.Delivery{DedupKey: "reminder:" + invoice.ID + ":2026-10-20", Payload: core.DeliveryPayload{Kind: "reminder", InvoiceID: invoice.ID}}
	created, err := s.CreateReminder(ctx, rem, del)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("reminder not created for pending invoice")
	}
	reminders, err := s.ListReminders(ctx, invoice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reminders) != 1 || reminders[0].Status != core.DeliveryPending || reminders[0].OutboxID == "" {
		t.Fatalf("reminder = %+v", reminders)
	}
	if created, err := s.CreateReminder(ctx, rem, del); err != nil || created {
		t.Fatalf("duplicate reminder: created=%v err=%v", created, err)
	}
	var outboxCount int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM delivery_outbox`).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if outboxCount != 1 {
		t.Fatalf("outbox rows = %d, want 1 (no orphan)", outboxCount)
	}

	if _, err := s.TransitionInvoice(ctx, invoice.ID, core.PendingConfirmation, core.PaidConfirmed, 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	created, err = s.CreateReminder(ctx, core.InvoiceReminder{InvoiceID: invoice.ID, ReminderDate: core.Date("2026-10-21")}, core.Delivery{DedupKey: "reminder:paid"})
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("paid invoice received a reminder")
	}
}

func TestListInvoicesZeroLimitReturnsAll(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustAccount(t, s, "acc-all", core.Microsoft, "all@example.com")
	const total = 150
	err := s.Transaction(ctx, func(r core.Repository) error {
		for i := 0; i < total; i++ {
			if _, _, err := r.SaveInvoice(ctx, sampleInvoice("acc-all", "INV-ALL-"+threeDigits(i))); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	all, err := s.ListInvoices(ctx, core.InvoiceFilter{AccountID: "acc-all"})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != total {
		t.Fatalf("zero limit returned %d invoices, want %d", len(all), total)
	}
	limited, err := s.ListInvoices(ctx, core.InvoiceFilter{AccountID: "acc-all", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 10 {
		t.Fatalf("positive limit returned %d, want 10", len(limited))
	}
}

func threeDigits(n int) string {
	digits := []byte{'0', '0', '0'}
	for i := 2; i >= 0 && n > 0; i-- {
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	return string(digits)
}

func TestListInvoicesFilters(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustAccount(t, s, "acc-f1", core.Microsoft, "f1@example.com")
	mustAccount(t, s, "acc-f2", core.Microsoft, "f2@example.com")
	mustInvoice(t, s, sampleInvoice("acc-f1", "INV-F1"))
	second := sampleInvoice("acc-f1", "INV-F1B")
	second.Status = core.ReviewRequired
	mustInvoice(t, s, second)
	mustInvoice(t, s, sampleInvoice("acc-f2", "INV-F2"))

	pending, err := s.ListInvoices(ctx, core.InvoiceFilter{Status: core.PendingConfirmation})
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 {
		t.Fatalf("pending = %d, want 2", len(pending))
	}
	accountOne, err := s.ListInvoices(ctx, core.InvoiceFilter{AccountID: "acc-f1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(accountOne) != 2 {
		t.Fatalf("account f1 = %d, want 2", len(accountOne))
	}
	provider, err := s.ListInvoices(ctx, core.InvoiceFilter{ProviderID: "rfchost", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider) != 1 {
		t.Fatalf("limit not applied: %d", len(provider))
	}
}
