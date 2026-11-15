package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"email-invoice-guard/internal/core"
)

type deliveryRecorder struct {
	mu            sync.Mutex
	recorded      []core.TelegramMessage
	completed     []core.TelegramMessage
	completeID    string
	failed        []string
	failedUnknown []bool
	skipped       []string
}

func (r *deliveryRecorder) install(store *fakeStore) {
	store.recordTelegramMessage = func(_ context.Context, m core.TelegramMessage) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.recorded = append(r.recorded, m)
		return nil
	}
	store.completeDelivery = func(_ context.Context, id string, messages []core.TelegramMessage, _ time.Time) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.completeID = id
		r.completed = append(r.completed, messages...)
		return nil
	}
	store.failDelivery = func(_ context.Context, _ string, _ time.Time, unknown bool, reason string) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.failed = append(r.failed, reason)
		r.failedUnknown = append(r.failedUnknown, unknown)
		return nil
	}
	store.skipDelivery = func(_ context.Context, id string, _ time.Time) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.skipped = append(r.skipped, id)
		return nil
	}
}

func (r *deliveryRecorder) snapshot() (recorded, completed []core.TelegramMessage, failed []string, unknown []bool, skipped []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]core.TelegramMessage(nil), r.recorded...),
		append([]core.TelegramMessage(nil), r.completed...),
		append([]string(nil), r.failed...),
		append([]bool(nil), r.failedUnknown...),
		append([]string(nil), r.skipped...)
}

func (e *testEnv) emailDelivery(email core.NormalizedEmail) *core.Delivery {
	account := sampleAccount()
	e.store.getEmail = func(context.Context, string) (core.NormalizedEmail, error) { return email, nil }
	e.store.getAccount = func(context.Context, string) (core.MailAccount, error) { return account, nil }
	delivery := &core.Delivery{
		ID:      testID(5),
		Payload: core.DeliveryPayload{Kind: "email", EmailID: email.InternalEmailID},
	}
	e.store.claimDelivery = func(context.Context, time.Time, time.Duration) (*core.Delivery, error) {
		return delivery, nil
	}
	return delivery
}

func (e *testEnv) invoiceDelivery(invoice core.Invoice, payload core.DeliveryPayload) *core.Delivery {
	account := sampleAccount()
	e.store.getInvoice = func(context.Context, string) (core.Invoice, error) { return invoice, nil }
	e.store.getAccount = func(context.Context, string) (core.MailAccount, error) { return account, nil }
	payload.InvoiceID = invoice.ID
	delivery := &core.Delivery{ID: testID(6), Payload: payload}
	e.store.claimDelivery = func(context.Context, time.Time, time.Duration) (*core.Delivery, error) {
		return delivery, nil
	}
	return delivery
}

func TestDeliverOnceEmailSuccess(t *testing.T) {
	env := newTestEnv(t)
	rec := &deliveryRecorder{}
	rec.install(env.store)
	email := sampleEmail()
	delivery := env.emailDelivery(email)

	handled, err := env.bot.DeliverOnce(context.Background())
	if err != nil || !handled {
		t.Fatalf("DeliverOnce = %v, %v; want true, nil", handled, err)
	}
	recorded, completed, failed, _, skipped := rec.snapshot()
	if len(failed) != 0 || len(skipped) != 0 {
		t.Fatalf("unexpected failure/skip: %v %v", failed, skipped)
	}
	if len(recorded) != 1 || recorded[0].Part != 0 || recorded[0].OutboxID != delivery.ID {
		t.Fatalf("unexpected recorded messages: %+v", recorded)
	}
	if recorded[0].MessageID == 0 || recorded[0].ChatID != 42 {
		t.Fatalf("recorded message lacks Telegram ids: %+v", recorded[0])
	}
	if len(completed) != 1 || completed[0].ID != recorded[0].ID {
		t.Fatalf("CompleteDelivery did not receive the part id: %+v", completed)
	}
	calls := env.tg.callsFor("sendMessage")
	if len(calls) != 1 {
		t.Fatalf("sendMessage calls = %d, want 1", len(calls))
	}
	if text, _ := calls[0].Body["text"].(string); !strings.Contains(text, "your invoice is attached") {
		t.Fatalf("delivered text missing body: %q", text)
	}
}

