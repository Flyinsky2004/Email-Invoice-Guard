package telegram

import (
	"context"
	"strings"
	"testing"
	"time"

	"email-invoice-guard/internal/core"
)

// manualDelivery wires one manual outbox job into the fake store.
func (e *testEnv) manualDelivery(m core.ManualReminderService, plan core.ManualPlan, occurrences []core.ManualOccurrence) *core.Delivery {
	e.withManual(m)
	ids := make([]string, len(occurrences))
	for i := range occurrences {
		ids[i] = occurrences[i].ID
	}
	delivery := &core.Delivery{
		ID: testID(5),
		Payload: core.DeliveryPayload{
			Kind:                "manual",
			ManualPlanID:        plan.ID,
			ManualOccurrenceIDs: ids,
		},
	}
	e.store.claimDelivery = func(context.Context, time.Time, time.Duration) (*core.Delivery, error) {
		return delivery, nil
	}
	return delivery
}

func occurrenceService(plan core.ManualPlan, occurrences []core.ManualOccurrence) *fakeManual {
	m := newFakeManual()
	m.getPlan = func(context.Context, string) (core.ManualPlan, error) { return plan, nil }
	m.getOccurrence = func(_ context.Context, id string) (core.ManualOccurrence, error) {
		for _, occ := range occurrences {
			if occ.ID == id {
				return occ, nil
			}
		}
		return core.ManualOccurrence{}, core.ErrNotFound
	}
	return m
}

func TestManualSummaryRendersMultipleOccurrences(t *testing.T) {
	env := newTestEnv(t)
	rec := &deliveryRecorder{}
	rec.install(env.store)
	plan := sampleManualPlan()
	pending := sampleManualOccurrence(core.ManualOccurrencePending)
	paid := sampleManualOccurrence(core.ManualOccurrencePaid)
	paid.ID = testID(22)
	paid.DueDate = core.Date("2026-10-15")
	m := occurrenceService(plan, []core.ManualOccurrence{pending, paid})
	delivery := env.manualDelivery(m, plan, []core.ManualOccurrence{pending, paid})

	handled, err := env.bot.DeliverOnce(context.Background())
	if err != nil || !handled {
		t.Fatalf("DeliverOnce = %v, %v", handled, err)
	}
	calls := env.tg.callsFor("sendMessage")
	if len(calls) != 1 {
		t.Fatalf("sendMessage calls = %d, want 1", len(calls))
	}
	text := sentText(calls)
	for _, want := range []string{
		"Manual reminder", "VPS renewal", "Example Host", "18.00 USD",
		"2026-11-15", "2026-10-15", "PENDING", "PAID", "billing.example.test",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("summary missing %q: %q", want, text)
		}
	}
	assertValidParts(t, []string{text})
	if !hasCallbackPrefix(calls, cbManualPaid+":") || !hasCallbackPrefix(calls, cbManualUndo+":") {
		t.Fatal("summary keyboard missing paid/undo actions")
	}
	if !hasCallbackPrefix(calls, cbManualView+":") || !hasCallbackPrefix(calls, cbManualHistory+":") {
		t.Fatal("summary keyboard missing plan/history actions")
	}
	_, completed, failed, _, skipped := rec.snapshot()
	if len(completed) != 1 || len(failed) != 0 || len(skipped) != 0 {
		t.Fatalf("delivery outcome: completed=%d failed=%v skipped=%v", len(completed), failed, skipped)
	}
	if completed[0].OutboxID != delivery.ID {
		t.Fatalf("completed outbox = %q, want %q", completed[0].OutboxID, delivery.ID)
	}
}

func TestManualSummaryEscapesHostileContent(t *testing.T) {
	env := newTestEnv(t)
	rec := &deliveryRecorder{}
	rec.install(env.store)
	plan := sampleManualPlan()
	plan.Title = `A&B<"quote">`
	plan.ProviderName = `<script>alert(1)</script>`
	plan.PaymentURL = "https://billing.example.test/pay?a=1&b=2"
	occ := sampleManualOccurrence(core.ManualOccurrencePending)
	m := occurrenceService(plan, []core.ManualOccurrence{occ})
	env.manualDelivery(m, plan, []core.ManualOccurrence{occ})

	if _, err := env.bot.DeliverOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	text := sentText(env.tg.callsFor("sendMessage"))
	if !strings.Contains(text, "A&amp;B&lt;&#34;quote&#34;&gt;") {
		t.Fatalf("title was not escaped: %q", text)
	}
	if !strings.Contains(text, "&lt;script&gt;") || strings.Contains(text, "<script>") {
		t.Fatalf("provider was not escaped: %q", text)
	}
	if !strings.Contains(text, "a=1&amp;b=2") {
		t.Fatalf("payment URL was not escaped: %q", text)
	}
	assertValidParts(t, []string{text})
}

