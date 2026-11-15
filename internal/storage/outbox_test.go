package storage

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"email-invoice-guard/internal/core"
)

func TestEnqueueDedupDoesNotReset(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	d := core.Delivery{DedupKey: "dedup-1", Payload: core.DeliveryPayload{Kind: "email", Text: "hello"}, AvailableAt: time.Now().Add(-time.Minute)}
	inserted, err := s.Enqueue(ctx, d)
	if err != nil || !inserted {
		t.Fatalf("first enqueue: %v %v", inserted, err)
	}
	if _, err := s.ClaimDelivery(ctx, time.Now(), time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteDelivery(ctx, mustOutboxID(t, s, "dedup-1"), nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	inserted, err = s.Enqueue(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if inserted {
		t.Fatal("enqueue reset an existing delivery")
	}
	var status string
	if err := s.DB().QueryRowContext(ctx, `SELECT status FROM delivery_outbox WHERE dedup_key = ?`, "dedup-1").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != string(core.DeliverySent) {
		t.Fatalf("status = %s, want SENT", status)
	}
}

func mustOutboxID(t *testing.T, s *Store, dedup string) string {
	t.Helper()
	var id string
	if err := s.DB().QueryRowContext(context.Background(), `SELECT id FROM delivery_outbox WHERE dedup_key = ?`, dedup).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestClaimDeliveryLeasesDueJob(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	if _, err := s.Enqueue(ctx, core.Delivery{DedupKey: "claim-1", AvailableAt: now.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimDelivery(ctx, now, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil {
		t.Fatal("no delivery claimed")
	}
	if claimed.Status != core.DeliverySending || claimed.Attempts != 1 || claimed.LeaseUntil == nil {
		t.Fatalf("claim = %+v", claimed)
	}
	if !claimed.LeaseUntil.Equal(now.Add(2 * time.Minute)) {
		t.Fatalf("lease = %v", claimed.LeaseUntil)
	}
	second, err := s.ClaimDelivery(ctx, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if second != nil {
		t.Fatalf("claimed an already leased delivery: %+v", second)
	}
}

func TestExpiredLeaseBecomesUnknownThenRetried(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	if _, err := s.Enqueue(ctx, core.Delivery{DedupKey: "lease-1", AvailableAt: now.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	first, err := s.ClaimDelivery(ctx, now, time.Second)
	if err != nil || first == nil {
		t.Fatalf("first claim: %v %v", first, err)
	}
	second, err := s.ClaimDelivery(ctx, now.Add(2*time.Second), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if second == nil {
		t.Fatal("expired lease was not retried")
	}
	if second.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", second.Attempts)
	}
	if second.ID != first.ID {
		t.Fatalf("retried a different delivery")
	}
}

func TestCompleteDeliveryUpsertsPartsAndReminder(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustAccount(t, s, "acc-out", core.Microsoft, "out@example.com")
	invoice := mustInvoice(t, s, sampleInvoice("acc-out", "INV-OUT"))
	created, err := s.CreateReminder(ctx, core.InvoiceReminder{InvoiceID: invoice.ID, ReminderDate: "2026-10-20"},
		core.Delivery{DedupKey: "rem:" + invoice.ID, Payload: core.DeliveryPayload{Kind: "reminder", InvoiceID: invoice.ID}})
	if err != nil || !created {
		t.Fatalf("reminder: %v %v", created, err)
	}
	reminders, err := s.ListReminders(ctx, invoice.ID)
	if err != nil {
		t.Fatal(err)
	}
	outboxID := reminders[0].OutboxID
	if _, err := s.ClaimDelivery(ctx, time.Now(), time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordTelegramMessage(ctx, core.TelegramMessage{OutboxID: outboxID, InvoiceID: invoice.ID, ChatID: 1, MessageID: 100, Part: 0}); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 6, 1, 2, 3, 4, time.UTC)
	parts := []core.TelegramMessage{
		{OutboxID: outboxID, InvoiceID: invoice.ID, ChatID: 1, MessageID: 100, Part: 0},
		{OutboxID: outboxID, InvoiceID: invoice.ID, ChatID: 1, MessageID: 101, Part: 1},
	}
	if err := s.CompleteDelivery(ctx, outboxID, parts, at); err != nil {
		t.Fatal(err)
	}
	// Idempotent replay with the same parts.
	if err := s.CompleteDelivery(ctx, outboxID, parts, at); err != nil {
		t.Fatalf("replay: %v", err)
	}
	delivered, err := s.DeliveryMessages(ctx, outboxID)
	if err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 2 || delivered[0].Part != 0 || delivered[1].Part != 1 {
		t.Fatalf("delivered parts = %+v", delivered)
	}
	byInvoice, err := s.ListTelegramMessages(ctx, invoice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(byInvoice) != 2 {
		t.Fatalf("invoice messages = %d", len(byInvoice))
	}
	var status string
	if err := s.DB().QueryRowContext(ctx, `SELECT status FROM delivery_outbox WHERE id = ?`, outboxID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != string(core.DeliverySent) {
		t.Fatalf("outbox status = %s", status)
	}
	reminders, err = s.ListReminders(ctx, invoice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reminders[0].Status != core.DeliverySent || reminders[0].SentAt == nil || !reminders[0].SentAt.Equal(at) {
		t.Fatalf("reminder not synchronized: %+v", reminders[0])
	}
}

func TestFailDeliveryUnknownVsFailed(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := time.Now()
	if _, err := s.Enqueue(ctx, core.Delivery{DedupKey: "fail-1", AvailableAt: now.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimDelivery(ctx, now, time.Minute)
	if err != nil || claimed == nil {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	longCode := strings.Repeat("x", 2000)
	if err := s.FailDelivery(ctx, claimed.ID, now, true, longCode); err != nil {
		t.Fatal(err)
	}
	var status, lastError string
	if err := s.DB().QueryRowContext(ctx, `SELECT status, last_error FROM delivery_outbox WHERE id = ?`, claimed.ID).Scan(&status, &lastError); err != nil {
		t.Fatal(err)
	}
	if status != string(core.DeliveryUnknown) {
		t.Fatalf("status = %s, want UNKNOWN", status)
	}
	if len(lastError) > 512 {
		t.Fatalf("last_error length = %d", len(lastError))
	}
	retried, err := s.ClaimDelivery(ctx, now.Add(time.Second), time.Minute)
	if err != nil || retried == nil {
		t.Fatalf("unknown not retried: %v %v", retried, err)
	}
	if err := s.FailDelivery(ctx, retried.ID, now.Add(time.Second), false, "bad request"); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRowContext(ctx, `SELECT status FROM delivery_outbox WHERE id = ?`, retried.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != string(core.DeliveryFailed) {
		t.Fatalf("status = %s, want FAILED", status)
	}
	if err := s.FailDelivery(ctx, "missing", now, false, "x"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("missing delivery = %v", err)
	}
}

func TestSkipDeliveryPreventsFurtherClaims(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := time.Now()
	if _, err := s.Enqueue(ctx, core.Delivery{DedupKey: "skip-1", AvailableAt: now.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimDelivery(ctx, now, time.Minute)
	if err != nil || claimed == nil {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	if err := s.SkipDelivery(ctx, claimed.ID, now); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := s.DB().QueryRowContext(ctx, `SELECT status FROM delivery_outbox WHERE id = ?`, claimed.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != string(core.DeliverySkipped) {
		t.Fatalf("status = %s, want SKIPPED", status)
	}
	next, err := s.ClaimDelivery(ctx, now.Add(time.Hour), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if next != nil {
		t.Fatalf("skipped delivery was claimed: %+v", next)
	}
	if err := s.SkipDelivery(ctx, "missing", now); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("missing skip = %v", err)
	}
}

func TestStaleFailAndSkipAreIgnored(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := time.Now()
	if _, err := s.Enqueue(ctx, core.Delivery{DedupKey: "stale-1", AvailableAt: now.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimDelivery(ctx, now, time.Minute)
	if err != nil || claimed == nil {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	if err := s.CompleteDelivery(ctx, claimed.ID, nil, now); err != nil {
		t.Fatal(err)
	}
	// A sender that lost the race must not overwrite a completed delivery.
	if err := s.FailDelivery(ctx, claimed.ID, now, false, "late"); err != nil {
		t.Fatalf("stale fail = %v", err)
	}
	if err := s.SkipDelivery(ctx, claimed.ID, now); err != nil {
		t.Fatalf("stale skip = %v", err)
	}
	var status string
	if err := s.DB().QueryRowContext(ctx, `SELECT status FROM delivery_outbox WHERE id = ?`, claimed.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != string(core.DeliverySent) {
		t.Fatalf("status = %s, want SENT", status)
	}
}

func TestRecordAndListTelegramMessages(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustAccount(t, s, "acc-tg", core.Microsoft, "tg@example.com")
	invoice := mustInvoice(t, s, sampleInvoice("acc-tg", "INV-TG"))
	if _, err := s.Enqueue(ctx, core.Delivery{DedupKey: "tg-1", Payload: core.DeliveryPayload{Kind: "invoice", InvoiceID: invoice.ID}}); err != nil {
		t.Fatal(err)
	}
	outboxID := mustOutboxID(t, s, "tg-1")
	for part := 0; part < 2; part++ {
		if err := s.RecordTelegramMessage(ctx, core.TelegramMessage{OutboxID: outboxID, InvoiceID: invoice.ID, ChatID: 5, MessageID: int64(200 + part), Part: part}); err != nil {
			t.Fatal(err)
		}
	}
	delivered, err := s.DeliveryMessages(ctx, outboxID)
	if err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 2 || delivered[0].MessageID != 200 || delivered[1].MessageID != 201 {
		t.Fatalf("delivery messages = %+v", delivered)
	}
	byInvoice, err := s.ListTelegramMessages(ctx, invoice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(byInvoice) != 2 {
		t.Fatalf("invoice messages = %d", len(byInvoice))
	}
	if err := s.RecordTelegramMessage(ctx, core.TelegramMessage{Part: 0}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("missing outbox = %v", err)
	}
}

func TestConcurrentClaimIsExclusive(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	const workers = 8
	now := time.Now()
	for i := 0; i < workers; i++ {
		if _, err := s.Enqueue(ctx, core.Delivery{DedupKey: "concurrent-" + string(rune('a'+i)), AvailableAt: now.Add(-time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	results := make(chan *core.Delivery, workers)
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := s.ClaimDelivery(ctx, now, time.Minute)
			if err != nil {
				errs <- err
				return
			}
			results <- d
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("claim error: %v", err)
	}
	seen := map[string]bool{}
	count := 0
	for d := range results {
		if d == nil {
			continue
		}
		if seen[d.ID] {
			t.Fatalf("delivery %s claimed twice", d.ID)
		}
		seen[d.ID] = true
		count++
	}
	if count != workers {
		t.Fatalf("claimed %d deliveries, want %d", count, workers)
	}
}

func TestConcurrentTransitionHasSingleWinner(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustAccount(t, s, "acc-ct", core.Microsoft, "ct@example.com")
	invoice := mustInvoice(t, s, sampleInvoice("acc-ct", "INV-CT"))
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(actor int64) {
			defer wg.Done()
			_, err := s.TransitionInvoice(ctx, invoice.ID, core.PendingConfirmation, core.PaidConfirmed, actor, time.Now())
			if err != nil && !errors.Is(err, core.ErrConflict) {
				t.Errorf("transition error: %v", err)
			}
		}(int64(i + 1))
	}
	wg.Wait()
	got, err := s.GetInvoice(ctx, invoice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != core.PaidConfirmed || got.ConfirmedBy == nil {
		t.Fatalf("final invoice = %+v", got)
	}
	events, err := s.InvoiceEvents(ctx, invoice.ID)
	if err != nil {
		t.Fatal(err)
	}
	paidEvents := 0
	for _, ev := range events {
		if ev.Action == string(core.PaidConfirmed) {
			paidEvents++
		}
	}
	if paidEvents != 1 {
		t.Fatalf("paid events = %d, want 1", paidEvents)
	}
}

func TestConcurrentReminderCreatesOne(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustAccount(t, s, "acc-cr", core.Microsoft, "cr@example.com")
	invoice := mustInvoice(t, s, sampleInvoice("acc-cr", "INV-CR"))
	const workers = 6
	var wg sync.WaitGroup
	createdCount := make(chan bool, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			created, err := s.CreateReminder(ctx,
				core.InvoiceReminder{InvoiceID: invoice.ID, ReminderDate: "2026-10-20"},
				core.Delivery{DedupKey: "reminder:" + invoice.ID + ":2026-10-20"})
			if err != nil {
				t.Errorf("create reminder: %v", err)
				return
			}
			createdCount <- created
		}()
	}
	wg.Wait()
	close(createdCount)
	created := 0
	for c := range createdCount {
		if c {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("created = %d, want exactly 1", created)
	}
	var reminders, outbox int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM invoice_reminders WHERE invoice_id = ?`, invoice.ID).Scan(&reminders); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM delivery_outbox`).Scan(&outbox); err != nil {
		t.Fatal(err)
	}
	if reminders != 1 || outbox != 1 {
		t.Fatalf("reminders=%d outbox=%d, want 1/1", reminders, outbox)
	}
}