func TestDeliverOnceSkipsSuccessfulParts(t *testing.T) {
	env := newTestEnv(t)
	rec := &deliveryRecorder{}
	rec.install(env.store)
	email := sampleEmail()
	email.TextBody = strings.Repeat("body line\n", 900)
	delivery := env.emailDelivery(email)
	expected := FormatEmail(email, sampleAccount())
	if len(expected) < 2 {
		t.Fatalf("test requires multiple parts, got %d", len(expected))
	}
	env.store.deliveryMessages = func(context.Context, string) ([]core.TelegramMessage, error) {
		return []core.TelegramMessage{{ID: testID(8), OutboxID: delivery.ID, ChatID: 42, MessageID: 77, Part: 0}}, nil
	}

	if _, err := env.bot.DeliverOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, want := env.tg.count("sendMessage"), len(expected)-1; got != want {
		t.Fatalf("sendMessage calls = %d, want %d (part 0 skipped)", got, want)
	}
	recorded, completed, _, _, _ := rec.snapshot()
	if len(recorded) != len(expected)-1 {
		t.Fatalf("recorded %d new parts, want %d", len(recorded), len(expected)-1)
	}
	for _, m := range recorded {
		if m.Part == 0 {
			t.Fatal("re-sent an already delivered part")
		}
	}
	if len(completed) != len(expected) {
		t.Fatalf("CompleteDelivery got %d parts, want %d", len(completed), len(expected))
	}
}

func TestDeliverOnceRetryAfterPersistsKnownFailure(t *testing.T) {
	env := newTestEnv(t)
	rec := &deliveryRecorder{}
	rec.install(env.store)
	env.emailDelivery(sampleEmail())
	env.tg.setHandler("sendMessage", func(context.Context, map[string]any) (any, *tgFailure) {
		return nil, &tgFailure{code: 429, description: "Too Many Requests: retry after 3", retryAfter: 3}
	})

	if _, err := env.bot.DeliverOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	recorded, completed, failed, unknown, _ := rec.snapshot()
	if len(recorded) != 0 || len(completed) != 0 {
		t.Fatalf("429 must not record or complete: %+v %+v", recorded, completed)
	}
	if len(failed) != 1 || unknown[0] {
		t.Fatalf("429 should be a known failure: %v %v", failed, unknown)
	}
	if !strings.Contains(failed[0], "429") {
		t.Fatalf("failure reason lacks status: %q", failed[0])
	}
	if delay := env.bot.takeDelay(); delay != 3*time.Second {
		t.Fatalf("retry_after not honored: %s", delay)
	}
}

func TestDeliverOnceHTTPFailureIsKnown(t *testing.T) {
	env := newTestEnv(t)
	rec := &deliveryRecorder{}
	rec.install(env.store)
	env.emailDelivery(sampleEmail())
	env.tg.setHandler("sendMessage", func(context.Context, map[string]any) (any, *tgFailure) {
		return nil, &tgFailure{code: 500, description: "Internal Server Error"}
	})
	if _, err := env.bot.DeliverOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, _, failed, unknown, _ := rec.snapshot()
	if len(failed) != 1 || unknown[0] {
		t.Fatalf("HTTP failure should be known: %v %v", failed, unknown)
	}
}

func TestDeliverOnceTransportFailureIsUnknown(t *testing.T) {
	env := newTestEnv(t)
	rec := &deliveryRecorder{}
	rec.install(env.store)
	env.emailDelivery(sampleEmail())
	env.bot.api.http = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection reset by peer")
	})}
	if _, err := env.bot.DeliverOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, _, failed, unknown, _ := rec.snapshot()
	if len(failed) != 1 || !unknown[0] {
		t.Fatalf("transport failure must be UNKNOWN: %v %v", failed, unknown)
	}
}

func TestDeliverOncePartialFailureKeepsSuccessfulParts(t *testing.T) {
	env := newTestEnv(t)
	rec := &deliveryRecorder{}
	rec.install(env.store)
	email := sampleEmail()
	email.TextBody = strings.Repeat("body line\n", 900)
	env.emailDelivery(email)
	var calls int
	var mu sync.Mutex
	env.tg.setHandler("sendMessage", func(context.Context, map[string]any) (any, *tgFailure) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 2 {
			return nil, &tgFailure{code: 500, description: "Internal Server Error"}
		}
		return map[string]any{"message_id": 500 + n, "chat": map[string]any{"id": 42}}, nil
	})

	if _, err := env.bot.DeliverOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	recorded, completed, failed, _, _ := rec.snapshot()
	if len(recorded) != 1 || recorded[0].Part != 0 {
		t.Fatalf("first acknowledged part not persisted: %+v", recorded)
	}
	if len(failed) != 1 {
		t.Fatalf("expected persisted retry, got %v", failed)
	}
	if len(completed) != 0 {
		t.Fatalf("partial delivery must not complete: %+v", completed)
	}
}