func TestManualSummaryChunksLongUnicode(t *testing.T) {
	env := newTestEnv(t)
	rec := &deliveryRecorder{}
	rec.install(env.store)
	plan := sampleManualPlan()
	plan.Title = strings.Repeat("あ", 5000)
	occ := sampleManualOccurrence(core.ManualOccurrencePending)
	m := occurrenceService(plan, []core.ManualOccurrence{occ})
	env.manualDelivery(m, plan, []core.ManualOccurrence{occ})

	if _, err := env.bot.DeliverOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := env.tg.callsFor("sendMessage")
	if len(calls) < 2 {
		t.Fatalf("long summary was not chunked: %d part(s)", len(calls))
	}
	var joinedText strings.Builder
	for _, call := range calls {
		text, _ := call.Body["text"].(string)
		if units := utf16Len(text); units > maxMessageUnits {
			t.Fatalf("part has %d UTF-16 units", units)
		}
		joinedText.WriteString(text)
	}
	if got := strings.Count(joinedText.String(), "あ"); got != 5000 {
		t.Fatalf("content truncated: %d of 5000 runes", got)
	}
	assertValidParts(t, sentTexts(calls))
}

func TestManualSummaryOmitsUntrustedURL(t *testing.T) {
	cases := []string{
		"http://billing.example.test/pay",
		"https://user:" + "pass" + "@billing.example.test/pay",
		"https://billing.example.test:8443/pay",
		"https://billing.example.test/pay#fragment",
		"",
	}
	for _, raw := range cases {
		env := newTestEnv(t)
		rec := &deliveryRecorder{}
		rec.install(env.store)
		plan := sampleManualPlan()
		plan.PaymentURL = raw
		occ := sampleManualOccurrence(core.ManualOccurrencePending)
		m := occurrenceService(plan, []core.ManualOccurrence{occ})
		env.manualDelivery(m, plan, []core.ManualOccurrence{occ})
		if _, err := env.bot.DeliverOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		text := sentText(env.tg.callsFor("sendMessage"))
		if raw != "" && strings.Contains(text, raw) {
			t.Fatalf("untrusted URL %q rendered: %q", raw, text)
		}
		if strings.Contains(text, "Payment URL:") {
			t.Fatalf("untrusted URL %q produced a payment line: %q", raw, text)
		}
	}
}

func TestManualDeliverySkipsWithoutService(t *testing.T) {
	env := newTestEnv(t)
	rec := &deliveryRecorder{}
	rec.install(env.store)
	plan := sampleManualPlan()
	occ := sampleManualOccurrence(core.ManualOccurrencePending)
	delivery := &core.Delivery{
		ID: testID(5),
		Payload: core.DeliveryPayload{
			Kind:                "manual",
			ManualPlanID:        plan.ID,
			ManualOccurrenceIDs: []string{occ.ID},
		},
	}
	env.store.claimDelivery = func(context.Context, time.Time, time.Duration) (*core.Delivery, error) {
		return delivery, nil
	}
	if _, err := env.bot.DeliverOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, _, _, _, skipped := rec.snapshot()
	if len(skipped) != 1 || skipped[0] != delivery.ID {
		t.Fatalf("manual delivery without service was not skipped: %v", skipped)
	}
	if env.tg.count("sendMessage") != 0 {
		t.Fatal("manual delivery without service sent a message")
	}
}

func TestManualDeliveryMissingPlanSkips(t *testing.T) {
	env := newTestEnv(t)
	rec := &deliveryRecorder{}
	rec.install(env.store)
	plan := sampleManualPlan()
	occ := sampleManualOccurrence(core.ManualOccurrencePending)
	m := newFakeManual()
	m.getPlan = func(context.Context, string) (core.ManualPlan, error) { return core.ManualPlan{}, core.ErrNotFound }
	env.manualDelivery(m, plan, []core.ManualOccurrence{occ})
	if _, err := env.bot.DeliverOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, _, _, _, skipped := rec.snapshot()
	if len(skipped) != 1 {
		t.Fatalf("missing plan was not skipped: %v", skipped)
	}
}

func TestManualDeliveryTransientReadRetries(t *testing.T) {
	env := newTestEnv(t)
	rec := &deliveryRecorder{}
	rec.install(env.store)
	plan := sampleManualPlan()
	occ := sampleManualOccurrence(core.ManualOccurrencePending)
	m := newFakeManual()
	m.getPlan = func(context.Context, string) (core.ManualPlan, error) { return core.ManualPlan{}, errBoom }
	env.manualDelivery(m, plan, []core.ManualOccurrence{occ})
	if _, err := env.bot.DeliverOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, _, failed, unknown, _ := rec.snapshot()
	if len(failed) != 1 || !strings.Contains(failed[0], "load manual plan") {
		t.Fatalf("transient read was not retried: %v", failed)
	}
	if len(unknown) != 1 || unknown[0] {
		t.Fatalf("transient read marked unknown: %v", unknown)
	}
}

