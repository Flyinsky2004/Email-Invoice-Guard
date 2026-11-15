package e2e_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"email-invoice-guard/internal/app"
	"email-invoice-guard/internal/core"
	"email-invoice-guard/internal/invoice/lifecycle"
	"email-invoice-guard/internal/invoice/parser"
	manualreminder "email-invoice-guard/internal/reminder/manual"
	"email-invoice-guard/internal/storage"
	"email-invoice-guard/internal/telegram"
)

// manualConfig returns a runtime config whose manual reminder defaults match
// the documented production defaults.
func manualConfig(tg *mockTelegram) core.Config {
	cfg := baseConfig(tg)
	cfg.Manual = core.ManualReminderConfig{
		DefaultTime:       "09:00",
		DefaultTimezone:   "Asia/Taipei",
		DailyRetry:        true,
		DailyInterval:     24 * time.Hour,
		MonthEndPolicy:    "last_day",
		SchedulerInterval: 30 * time.Second,
		WizardTTL:         10 * time.Minute,
		MaxCatchUpMonths:  24,
	}
	return cfg
}

// newManualBot builds a Bot wired to the manual service, mirroring the
// production app.Run composition.
func newManualBot(t *testing.T, s *storage.Store, cfg core.Config, tg *mockTelegram) *telegram.Bot {
	t.Helper()
	pay := lifecycle.New(s, cfg.Telegram.AllowedUserIDs)
	syncer := newEngine(s, cfg, map[core.ProviderType]core.MailSource{})
	svc := manualreminder.NewService(s, cfg, discardLogger())
	return telegram.NewWithManual(s, pay, syncer, parser.New(), map[core.ProviderType]core.AccountOnboarding{}, svc, cfg, tg.Client(), discardLogger())
}

// currentMonthBilling picks a billing day whose first due date still falls in
// the current calendar month. Tomorrow is used whenever the month has room so
// the result is independent of the wall-clock time; on the month's last day the
// only current-month candidate is today itself, which counts until 23:59.
func currentMonthBilling(t *testing.T, loc *time.Location) (int, core.Date) {
	t.Helper()
	now := time.Now().In(loc)
	lastDay := time.Date(now.Year(), now.Month()+1, 0, 0, 0, 0, 0, loc).Day()
	day := now.Day() + 1
	if day > lastDay {
		day = now.Day()
	}
	due := core.Date(time.Date(now.Year(), now.Month(), day, 0, 0, 0, 0, loc).Format("2006-01-02"))
	return day, due
}

func localReminderInstant(t *testing.T, due core.Date, loc *time.Location) time.Time {
	t.Helper()
	parsed, err := time.ParseInLocation("2006-01-02", string(due), loc)
	if err != nil {
		t.Fatalf("parse due date %q: %v", due, err)
	}
	return time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 23, 59, 0, 0, loc)
}

func pendingOccurrences(t *testing.T, s core.ManualReminderRepository, planID string) []core.ManualOccurrence {
	t.Helper()
	occurrences, err := s.ListOccurrences(t.Context(), core.ManualOccurrenceFilter{
		PlanID: planID,
		Status: core.ManualOccurrencePending,
	})
	if err != nil {
		t.Fatalf("list pending occurrences: %v", err)
	}
	return occurrences
}

func allOccurrences(t *testing.T, s core.ManualReminderRepository, planID string) []core.ManualOccurrence {
	t.Helper()
	occurrences, err := s.ListOccurrences(t.Context(), core.ManualOccurrenceFilter{PlanID: planID})
	if err != nil {
		t.Fatalf("list occurrences: %v", err)
	}
	return occurrences
}

func planDispatches(t *testing.T, s core.ManualReminderRepository, planID string) []core.ManualDispatch {
	t.Helper()
	dispatches, err := s.ListPlanDispatches(t.Context(), planID)
	if err != nil {
		t.Fatalf("list dispatches: %v", err)
	}
	return dispatches
}