func TestDeliverOnceStaleReminderSkipped(t *testing.T) {
	env := newTestEnv(t)
	rec := &deliveryRecorder{}
	rec.install(env.store)
	invoice := sampleInvoice()
	invoice.Status = core.PaidConfirmed
	env.invoiceDelivery(invoice, core.DeliveryPayload{Kind: "reminder", ReminderID: testID(7)})

	if _, err := env.bot.DeliverOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, _, _, _, skipped := rec.snapshot()
	if len(skipped) != 1 {
		t.Fatalf("stale reminder not skipped: %v", skipped)
	}
	if env.tg.count("sendMessage") != 0 {
		t.Fatal("stale reminder was delivered")
	}
}

func TestDeliverOnceInvoiceEnhancedOnly(t *testing.T) {
	env := newTestEnv(t)
	rec := &deliveryRecorder{}
	rec.install(env.store)
	invoice := sampleInvoice()
	env.invoiceDelivery(invoice, core.DeliveryPayload{Kind: "invoice", EmailID: testID(1)})

	if _, err := env.bot.DeliverOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	expected := FormatInvoice(invoice, sampleAccount())
	calls := env.tg.callsFor("sendMessage")
	if len(calls) != len(expected) {
		t.Fatalf("sendMessage calls = %d, want %d (enhanced invoice only)", len(calls), len(expected))
	}
	all := ""
	for _, call := range calls {
		text, _ := call.Body["text"].(string)
		all += text
	}
	if !strings.Contains(all, "INV-2026-001") || !strings.Contains(all, "VPS-2C4G") {
		t.Fatalf("enhanced invoice content missing: %s", all)
	}
	if strings.Contains(all, "your invoice is attached") {
		t.Fatal("ordinary email duplicate was sent for an invoice delivery")
	}
	recorded, completed, _, _, _ := rec.snapshot()
	if len(recorded) != len(expected) || len(completed) != len(expected) {
		t.Fatalf("part ids not retained: recorded=%d completed=%d want=%d", len(recorded), len(completed), len(expected))
	}
	for _, m := range recorded {
		if m.InvoiceID != invoice.ID {
			t.Fatalf("recorded part missing invoice id: %+v", m)
		}
	}
	last := calls[len(calls)-1]
	markup, ok := last.Body["reply_markup"].(map[string]any)
	if !ok {
		t.Fatalf("invoice keyboard missing on last part: %+v", last.Body)
	}
	rows, _ := markup["inline_keyboard"].([]any)
	if len(rows) == 0 {
		t.Fatal("invoice keyboard has no buttons")
	}
}

func TestDeliverOnceMissingSourceSkips(t *testing.T) {
	env := newTestEnv(t)
	rec := &deliveryRecorder{}
	rec.install(env.store)
	env.store.getEmail = func(context.Context, string) (core.NormalizedEmail, error) {
		return core.NormalizedEmail{}, core.ErrNotFound
	}
	env.store.claimDelivery = func(context.Context, time.Time, time.Duration) (*core.Delivery, error) {
		return &core.Delivery{ID: testID(5), Payload: core.DeliveryPayload{Kind: "email", EmailID: testID(1)}}, nil
	}
	if _, err := env.bot.DeliverOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, _, _, _, skipped := rec.snapshot()
	if len(skipped) != 1 {
		t.Fatalf("missing source not skipped: %v", skipped)
	}
}

func TestDeliverOnceEmptyQueue(t *testing.T) {
	env := newTestEnv(t)
	handled, err := env.bot.DeliverOnce(context.Background())
	if err != nil || handled {
		t.Fatalf("empty queue = %v, %v; want false, nil", handled, err)
	}
}

