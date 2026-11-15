package telegram

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"email-invoice-guard/internal/core"
)

func pendingInvoice() core.Invoice {
	return sampleInvoice()
}

func TestPaidTwoStageConfirmation(t *testing.T) {
	env := newTestEnv(t)
	invoice := pendingInvoice()
	env.store.getInvoice = func(context.Context, string) (core.Invoice, error) { return invoice, nil }
	env.store.getAccount = func(context.Context, string) (core.MailAccount, error) { return sampleAccount(), nil }
	env.store.listTelegramMessages = func(context.Context, string) ([]core.TelegramMessage, error) {
		return []core.TelegramMessage{{ID: testID(8), InvoiceID: invoice.ID, ChatID: 42, MessageID: 55, Part: 0}}, nil
	}

	answers := env.runCallback(callbackData(cbPaidPrompt, invoice.ID))
	if len(answers) != 1 {
		t.Fatalf("answers = %d, want 1", len(answers))
	}
	if env.payment.callCount() != 0 {
		t.Fatal("payment state changed before the confirmation callback")
	}
	edits := env.tg.callsFor("editMessageText")
	if len(edits) != 1 {
		t.Fatalf("editMessageText calls = %d, want 1", len(edits))
	}
	promptText, _ := edits[0].Body["text"].(string)
	if !strings.Contains(promptText, "Confirm manual payment") {
		t.Fatalf("confirmation prompt missing: %q", promptText)
	}
	markup, _ := edits[0].Body["reply_markup"].(map[string]any)
	raw, _ := json.Marshal(markup)
	serialized := string(raw)
	for _, want := range []string{"pc:", "pu:", "px:"} {
		if !strings.Contains(serialized, want) {
			t.Fatalf("confirmation keyboard missing %q: %s", want, serialized)
		}
	}

	paid := invoice
	paid.Status = core.PaidConfirmed
	env.payment.confirm = func(context.Context, string, int64) (core.Invoice, error) { return paid, nil }
	env.tg.reset()
	answers = env.runCallback(callbackData(cbPaidConfirm, invoice.ID))
	if len(answers) != 1 {
		t.Fatalf("confirm answers = %d, want 1", len(answers))
	}
	if env.payment.callCount() != 1 {
		t.Fatalf("Confirm calls = %d, want 1", env.payment.callCount())
	}
	if text, _ := answers[0].Body["text"].(string); text != "Marked as paid" {
		t.Fatalf("unexpected confirm answer: %q", text)
	}
	if got := env.tg.count("editMessageText"); got < 1 {
		t.Fatalf("stored invoice message was not refreshed: edits=%d", got)
	}
}

func TestPaidReplayIsIdempotent(t *testing.T) {
	env := newTestEnv(t)
	invoice := pendingInvoice()
	invoice.Status = core.PaidConfirmed
	env.store.getInvoice = func(context.Context, string) (core.Invoice, error) { return invoice, nil }
	env.store.getAccount = func(context.Context, string) (core.MailAccount, error) { return sampleAccount(), nil }
	env.store.listTelegramMessages = func(context.Context, string) ([]core.TelegramMessage, error) {
		return []core.TelegramMessage{{ID: testID(8), InvoiceID: invoice.ID, ChatID: 42, MessageID: 55}}, nil
	}
	answers := env.runCallback(callbackData(cbPaidConfirm, invoice.ID))
	if env.payment.callCount() != 0 {
		t.Fatal("replayed confirmation called the payment service")
	}
	if text, _ := answers[0].Body["text"].(string); text != "Already marked as paid" {
		t.Fatalf("unexpected replay answer: %q", text)
	}
}

func TestUndoRestoresPending(t *testing.T) {
	env := newTestEnv(t)
	invoice := pendingInvoice()
	invoice.Status = core.PaidConfirmed
	env.store.getInvoice = func(context.Context, string) (core.Invoice, error) { return invoice, nil }
	env.store.getAccount = func(context.Context, string) (core.MailAccount, error) { return sampleAccount(), nil }
	restored := invoice
	restored.Status = core.PendingConfirmation
	env.payment.undo = func(context.Context, string, int64) (core.Invoice, error) { return restored, nil }

	answers := env.runCallback(callbackData(cbPaidUndo, invoice.ID))
	if env.payment.callCount() != 1 {
		t.Fatalf("Undo calls = %d, want 1", env.payment.callCount())
	}
	if text, _ := answers[0].Body["text"].(string); text != "Restored to pending" {
		t.Fatalf("unexpected undo answer: %q", text)
	}
}

func TestCancelInvoice(t *testing.T) {
	env := newTestEnv(t)
	invoice := pendingInvoice()
	env.store.getInvoice = func(context.Context, string) (core.Invoice, error) { return invoice, nil }
	env.store.getAccount = func(context.Context, string) (core.MailAccount, error) { return sampleAccount(), nil }
	cancelled := invoice
	cancelled.Status = core.Cancelled
	env.payment.cancel = func(context.Context, string, int64) (core.Invoice, error) { return cancelled, nil }
	answers := env.runCallback(callbackData(cbPaidCancel, invoice.ID))
	if env.payment.callCount() != 1 {
		t.Fatalf("Cancel calls = %d, want 1", env.payment.callCount())
	}
	if text, _ := answers[0].Body["text"].(string); text != "Invoice cancelled" {
		t.Fatalf("unexpected cancel answer: %q", text)
	}
}