func TestManualDeliveryMissingOccurrenceSkips(t *testing.T) {
	env := newTestEnv(t)
	rec := &deliveryRecorder{}
	rec.install(env.store)
	plan := sampleManualPlan()
	occ := sampleManualOccurrence(core.ManualOccurrencePending)
	m := newFakeManual()
	m.getPlan = func(context.Context, string) (core.ManualPlan, error) { return plan, nil }
	m.getOccurrence = func(context.Context, string) (core.ManualOccurrence, error) {
		return core.ManualOccurrence{}, core.ErrNotFound
	}
	env.manualDelivery(m, plan, []core.ManualOccurrence{occ})
	if _, err := env.bot.DeliverOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, _, _, _, skipped := rec.snapshot()
	if len(skipped) != 1 {
		t.Fatalf("missing occurrence was not skipped: %v", skipped)
	}
}

func TestManualDeliveryEmptyOccurrencesSkips(t *testing.T) {
	env := newTestEnv(t)
	rec := &deliveryRecorder{}
	rec.install(env.store)
	plan := sampleManualPlan()
	m := newFakeManual()
	m.getPlan = func(context.Context, string) (core.ManualPlan, error) { return plan, nil }
	env.manualDelivery(m, plan, nil)
	if _, err := env.bot.DeliverOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, _, _, _, skipped := rec.snapshot()
	if len(skipped) != 1 {
		t.Fatalf("empty summary was not skipped: %v", skipped)
	}
}

func TestManualSummaryKeyboardCallbackLimit(t *testing.T) {
	env := newTestEnv(t)
	rec := &deliveryRecorder{}
	rec.install(env.store)
	plan := sampleManualPlan()
	pending := sampleManualOccurrence(core.ManualOccurrencePending)
	paid := sampleManualOccurrence(core.ManualOccurrencePaid)
	paid.ID = testID(22)
	m := occurrenceService(plan, []core.ManualOccurrence{pending, paid})
	env.manualDelivery(m, plan, []core.ManualOccurrence{pending, paid})
	if _, err := env.bot.DeliverOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	call, ok := env.tg.last("sendMessage")
	if !ok {
		t.Fatal("no summary message")
	}
	values := callbackDataValues(call.Body["reply_markup"])
	if len(values) == 0 {
		t.Fatal("summary keyboard has no callbacks")
	}
	for _, data := range values {
		if len(data) > 64 {
			t.Fatalf("callback data %q is %d bytes", data, len(data))
		}
		if _, _, ok := parseCallbackData(data); !ok {
			t.Fatalf("manual callback data did not round trip: %q", data)
		}
	}
}

func sentTexts(calls []tgCall) []string {
	out := make([]string, 0, len(calls))
	for _, call := range calls {
		if text, ok := call.Body["text"].(string); ok {
			out = append(out, text)
		}
	}
	return out
}

func TestManualSummaryRechecksBeforeSend(t *testing.T) {
	env := newTestEnv(t)
	rec := &deliveryRecorder{}
	rec.install(env.store)
	plan := sampleManualPlan()
	occ := sampleManualOccurrence(core.ManualOccurrencePending)
	reads := 0
	m := newFakeManual()
	m.getPlan = func(context.Context, string) (core.ManualPlan, error) { return plan, nil }
	m.getOccurrence = func(context.Context, string) (core.ManualOccurrence, error) {
		reads++
		if reads == 1 {
			return occ, nil // render sees a pending occurrence
		}
		paid := occ
		paid.Status = core.ManualOccurrencePaid // confirmed while waiting to send
		return paid, nil
	}
	env.manualDelivery(m, plan, []core.ManualOccurrence{occ})

	handled, err := env.bot.DeliverOnce(context.Background())
	if err != nil || !handled {
		t.Fatalf("DeliverOnce = %v, %v", handled, err)
	}
	if calls := env.tg.callsFor("sendMessage"); len(calls) != 0 {
		t.Fatalf("stale summary was sent: %d calls", len(calls))
	}
	_, _, _, _, skipped := rec.snapshot()
	if len(skipped) != 1 {
		t.Fatalf("skipped deliveries = %d, want 1", len(skipped))
	}
}

func TestManualSummarySkipsWhenPlanPausedBeforeSend(t *testing.T) {
	env := newTestEnv(t)
	rec := &deliveryRecorder{}
	rec.install(env.store)
	plan := sampleManualPlan()
	occ := sampleManualOccurrence(core.ManualOccurrencePending)
	reads := 0
	m := newFakeManual()
	m.getPlan = func(context.Context, string) (core.ManualPlan, error) {
		reads++
		if reads > 1 {
			plan.Status = core.ManualPlanPaused
		}
		return plan, nil
	}
	m.getOccurrence = func(context.Context, string) (core.ManualOccurrence, error) { return occ, nil }
	env.manualDelivery(m, plan, []core.ManualOccurrence{occ})

	handled, err := env.bot.DeliverOnce(context.Background())
	if err != nil || !handled {
		t.Fatalf("DeliverOnce = %v, %v", handled, err)
	}
	if calls := env.tg.callsFor("sendMessage"); len(calls) != 0 {
		t.Fatalf("paused plan summary was sent: %d calls", len(calls))
	}
	if _, _, _, _, skipped := rec.snapshot(); len(skipped) != 1 {
		t.Fatalf("skipped deliveries = %d, want 1", len(skipped))
	}
}