func TestSendMessageWithoutMessageIDIsUnknown(t *testing.T) {
	env := newTestEnv(t)
	rec := &deliveryRecorder{}
	rec.install(env.store)
	env.emailDelivery(sampleEmail())
	env.tg.setHandler("sendMessage", func(context.Context, map[string]any) (any, *tgFailure) {
		return rawResponse(`{"ok":true,"result":{}}`), nil
	})
	if _, err := env.bot.DeliverOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	recorded, _, failed, unknown, _ := rec.snapshot()
	if len(recorded) != 0 {
		t.Fatalf("unacknowledged part must not be recorded: %+v", recorded)
	}
	if len(failed) != 1 || !unknown[0] {
		t.Fatalf("missing message id must be UNKNOWN: %v %v", failed, unknown)
	}
}

func TestSendMessageCorruptResponseIsUnknown(t *testing.T) {
	env := newTestEnv(t)
	rec := &deliveryRecorder{}
	rec.install(env.store)
	env.emailDelivery(sampleEmail())
	env.tg.setHandler("sendMessage", func(context.Context, map[string]any) (any, *tgFailure) {
		return rawResponse(`<html>gateway error</html>`), nil
	})
	if _, err := env.bot.DeliverOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, _, failed, unknown, _ := rec.snapshot()
	if len(failed) != 1 || !unknown[0] {
		t.Fatalf("corrupt response must be UNKNOWN: %v %v", failed, unknown)
	}
}

func TestAPIErrorDescriptionRedacted(t *testing.T) {
	env := newTestEnv(t)
	rec := &deliveryRecorder{}
	rec.install(env.store)
	env.emailDelivery(sampleEmail())
	env.tg.setHandler("sendMessage", func(context.Context, map[string]any) (any, *tgFailure) {
		return nil, &tgFailure{code: 400, description: "bad request for token " + env.cfg.Telegram.Token}
	})
	if _, err := env.bot.DeliverOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, _, failed, _, _ := rec.snapshot()
	if len(failed) != 1 {
		t.Fatalf("expected failure, got %v", failed)
	}
	if strings.Contains(failed[0], env.cfg.Telegram.Token) {
		t.Fatalf("token leaked into persisted failure: %q", failed[0])
	}
	if strings.Contains(env.logs.String(), env.cfg.Telegram.Token) {
		t.Fatal("token leaked into logs")
	}
}

func TestDeliveryRespectsRateInterval(t *testing.T) {
	env := newTestEnv(t)
	env.cfg.Telegram.RateInterval = 25 * time.Millisecond
	env.rebuild()
	rec := &deliveryRecorder{}
	rec.install(env.store)
	email := sampleEmail()
	email.TextBody = strings.Repeat("body line\n", 900)
	env.emailDelivery(email)

	var mu sync.Mutex
	var stamps []time.Time
	env.tg.setHandler("sendMessage", func(context.Context, map[string]any) (any, *tgFailure) {
		mu.Lock()
		stamps = append(stamps, time.Now())
		n := len(stamps)
		mu.Unlock()
		return map[string]any{"message_id": 900 + n, "chat": map[string]any{"id": 42}}, nil
	})
	if _, err := env.bot.DeliverOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(stamps) < 2 {
		t.Fatalf("expected multiple sends, got %d", len(stamps))
	}
	for i := 1; i < len(stamps); i++ {
		if gap := stamps[i].Sub(stamps[i-1]); gap < 15*time.Millisecond {
			t.Fatalf("send %d followed send %d after %s, want >= 15ms", i+1, i, gap)
		}
	}
}

func TestDeliverOnceReminderRecomputesDaysAtRenderTime(t *testing.T) {
	cases := []struct {
		name string
		due  core.Date
		want string
	}{
		{name: "upcoming", due: "2026-11-20", want: "due in 5 day(s)"},
		{name: "today", due: "2026-11-15", want: "due today"},
		{name: "overdue", due: "2026-11-10", want: "overdue by 5 day(s)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t)
			rec := &deliveryRecorder{}
			rec.install(env.store)
			env.bot.now = func() time.Time { return time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC) }
			invoice := sampleInvoice()
			invoice.DueDate = tc.due
			env.invoiceDelivery(invoice, core.DeliveryPayload{
				Kind:       "reminder",
				ReminderID: testID(7),
				Text:       "stale scheduling text",
			})
			if _, err := env.bot.DeliverOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			all := ""
			for _, call := range env.tg.callsFor("sendMessage") {
				text, _ := call.Body["text"].(string)
				all += text
			}
			if !strings.Contains(all, tc.want) {
				t.Fatalf("reminder missing %q:\n%s", tc.want, all)
			}
			if strings.Contains(all, "stale scheduling text") {
				t.Fatalf("reminder trusted stale payload text:\n%s", all)
			}
			_, _, _, _, skipped := rec.snapshot()
			if len(skipped) != 0 {
				t.Fatalf("pending reminder was skipped: %v", skipped)
			}
		})
	}
}