func TestConfirmEditFailureDoesNotUndoPaid(t *testing.T) {
	env := newTestEnv(t)
	invoice := pendingInvoice()
	env.store.getInvoice = func(context.Context, string) (core.Invoice, error) { return invoice, nil }
	env.store.getAccount = func(context.Context, string) (core.MailAccount, error) { return sampleAccount(), nil }
	env.store.listTelegramMessages = func(context.Context, string) ([]core.TelegramMessage, error) {
		return []core.TelegramMessage{{ID: testID(8), InvoiceID: invoice.ID, ChatID: 42, MessageID: 55}}, nil
	}
	paid := invoice
	paid.Status = core.PaidConfirmed
	env.payment.confirm = func(context.Context, string, int64) (core.Invoice, error) { return paid, nil }
	env.tg.setHandler("editMessageText", func(context.Context, map[string]any) (any, *tgFailure) {
		return nil, &tgFailure{code: 400, description: "message can't be edited"}
	})

	answers := env.runCallback(callbackData(cbPaidConfirm, invoice.ID))
	if text, _ := answers[0].Body["text"].(string); text != "Marked as paid" {
		t.Fatalf("transient edit failure changed the answer: %q", text)
	}
	env.payment.mu.Lock()
	defer env.payment.mu.Unlock()
	for _, call := range env.payment.calls {
		if call == "undo" {
			t.Fatal("paid state was rolled back after a transient edit failure")
		}
	}
}

func TestCallbackEmailSendsCompleteContent(t *testing.T) {
	env := newTestEnv(t)
	email := sampleEmail()
	email.TextBody = strings.Repeat("full body line\n", 700)
	env.store.getEmail = func(context.Context, string) (core.NormalizedEmail, error) { return email, nil }
	env.store.getAccount = func(context.Context, string) (core.MailAccount, error) { return sampleAccount(), nil }

	env.runCallback(callbackData(cbEmail, email.InternalEmailID))
	expected := FormatEmail(email, sampleAccount())
	calls := env.tg.callsFor("sendMessage")
	if len(calls) != len(expected) {
		t.Fatalf("sendMessage calls = %d, want %d", len(calls), len(expected))
	}
	all := sentText(calls)
	if got, want := strings.Count(all, "full body line"), 700; got != want {
		t.Fatalf("full email content truncated: got %d lines, want %d", got, want)
	}
}

func TestCallbackInvoiceSendsDetail(t *testing.T) {
	env := newTestEnv(t)
	invoice := pendingInvoice()
	env.store.getInvoice = func(context.Context, string) (core.Invoice, error) { return invoice, nil }
	env.store.getAccount = func(context.Context, string) (core.MailAccount, error) { return sampleAccount(), nil }

	env.runCallback(callbackData(cbInvoice, invoice.ID))
	calls := env.tg.callsFor("sendMessage")
	if len(calls) == 0 {
		t.Fatal("invoice detail was not sent")
	}
	text := sentText(calls)
	if !strings.Contains(text, "INV-2026-001") || !strings.Contains(text, "VPS-2C4G") {
		t.Fatalf("invoice detail incomplete: %q", text)
	}
	markup, ok := calls[len(calls)-1].Body["reply_markup"].(map[string]any)
	if !ok {
		t.Fatal("invoice detail keyboard missing")
	}
	raw, _ := json.Marshal(markup)
	if !strings.Contains(string(raw), "em:") {
		t.Fatalf("original mail callback missing from keyboard: %s", raw)
	}
}

func TestCallbackUnknownActionFailsClosed(t *testing.T) {
	env := newTestEnv(t)
	env.store.getInvoice = func(context.Context, string) (core.Invoice, error) {
		t.Fatal("unknown callback reached the store")
		return core.Invoice{}, nil
	}
	answers := env.runCallback("zz:" + testID(3))
	if len(answers) != 1 {
		t.Fatalf("answers = %d, want 1", len(answers))
	}
	if text, _ := answers[0].Body["text"].(string); text != "Unknown action" {
		t.Fatalf("unexpected answer: %q", text)
	}
}

func TestCallbackRemoveForeignAccountDenied(t *testing.T) {
	env := newTestEnv(t)
	account := core.MailAccount{ID: testID(2), Address: "other@example.com", OwnerUserID: 99}
	env.store.getAccount = func(context.Context, string) (core.MailAccount, error) { return account, nil }
	env.store.removeAccount = func(context.Context, string) error {
		t.Fatal("foreign account removal attempted")
		return nil
	}
	answers := env.runCallback(callbackData(cbRemoveConfirm, account.ID))
	if text, _ := answers[0].Body["text"].(string); text != "Not authorized" {
		t.Fatalf("unexpected answer: %q", text)
	}
}

func TestCallbackPaymentErrorsAreReported(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{core.ErrConflict, "Cannot change this invoice"},
		{core.ErrUnauthorized, "Not authorized"},
		{core.ErrNotFound, "Invoice not found"},
	}
	for _, tc := range cases {
		env := newTestEnv(t)
		invoice := pendingInvoice()
		env.store.getInvoice = func(context.Context, string) (core.Invoice, error) { return invoice, nil }
		env.payment.confirm = func(context.Context, string, int64) (core.Invoice, error) {
			return core.Invoice{}, tc.err
		}
		answers := env.runCallback(callbackData(cbPaidConfirm, invoice.ID))
		if text, _ := answers[0].Body["text"].(string); !strings.Contains(text, tc.want) {
			t.Fatalf("error %v produced %q, want %q", tc.err, text, tc.want)
		}
	}
}
