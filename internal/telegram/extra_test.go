package telegram

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"email-invoice-guard/internal/core"
)

func TestCommandWithBotSuffix(t *testing.T) {
	env := newTestEnv(t)
	text := sentText(env.runMessage("/help@InvoiceGuardBot"))
	if !strings.Contains(text, "Invoice Guard") {
		t.Fatalf("suffixed command was not handled: %q", text)
	}
}

func TestParseCommand(t *testing.T) {
	cases := []struct {
		text string
		cmd  string
		args []string
	}{
		{"/help", "help", nil},
		{"/Help@Bot", "help", nil},
		{"/search foo bar", "search", []string{"foo", "bar"}},
		{"plain text", "", nil},
		{"", "", nil},
	}
	for _, tc := range cases {
		cmd, args := parseCommand(tc.text)
		if cmd != tc.cmd {
			t.Fatalf("parseCommand(%q) cmd = %q, want %q", tc.text, cmd, tc.cmd)
		}
		if len(args) != len(tc.args) {
			t.Fatalf("parseCommand(%q) args = %v, want %v", tc.text, args, tc.args)
		}
		for i := range args {
			if args[i] != tc.args[i] {
				t.Fatalf("parseCommand(%q) args = %v, want %v", tc.text, args, tc.args)
			}
		}
	}
}

func TestRunOutboxDeliversQueuedJob(t *testing.T) {
	env := newTestEnv(t)
	rec := &deliveryRecorder{}
	rec.install(env.store)
	email := sampleEmail()
	env.store.getEmail = func(context.Context, string) (core.NormalizedEmail, error) { return email, nil }
	env.store.getAccount = func(context.Context, string) (core.MailAccount, error) { return sampleAccount(), nil }
	var claimed atomic.Bool
	env.store.claimDelivery = func(context.Context, time.Time, time.Duration) (*core.Delivery, error) {
		if claimed.CompareAndSwap(false, true) {
			return &core.Delivery{ID: testID(5), Payload: core.DeliveryPayload{Kind: "email", EmailID: email.InternalEmailID}}, nil
		}
		return nil, nil
	}
	env.tg.setHandler("getUpdates", func(ctx context.Context, _ map[string]any) (any, *tgFailure) {
		<-ctx.Done()
		return []update{}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- env.bot.Run(ctx) }()
	waitFor(t, 2*time.Second, func() bool { return env.tg.count("sendMessage") == 1 })
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
	recorded, completed, failed, _, _ := rec.snapshot()
	if len(failed) != 0 || len(recorded) != 1 || len(completed) != 1 {
		t.Fatalf("outbox loop did not deliver: recorded=%d completed=%d failed=%v", len(recorded), len(completed), failed)
	}
}

func TestAppendEscapedLineStartsNewPartWhenFull(t *testing.T) {
	parts := []string{strings.Repeat("x", maxMessageUnits)}
	out := appendEscapedLine(parts, "reminder line")
	if len(out) != 2 {
		t.Fatalf("parts = %d, want 2", len(out))
	}
	if out[1] != "reminder line" {
		t.Fatalf("unexpected appended part: %q", out[1])
	}
}

func TestFormatEmailEmptyBodyFallback(t *testing.T) {
	email := sampleEmail()
	email.TextBody = ""
	email.HTMLBody = ""
	text := joined(FormatEmail(email, sampleAccount()))
	if !strings.Contains(text, "(no text content)") {
		t.Fatalf("empty body fallback missing: %q", text)
	}
}

func TestCallbackMissingSourcesReported(t *testing.T) {
	env := newTestEnv(t)
	if text, _ := env.runCallback(callbackData(cbEmail, testID(1)))[0].Body["text"].(string); text != "Mail not found" {
		t.Fatalf("unexpected missing mail answer: %q", text)
	}
	env.tg.reset()
	if text, _ := env.runCallback(callbackData(cbInvoice, testID(3)))[0].Body["text"].(string); text != "Invoice not found" {
		t.Fatalf("unexpected missing invoice answer: %q", text)
	}
	env.tg.reset()
	env.store.assetInvoices = func(context.Context, string) ([]core.Invoice, error) { return nil, nil }
	if text, _ := env.runCallback(callbackData(cbAssetHistory, testID(4)))[0].Body["text"].(string); text != "No invoice history" {
		t.Fatalf("unexpected empty history answer: %q", text)
	}
}

func TestBotEmailUsesDynamicTimezoneSetting(t *testing.T) {
	env := newTestEnv(t)
	if err := env.store.SetSetting(context.Background(), settingTimezone, "UTC"); err != nil {
		t.Fatal(err)
	}
	email := sampleEmail()
	env.store.getEmail = func(context.Context, string) (core.NormalizedEmail, error) { return email, nil }
	env.store.getAccount = func(context.Context, string) (core.MailAccount, error) { return sampleAccount(), nil }
	env.runCallback(callbackData(cbEmail, email.InternalEmailID))
	text := sentText(env.tg.callsFor("sendMessage"))
	if !strings.Contains(text, "2026-11-15 10:30 UTC +00:00") {
		t.Fatalf("dynamic timezone not applied to mail time: %s", text)
	}
}

func TestBotInvoiceUsesDynamicTimezoneSetting(t *testing.T) {
	env := newTestEnv(t)
	if err := env.store.SetSetting(context.Background(), settingTimezone, "UTC"); err != nil {
		t.Fatal(err)
	}
	invoice := sampleInvoice()
	confirmed := time.Date(2026, 11, 15, 10, 30, 0, 0, time.UTC)
	invoice.ConfirmedAt = &confirmed
	invoice.Status = core.PaidConfirmed
	env.store.getInvoice = func(context.Context, string) (core.Invoice, error) { return invoice, nil }
	env.store.getAccount = func(context.Context, string) (core.MailAccount, error) { return sampleAccount(), nil }
	env.runCallback(callbackData(cbInvoice, invoice.ID))
	text := sentText(env.tg.callsFor("sendMessage"))
	if !strings.Contains(text, "Confirmed at: 2026-11-15 10:30 UTC +00:00") {
		t.Fatalf("dynamic timezone not applied to confirmed time: %s", text)
	}
}

func TestCommandUsageMessages(t *testing.T) {
	env := newTestEnv(t)
	cases := []struct {
		command string
		want    string
	}{
		{"/add", "Usage: /add"},
		{"/remove", "Usage: /remove"},
		{"/search", "Usage: /search"},
		{"/invoices bogus", "Usage: /invoices"},
		{"/settings folder", "Usage: /settings folder"},
		{"/nonsense", "Unknown command"},
	}
	for _, tc := range cases {
		env.tg.reset()
		text := sentText(env.runMessage(tc.command))
		if !strings.Contains(text, tc.want) {
			t.Fatalf("%q reply = %q, want %q", tc.command, text, tc.want)
		}
	}
}