type timeoutError struct{}

func (timeoutError) Error() string { return "i/o timeout" }
func (timeoutError) Timeout() bool { return true }

func TestDeliverOnceTransportTimeoutIsUnknown(t *testing.T) {
	env := newTestEnv(t)
	rec := &deliveryRecorder{}
	rec.install(env.store)
	env.emailDelivery(sampleEmail())
	env.bot.api.http = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, timeoutError{}
	})}
	if _, err := env.bot.DeliverOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, _, failed, unknown, _ := rec.snapshot()
	if len(failed) != 1 || !unknown[0] {
		t.Fatalf("timeout must be UNKNOWN: %v %v", failed, unknown)
	}
	if strings.Contains(env.logs.String(), env.cfg.Telegram.Token) {
		t.Fatal("token leaked into logs")
	}
}

func TestReminderRecheckedAfterRateLimitWait(t *testing.T) {
	env := newTestEnv(t)
	rec := &deliveryRecorder{}
	rec.install(env.store)
	invoice := sampleInvoice()
	var mu sync.Mutex
	status := core.PendingConfirmation
	env.store.getInvoice = func(context.Context, string) (core.Invoice, error) {
		mu.Lock()
		defer mu.Unlock()
		out := invoice
		out.Status = status
		return out, nil
	}
	env.store.getAccount = func(context.Context, string) (core.MailAccount, error) { return sampleAccount(), nil }
	env.store.claimDelivery = func(context.Context, time.Time, time.Duration) (*core.Delivery, error) {
		return &core.Delivery{ID: testID(6), Payload: core.DeliveryPayload{
			Kind: "reminder", InvoiceID: invoice.ID, ReminderID: testID(7),
		}}, nil
	}
	env.cfg.Telegram.RateInterval = 10 * time.Millisecond
	env.rebuild()
	// Force the first send to wait on the rate limiter.
	env.bot.limiter.lastGlobal = time.Now()
	started := make(chan struct{})
	release := make(chan struct{})
	env.bot.limiter.sleep = func(context.Context, time.Duration) error {
		close(started)
		<-release
		return nil
	}

	done := make(chan struct{})
	go func() {
		_, _ = env.bot.DeliverOnce(context.Background())
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("reminder did not reach the rate-limit wait")
	}
	// The user confirms payment while the reminder waits for its send slot.
	mu.Lock()
	status = core.PaidConfirmed
	mu.Unlock()
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("DeliverOnce did not finish")
	}

	if got := env.tg.count("sendMessage"); got != 0 {
		t.Fatalf("stale reminder was sent after confirmation during the limiter wait: %d sends", got)
	}
	_, _, _, _, skipped := rec.snapshot()
	if len(skipped) != 1 {
		t.Fatalf("reminder was not skipped: %v", skipped)
	}
}