func outboxDedupKey(t *testing.T, s *storage.Store, outboxID string) string {
	t.Helper()
	var key string
	if err := s.DB().QueryRow("SELECT dedup_key FROM delivery_outbox WHERE id = ?", outboxID).Scan(&key); err != nil {
		t.Fatalf("read outbox dedup key: %v", err)
	}
	return key
}

func outboxPayload(t *testing.T, s *storage.Store, outboxID string) core.DeliveryPayload {
	t.Helper()
	var raw string
	if err := s.DB().QueryRow("SELECT payload_json FROM delivery_outbox WHERE id = ?", outboxID).Scan(&raw); err != nil {
		t.Fatalf("read outbox payload: %v", err)
	}
	var payload core.DeliveryPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode outbox payload: %v", err)
	}
	return payload
}

// TestManualReminderEndToEnd drives the whole manual reminder feature against
// real SQLite and a mock Telegram server: create, tick, deliver, confirm,
// advance a month, then restart and prove nothing is duplicated.
func TestManualReminderEndToEnd(t *testing.T) {
	ctx := context.Background()
	store, path, key := openStore(t)
	tg := newMockTelegram(t)
	cfg := manualConfig(tg)
	svc := manualreminder.NewService(store, cfg, discardLogger())

	loc, err := time.LoadLocation(cfg.Manual.DefaultTimezone)
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	billingDay, dueDate := currentMonthBilling(t, loc)
	if !strings.HasPrefix(string(dueDate), time.Now().In(loc).Format("2006-01")) {
		t.Fatalf("due date %s is not in the current month", dueDate)
	}

	// 1. Create a plan through the manual service.
	plan, err := svc.CreatePlan(ctx, testUserID, core.ManualReminderInput{
		Title:        "VPS renewal",
		ProviderName: "Example Host",
		Amount:       &core.Money{Minor: 1200, Currency: "USD"},
		BillingDay:   billingDay,
		ReminderTime: "23:59",
		Timezone:     cfg.Manual.DefaultTimezone,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if plan.NextDueDate != dueDate {
		t.Fatalf("next due date = %s, want %s", plan.NextDueDate, dueDate)
	}

	// 2. Tick at the plan-local reminder time: one pending occurrence and one
	// manual outbox delivery with the documented dedup key.
	firstTick := localReminderInstant(t, dueDate, loc)
	if err := svc.Tick(ctx, firstTick); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	pending := pendingOccurrences(t, store, plan.ID)
	if len(pending) != 1 {
		t.Fatalf("want exactly one pending occurrence, got %d", len(pending))
	}
	firstOccurrence := pending[0]
	if firstOccurrence.DueDate != dueDate {
		t.Fatalf("occurrence due date = %s, want %s", firstOccurrence.DueDate, dueDate)
	}
	dispatches := planDispatches(t, store, plan.ID)
	if len(dispatches) != 1 {
		t.Fatalf("want exactly one dispatch, got %d", len(dispatches))
	}
	firstDispatch := dispatches[0]
	if firstDispatch.Kind != core.ManualDispatchDailySummary || firstDispatch.LocalDate != dueDate {
		t.Fatalf("unexpected dispatch: %+v", firstDispatch)
	}
	if outboxCount(t, store) != 1 {
		t.Fatalf("want exactly one outbox delivery, got %d", outboxCount(t, store))
	}
	wantDedup := "manual-reminder:" + plan.ID + ":" + string(dueDate)
	if got := outboxDedupKey(t, store, firstDispatch.OutboxID); got != wantDedup {
		t.Fatalf("dedup key = %q, want %q", got, wantDedup)
	}
	payload := outboxPayload(t, store, firstDispatch.OutboxID)
	if payload.Kind != "manual" || payload.ManualPlanID != plan.ID {
		t.Fatalf("unexpected manual payload: %+v", payload)
	}
	if len(payload.ManualOccurrenceIDs) != 1 || payload.ManualOccurrenceIDs[0] != firstOccurrence.ID {
		t.Fatalf("unexpected manual occurrence ids: %v", payload.ManualOccurrenceIDs)
	}

	// 3. Deliver once against the mock Telegram and mirror SENT on the dispatch.
	makeDeliveriesDue(t, store)
	bot := newManualBot(t, store, cfg, tg)
	handled, err := bot.DeliverOnce(ctx)
	if err != nil || !handled {
		t.Fatalf("deliver once: handled=%v err=%v", handled, err)
	}
	if got := tg.count("sendMessage"); got != 1 {
		t.Fatalf("want exactly one summary message, got %d", got)
	}
	if texts := tg.sentTexts(); len(texts) != 1 || !strings.Contains(texts[0], "Manual reminder") || !strings.Contains(texts[0], "VPS renewal") {
		t.Fatalf("unexpected summary text: %v", texts)
	}
	sentDispatch, err := store.GetDispatch(ctx, firstDispatch.ID)
	if err != nil {
		t.Fatalf("get dispatch: %v", err)
	}
	if sentDispatch.Status != core.DeliverySent {
		t.Fatalf("dispatch status = %s, want SENT", sentDispatch.Status)
	}

	// 4. Confirm the occurrence: PAID_CONFIRMED plus the audit event, and a
	// second tick the same day must not create a duplicate summary.
	confirmed, err := svc.ConfirmOccurrence(ctx, testUserID, firstOccurrence.ID)
	if err != nil {
		t.Fatalf("confirm occurrence: %v", err)
	}
	if confirmed.Status != core.ManualOccurrencePaid {
		t.Fatalf("occurrence status = %s, want PAID_CONFIRMED", confirmed.Status)
	}
	events, err := svc.Events(ctx, plan.ID)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if !hasEvent(events, core.ManualEventPaymentConfirmed, firstOccurrence.ID) {
		t.Fatalf("missing PAYMENT_CONFIRMED event: %+v", events)
	}
	if err := svc.Tick(ctx, firstTick); err != nil {
		t.Fatalf("second tick same day: %v", err)
	}
	if got := len(planDispatches(t, store, plan.ID)); got != 1 {
		t.Fatalf("same-day tick duplicated the summary: %d dispatches", got)
	}
	if got := outboxCount(t, store); got != 1 {
		t.Fatalf("same-day tick duplicated the outbox row: %d", got)
	}

	// 5. Advance to the next month: a new occurrence and a new summary.
	nextPlan, err := store.GetPlan(ctx, plan.ID)
	if err != nil {
		t.Fatalf("reload plan: %v", err)
	}
	secondDue := nextPlan.NextDueDate
	secondTick := localReminderInstant(t, secondDue, loc)
	if err := svc.Tick(ctx, secondTick); err != nil {
		t.Fatalf("next-month tick: %v", err)
	}
	occurrences := allOccurrences(t, store, plan.ID)
	if len(occurrences) != 2 {
		t.Fatalf("want two occurrences after the next month, got %d", len(occurrences))
	}
	dispatches = planDispatches(t, store, plan.ID)
	if len(dispatches) != 2 {
		t.Fatalf("want two dispatches after the next month, got %d", len(dispatches))
	}
	if dispatches[1].LocalDate != secondDue {
		t.Fatalf("second dispatch local date = %s, want %s", dispatches[1].LocalDate, secondDue)
	}
	if outboxCount(t, store) != 2 {
		t.Fatalf("want two outbox deliveries, got %d", outboxCount(t, store))
	}

	// 6. Restart: reopen the store and tick the same day again. The paid state
	// survives and neither a duplicate occurrence nor a duplicate dispatch is
	// produced.
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	restarted, err := storage.Open(ctx, path, key)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	restartedSvc := manualreminder.NewService(restarted, cfg, discardLogger())
	if err := restartedSvc.Tick(ctx, secondTick); err != nil {
		t.Fatalf("tick after restart: %v", err)
	}
	if got := len(allOccurrences(t, restarted, plan.ID)); got != 2 {
		t.Fatalf("restart duplicated occurrences: %d", got)
	}
	if got := len(planDispatches(t, restarted, plan.ID)); got != 2 {
		t.Fatalf("restart duplicated dispatches: %d", got)
	}
	if got := outboxCount(t, restarted); got != 2 {
		t.Fatalf("restart duplicated outbox rows: %d", got)
	}
	paid, err := restarted.GetOccurrence(ctx, firstOccurrence.ID)
	if err != nil {
		t.Fatalf("get occurrence after restart: %v", err)
	}
	if paid.Status != core.ManualOccurrencePaid {
		t.Fatalf("paid state lost across restart: %s", paid.Status)
	}
}

func hasEvent(events []core.ManualReminderEvent, eventType, occurrenceID string) bool {
	for _, event := range events {
		if event.EventType == eventType && event.OccurrenceID == occurrenceID {
			return true
		}
	}
	return false
}

// TestAppRunRunsManualScheduler proves app.Run composes the manual service,
// starts its scheduler goroutine, delivers the summary through the real bot,
// and stops cleanly on cancellation.
func TestAppRunRunsManualScheduler(t *testing.T) {
	tg := newMockTelegram(t)
	addr := freeAddr(t)
	dbPath := filepath.Join(t.TempDir(), "guard.db")
	key := []byte("01234567890123456789012345678901")

	loc, err := time.LoadLocation("Asia/Taipei")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	now := time.Now()
	today := core.LocalDate(now, loc)

	// Seed a plan that is already due at the current plan-local time so the
	// scheduler's first tick materializes it immediately.
	seed, err := storage.Open(t.Context(), dbPath, key)
	if err != nil {
		t.Fatalf("open seed store: %v", err)
	}
	plan := core.ManualPlan{
		ID:           core.NewID(),
		Title:        "Seeded renewal",
		BillingDay:   now.In(loc).Day(),
		ReminderTime: "00:00",
		Timezone:     "Asia/Taipei",
		Status:       core.ManualPlanActive,
		NextDueDate:  today,
		CreatedAt:    now.UTC(),
		UpdatedAt:    now.UTC(),
	}
	event := core.ManualReminderEvent{
		ID:        core.NewID(),
		PlanID:    plan.ID,
		EventType: core.ManualEventPlanCreated,
		CreatedAt: now.UTC(),
		Metadata:  "{}",
	}
	if _, err := seed.CreatePlan(t.Context(), plan, event); err != nil {
		t.Fatalf("seed plan: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	cfg := core.Config{
		DatabasePath:    dbPath,
		EncryptionKey:   key,
		ListenAddress:   addr,
		PublicBaseURL:   "https://invoice.example.com",
		HealthPath:      "/healthz",
		Timezone:        "Asia/Taipei",
		ReminderTime:    "09:00",
		SyncInterval:    time.Hour,
		ShutdownTimeout: 3 * time.Second,
		Telegram: core.TelegramConfig{
			Token:          testToken,
			BaseURL:        tg.URL(),
			ChatID:         testChatID,
			AllowedUserIDs: []int64{testUserID},
			PollTimeout:    500 * time.Millisecond,
			RateInterval:   time.Millisecond,
		},
		IMAP: core.IMAPConfig{
			Address:        "imap.qq.com:993",
			ServerName:     "imap.qq.com",
			AllowedDomains: []string{"qq.com", "foxmail.com"},
			Timeout:        200 * time.Millisecond,
		},
		Manual: core.ManualReminderConfig{
			DefaultTime:       "09:00",
			DefaultTimezone:   "Asia/Taipei",
			DailyRetry:        true,
			DailyInterval:     24 * time.Hour,
			MonthEndPolicy:    "last_day",
			SchedulerInterval: 50 * time.Millisecond,
			WizardTTL:         10 * time.Minute,
			MaxCatchUpMonths:  24,
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx, cfg, discardLogger()) }()

	waitFor(t, 8*time.Second, "manual summary delivered", func() bool {
		return tg.count("sendMessage") > 0
	})
	if texts := tg.sentTexts(); len(texts) == 0 || !strings.Contains(texts[0], "Seeded renewal") {
		t.Fatalf("unexpected manual summary: %v", texts)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("app.Run returned error: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("app.Run did not stop after cancellation")
	}
}