func TestDeliverOnceTransientReadFailureRetries(t *testing.T) {
	cases := []struct {
		name    string
		payload core.DeliveryPayload
		setup   func(*testEnv)
		reason  string
	}{
		{
			name:    "invoice read",
			payload: core.DeliveryPayload{Kind: "invoice", InvoiceID: testID(3)},
			setup: func(env *testEnv) {
				env.store.getInvoice = func(context.Context, string) (core.Invoice, error) {
					return core.Invoice{}, errBoom
				}
			},
			reason: "load invoice",
		},
		{
			name:    "email read",
			payload: core.DeliveryPayload{Kind: "email", EmailID: testID(1)},
			setup: func(env *testEnv) {
				env.store.getEmail = func(context.Context, string) (core.NormalizedEmail, error) {
					return core.NormalizedEmail{}, errBoom
				}
			},
			reason: "load email",
		},
		{
			name:    "account read",
			payload: core.DeliveryPayload{Kind: "email", EmailID: testID(1)},
			setup: func(env *testEnv) {
				email := sampleEmail()
				env.store.getEmail = func(context.Context, string) (core.NormalizedEmail, error) { return email, nil }
				env.store.getAccount = func(context.Context, string) (core.MailAccount, error) {
					return core.MailAccount{}, errBoom
				}
			},
			reason: "load account",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t)
			rec := &deliveryRecorder{}
			rec.install(env.store)
			tc.setup(env)
			env.store.claimDelivery = func(context.Context, time.Time, time.Duration) (*core.Delivery, error) {
				return &core.Delivery{ID: testID(6), Payload: tc.payload}, nil
			}
			if _, err := env.bot.DeliverOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			recorded, completed, failed, unknown, skipped := rec.snapshot()
			if len(skipped) != 0 {
				t.Fatalf("transient read failure was permanently skipped: %v", skipped)
			}
			if len(failed) != 1 || unknown[0] {
				t.Fatalf("transient read failure should be a known retry: %v %v", failed, unknown)
			}
			if !strings.Contains(failed[0], tc.reason) {
				t.Fatalf("failure reason %q missing %q", failed[0], tc.reason)
			}
			if len(recorded) != 0 || len(completed) != 0 || env.tg.count("sendMessage") != 0 {
				t.Fatal("transient read failure still attempted a send")
			}
		})
	}
}

func TestDeliverOnceMissingInvoiceSkips(t *testing.T) {
	env := newTestEnv(t)
	rec := &deliveryRecorder{}
	rec.install(env.store)
	env.store.getInvoice = func(context.Context, string) (core.Invoice, error) {
		return core.Invoice{}, core.ErrNotFound
	}
	env.store.claimDelivery = func(context.Context, time.Time, time.Duration) (*core.Delivery, error) {
		return &core.Delivery{ID: testID(6), Payload: core.DeliveryPayload{Kind: "invoice", InvoiceID: testID(3)}}, nil
	}
	if _, err := env.bot.DeliverOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, _, failed, _, skipped := rec.snapshot()
	if len(failed) != 0 || len(skipped) != 1 {
		t.Fatalf("missing invoice should be skipped: failed=%v skipped=%v", failed, skipped)
	}
}

func TestInvoiceKeyboardPrefersPayloadEmail(t *testing.T) {
	env := newTestEnv(t)
	rec := &deliveryRecorder{}
	rec.install(env.store)
	invoice := sampleInvoice()
	firstEmailID := invoice.SourceEmailID
	secondEmailID := testID(9)
	second := sampleEmail()
	second.InternalEmailID = secondEmailID
	second.TextBody = "second copy full body"

	env.store.getInvoice = func(context.Context, string) (core.Invoice, error) { return invoice, nil }
	env.store.getAccount = func(context.Context, string) (core.MailAccount, error) { return sampleAccount(), nil }
	env.store.getEmail = func(_ context.Context, id string) (core.NormalizedEmail, error) {
		if id == secondEmailID {
			return second, nil
		}
		return core.NormalizedEmail{}, core.ErrNotFound
	}
	env.store.claimDelivery = func(context.Context, time.Time, time.Duration) (*core.Delivery, error) {
		return &core.Delivery{ID: testID(6), Payload: core.DeliveryPayload{
			Kind: "invoice", InvoiceID: invoice.ID, EmailID: secondEmailID,
		}}, nil
	}

	if _, err := env.bot.DeliverOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := env.tg.callsFor("sendMessage")
	if len(calls) == 0 {
		t.Fatal("invoice notification was not sent")
	}
	markup, ok := calls[len(calls)-1].Body["reply_markup"].(map[string]any)
	if !ok {
		t.Fatal("invoice keyboard missing")
	}
	raw, _ := json.Marshal(markup)
	serialized := string(raw)
	if !strings.Contains(serialized, callbackData(cbEmail, secondEmailID)) {
		t.Fatalf("keyboard does not point at the triggering mail: %s", serialized)
	}
	if strings.Contains(serialized, callbackData(cbEmail, firstEmailID)) {
		t.Fatalf("keyboard still points at the first invoice mail: %s", serialized)
	}
	if invoice.SourceEmailID != firstEmailID {
		t.Fatal("persisted invoice was mutated")
	}

	env.tg.reset()
	env.runCallback(callbackData(cbEmail, secondEmailID))
	text := sentText(env.tg.callsFor("sendMessage"))
	if !strings.Contains(text, "second copy full body") {
		t.Fatalf("second mail full text not reachable: %q", text)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
