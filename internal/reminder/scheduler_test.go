package reminder

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"email-invoice-guard/internal/core"
)

type fakeStore struct {
	core.Store // unimplemented calls panic, proving the scheduler uses no other API
	mu         sync.Mutex
	settings   map[string]string
	settingErr map[string]error
	invoices   map[string]core.Invoice
	reminders  []core.InvoiceReminder
	deliveries map[string]core.Delivery
	createErr  error
	creates    int
	getInvoice func(string) (core.Invoice, error)
}

func newFakeStore(invoices ...core.Invoice) *fakeStore {
	store := &fakeStore{
		settings:   map[string]string{},
		settingErr: map[string]error{},
		invoices:   map[string]core.Invoice{},
		deliveries: map[string]core.Delivery{},
	}
	for _, invoice := range invoices {
		store.invoices[invoice.ID] = invoice
	}
	return store
}

func (f *fakeStore) GetSetting(ctx context.Context, key string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.settingErr[key]; ok {
		return "", err
	}
	value, ok := f.settings[key]
	if !ok {
		return "", core.ErrNotFound
	}
	return value, nil
}

func (f *fakeStore) ListInvoices(ctx context.Context, filter core.InvoiceFilter) ([]core.Invoice, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []core.Invoice
	for _, invoice := range f.invoices {
		if filter.Status != "" && invoice.Status != filter.Status {
			continue
		}
		out = append(out, invoice)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeStore) GetInvoice(ctx context.Context, id string) (core.Invoice, error) {
	if err := ctx.Err(); err != nil {
		return core.Invoice{}, err
	}
	f.mu.Lock()
	override := f.getInvoice
	f.mu.Unlock()
	if override != nil {
		return override(id)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	invoice, ok := f.invoices[id]
	if !ok {
		return core.Invoice{}, core.ErrNotFound
	}
	return invoice, nil
}

func (f *fakeStore) CreateReminder(ctx context.Context, reminder core.InvoiceReminder, delivery core.Delivery) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates++
	if f.createErr != nil {
		return false, f.createErr
	}
	for _, existing := range f.reminders {
		if existing.InvoiceID == reminder.InvoiceID && existing.ReminderDate == reminder.ReminderDate {
			return false, nil
		}
	}
	f.reminders = append(f.reminders, reminder)
	f.deliveries[delivery.ID] = delivery
	return true, nil
}

func (f *fakeStore) Transaction(ctx context.Context, fn func(core.Repository) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn(f)
}

func (f *fakeStore) snapshot() ([]core.InvoiceReminder, map[string]core.Delivery, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	reminders := append([]core.InvoiceReminder(nil), f.reminders...)
	deliveries := make(map[string]core.Delivery, len(f.deliveries))
	for key, value := range f.deliveries {
		deliveries[key] = value
	}
	return reminders, deliveries, f.creates
}

func testScheduler(store core.Store, cfg core.Config) *Scheduler {
	scheduler := New(store, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	scheduler.now = func() time.Time { return utc(2026, 3, 8, 1, 0) }
	scheduler.interval = 5 * time.Millisecond
	return scheduler
}

func utc(year int, month time.Month, day, hour, minute int) time.Time {
	return time.Date(year, month, day, hour, minute, 0, 0, time.UTC)
}

func pendingInvoice(id string, createdAt time.Time) core.Invoice {
	return core.Invoice{
		ID:         id,
		AccountID:  "account-1",
		ProviderID: "rfchost",
		Number:     "INV-100",
		Status:     core.PendingConfirmation,
		DueDate:    core.Date("2026-03-20"),
		AmountDue:  core.Money{Minor: 684, Currency: "USD"},
		CreatedAt:  createdAt,
	}
}

func TestBeforeScheduleCreatesNothing(t *testing.T) {
	store := newFakeStore(pendingInvoice("inv-1", utc(2026, 3, 6, 0, 0)))
	scheduler := testScheduler(store, core.Config{})
	// 00:59 UTC is 08:59 in Asia/Taipei.
	if err := scheduler.Tick(context.Background(), utc(2026, 3, 8, 0, 59)); err != nil {
		t.Fatal(err)
	}
	if reminders, _, _ := store.snapshot(); len(reminders) != 0 {
		t.Fatalf("reminders = %d before 09:00", len(reminders))
	}
}

func TestAtScheduleCreatesReminderAndOutbox(t *testing.T) {
	store := newFakeStore(pendingInvoice("inv-1", utc(2026, 3, 6, 0, 0)))
	scheduler := testScheduler(store, core.Config{})
	now := utc(2026, 3, 8, 1, 0) // 09:00 Asia/Taipei
	if err := scheduler.Tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	reminders, deliveries, _ := store.snapshot()
	if len(reminders) != 1 {
		t.Fatalf("reminders = %d", len(reminders))
	}
	reminder := reminders[0]
	if reminder.ReminderDate != core.Date("2026-03-08") {
		t.Fatalf("reminder date = %s", reminder.ReminderDate)
	}
	if reminder.Status != core.DeliveryPending {
		t.Fatalf("reminder status = %s, reminder creation must not mark sent", reminder.Status)
	}
	if reminder.SentAt != nil {
		t.Fatal("reminder marked sent at creation")
	}
	delivery, ok := deliveries[reminder.OutboxID]
	if !ok {
		t.Fatalf("outbox %q missing", reminder.OutboxID)
	}
	if delivery.Payload.Kind != "reminder" || delivery.Payload.InvoiceID != "inv-1" || delivery.Payload.ReminderID != reminder.ID {
		t.Fatalf("payload = %+v", delivery.Payload)
	}
	if delivery.Status != core.DeliveryPending || delivery.AvailableAt.IsZero() {
		t.Fatalf("delivery = %+v", delivery)
	}
	if !strings.Contains(delivery.DedupKey, "inv-1") || !strings.Contains(delivery.DedupKey, "2026-03-08") {
		t.Fatalf("dedup key = %q", delivery.DedupKey)
	}
}

func TestCatchUpAfterSchedule(t *testing.T) {
	store := newFakeStore(pendingInvoice("inv-1", utc(2026, 3, 6, 0, 0)))
	scheduler := testScheduler(store, core.Config{})
	if err := scheduler.Tick(context.Background(), utc(2026, 3, 8, 14, 0)); err != nil { // 22:00 Taipei
		t.Fatal(err)
	}
	if reminders, _, _ := store.snapshot(); len(reminders) != 1 {
		t.Fatalf("catch-up reminders = %d", len(reminders))
	}
}

func TestSameDayInvoiceIsSuppressed(t *testing.T) {
	cases := []struct {
		name      string
		createdAt time.Time
	}{
		{"local same day", utc(2026, 3, 8, 0, 30)}, // 08:30 Taipei
		{"utc prior day", utc(2026, 3, 7, 23, 30)}, // 07:30 Taipei on 03-08
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore(pendingInvoice("inv-1", tc.createdAt))
			scheduler := testScheduler(store, core.Config{})
			if err := scheduler.Tick(context.Background(), utc(2026, 3, 8, 1, 0)); err != nil {
				t.Fatal(err)
			}
			if reminders, _, _ := store.snapshot(); len(reminders) != 0 {
				t.Fatalf("same-day invoice got %d reminders", len(reminders))
			}
		})
	}
}

func TestPriorDayInvoiceGetsReminder(t *testing.T) {
	store := newFakeStore(pendingInvoice("inv-1", utc(2026, 3, 7, 15, 0))) // 23:00 Taipei 03-07
	scheduler := testScheduler(store, core.Config{})
	if err := scheduler.Tick(context.Background(), utc(2026, 3, 8, 1, 0)); err != nil {
		t.Fatal(err)
	}
	if reminders, _, _ := store.snapshot(); len(reminders) != 1 {
		t.Fatalf("prior-day reminders = %d", len(reminders))
	}
}

func TestOverdueComesFromDueDateNotStatus(t *testing.T) {
	invoice := pendingInvoice("inv-1", utc(2026, 3, 1, 0, 0))
	invoice.DueDate = core.Date("2026-03-01")
	store := newFakeStore(invoice)
	scheduler := testScheduler(store, core.Config{})
	if err := scheduler.Tick(context.Background(), utc(2026, 3, 8, 1, 0)); err != nil {
		t.Fatal(err)
	}
	reminders, deliveries, _ := store.snapshot()
	if len(reminders) != 1 {
		t.Fatalf("overdue reminders = %d", len(reminders))
	}
	text := deliveries[reminders[0].OutboxID].Payload.Text
	if !strings.Contains(text, "Overdue by 7 day(s)") {
		t.Fatalf("text = %q", text)
	}
}

func TestDueTodayText(t *testing.T) {
	invoice := pendingInvoice("inv-1", utc(2026, 3, 1, 0, 0))
	invoice.DueDate = core.Date("2026-03-08")
	store := newFakeStore(invoice)
	scheduler := testScheduler(store, core.Config{})
	if err := scheduler.Tick(context.Background(), utc(2026, 3, 8, 1, 0)); err != nil {
		t.Fatal(err)
	}
	reminders, deliveries, _ := store.snapshot()
	if len(reminders) != 1 {
		t.Fatalf("reminders = %d", len(reminders))
	}
	if text := deliveries[reminders[0].OutboxID].Payload.Text; !strings.Contains(text, "Due today") {
		t.Fatalf("text = %q", text)
	}
}

func TestOnlyPendingInvoicesAreReminded(t *testing.T) {
	invoices := []core.Invoice{
		pendingInvoice("pending", utc(2026, 3, 6, 0, 0)),
		pendingInvoice("paid", utc(2026, 3, 6, 0, 0)),
		pendingInvoice("review", utc(2026, 3, 6, 0, 0)),
		pendingInvoice("cancelled", utc(2026, 3, 6, 0, 0)),
	}
	invoices[1].Status = core.PaidConfirmed
	invoices[2].Status = core.ReviewRequired
	invoices[3].Status = core.Cancelled
	store := newFakeStore(invoices...)
	scheduler := testScheduler(store, core.Config{})
	if err := scheduler.Tick(context.Background(), utc(2026, 3, 8, 1, 0)); err != nil {
		t.Fatal(err)
	}
	reminders, _, _ := store.snapshot()
	if len(reminders) != 1 || reminders[0].InvoiceID != "pending" {
		t.Fatalf("reminders = %+v", reminders)
	}
}

func TestRepeatedTickCreatesOneTask(t *testing.T) {
	store := newFakeStore(pendingInvoice("inv-1", utc(2026, 3, 6, 0, 0)))
	scheduler := testScheduler(store, core.Config{})
	for i := 0; i < 3; i++ {
		if err := scheduler.Tick(context.Background(), utc(2026, 3, 8, 1, 0)); err != nil {
			t.Fatal(err)
		}
	}
	reminders, _, creates := store.snapshot()
	if len(reminders) != 1 {
		t.Fatalf("reminders = %d", len(reminders))
	}
	if creates != 3 {
		t.Fatalf("create calls = %d, want one attempt per tick", creates)
	}
}

func TestRestartDoesNotDuplicateReminder(t *testing.T) {
	store := newFakeStore(pendingInvoice("inv-1", utc(2026, 3, 6, 0, 0)))
	now := utc(2026, 3, 8, 1, 0)
	if err := testScheduler(store, core.Config{}).Tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	// A fresh scheduler simulates a process restart against persistent state.
	if err := testScheduler(store, core.Config{}).Tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if reminders, _, _ := store.snapshot(); len(reminders) != 1 {
		t.Fatalf("restart reminders = %d", len(reminders))
	}
}

func TestConcurrentConfirmationSkipsReminder(t *testing.T) {
	store := newFakeStore(pendingInvoice("inv-1", utc(2026, 3, 6, 0, 0)))
	paid := pendingInvoice("inv-1", utc(2026, 3, 6, 0, 0))
	paid.Status = core.PaidConfirmed
	store.getInvoice = func(string) (core.Invoice, error) { return paid, nil }
	scheduler := testScheduler(store, core.Config{})
	if err := scheduler.Tick(context.Background(), utc(2026, 3, 8, 1, 0)); err != nil {
		t.Fatal(err)
	}
	reminders, _, creates := store.snapshot()
	if len(reminders) != 0 || creates != 0 {
		t.Fatalf("reminders=%d creates=%d after concurrent confirmation", len(reminders), creates)
	}
}

func TestConcurrentCancellationSkipsReminder(t *testing.T) {
	store := newFakeStore(pendingInvoice("inv-1", utc(2026, 3, 6, 0, 0)))
	cancelled := pendingInvoice("inv-1", utc(2026, 3, 6, 0, 0))
	cancelled.Status = core.Cancelled
	store.getInvoice = func(string) (core.Invoice, error) { return cancelled, nil }
	scheduler := testScheduler(store, core.Config{})
	if err := scheduler.Tick(context.Background(), utc(2026, 3, 8, 1, 0)); err != nil {
		t.Fatal(err)
	}
	if reminders, _, _ := store.snapshot(); len(reminders) != 0 {
		t.Fatalf("reminders = %d after concurrent cancellation", len(reminders))
	}
}

func TestInvalidPersistedTimezoneReturnsError(t *testing.T) {
	store := newFakeStore(pendingInvoice("inv-1", utc(2026, 3, 6, 0, 0)))
	store.settings["timezone"] = "Mars/Olympus"
	scheduler := testScheduler(store, core.Config{})
	if err := scheduler.Tick(context.Background(), utc(2026, 3, 8, 1, 0)); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("want invalid, got %v", err)
	}
}

func TestInvalidPersistedReminderTimeReturnsError(t *testing.T) {
	store := newFakeStore(pendingInvoice("inv-1", utc(2026, 3, 6, 0, 0)))
	store.settings["reminder_time"] = "9am"
	scheduler := testScheduler(store, core.Config{})
	if err := scheduler.Tick(context.Background(), utc(2026, 3, 8, 1, 0)); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("want invalid, got %v", err)
	}
}

func TestEmptyPersistedSettingReturnsError(t *testing.T) {
	store := newFakeStore(pendingInvoice("inv-1", utc(2026, 3, 6, 0, 0)))
	store.settings["timezone"] = "   "
	scheduler := testScheduler(store, core.Config{})
	if err := scheduler.Tick(context.Background(), utc(2026, 3, 8, 1, 0)); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("want invalid, got %v", err)
	}
}

func TestInvalidConfigTimezoneReturnsError(t *testing.T) {
	store := newFakeStore(pendingInvoice("inv-1", utc(2026, 3, 6, 0, 0)))
	scheduler := testScheduler(store, core.Config{Timezone: "Not/AZone"})
	if err := scheduler.Tick(context.Background(), utc(2026, 3, 8, 1, 0)); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("want invalid, got %v", err)
	}
}

func TestSettingReadFailureIsReturned(t *testing.T) {
	store := newFakeStore(pendingInvoice("inv-1", utc(2026, 3, 6, 0, 0)))
	store.settingErr["timezone"] = errors.New("database unavailable")
	scheduler := testScheduler(store, core.Config{})
	err := scheduler.Tick(context.Background(), utc(2026, 3, 8, 1, 0))
	if err == nil || errors.Is(err, core.ErrInvalid) || !strings.Contains(err.Error(), "database unavailable") {
		t.Fatalf("err = %v", err)
	}
}

func TestSettingsOverrideConfigPerTick(t *testing.T) {
	store := newFakeStore(pendingInvoice("inv-1", utc(2026, 3, 6, 0, 0)))
	store.settings["timezone"] = "Asia/Taipei"
	store.settings["reminder_time"] = "09:00"
	// Config says UTC, where 01:00 is before 09:00; the persisted setting wins.
	scheduler := testScheduler(store, core.Config{Timezone: "UTC", ReminderTime: "09:00"})
	if err := scheduler.Tick(context.Background(), utc(2026, 3, 8, 1, 0)); err != nil {
		t.Fatal(err)
	}
	if reminders, _, _ := store.snapshot(); len(reminders) != 1 {
		t.Fatalf("reminders = %d, persisted timezone was not used", len(reminders))
	}
}

func TestSettingChangeBetweenTicks(t *testing.T) {
	store := newFakeStore(pendingInvoice("inv-1", utc(2026, 3, 6, 0, 0)))
	store.settings["timezone"] = "UTC"
	scheduler := testScheduler(store, core.Config{})
	now := utc(2026, 3, 8, 1, 0)
	if err := scheduler.Tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if reminders, _, _ := store.snapshot(); len(reminders) != 0 {
		t.Fatalf("reminders = %d before setting change", len(reminders))
	}
	store.settings["timezone"] = "Asia/Taipei"
	if err := scheduler.Tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if reminders, _, _ := store.snapshot(); len(reminders) != 1 {
		t.Fatalf("reminders = %d after setting change", len(reminders))
	}
}

func TestDSTSpringForwardLocalDay(t *testing.T) {
	store := newFakeStore(pendingInvoice("inv-1", utc(2026, 3, 6, 0, 0)))
	store.settings["timezone"] = "America/New_York"
	scheduler := testScheduler(store, core.Config{})
	// 12:59 UTC is 08:59 EDT on the spring-forward day.
	if err := scheduler.Tick(context.Background(), utc(2026, 3, 8, 12, 59)); err != nil {
		t.Fatal(err)
	}
	if reminders, _, _ := store.snapshot(); len(reminders) != 0 {
		t.Fatalf("reminders = %d before 09:00 EDT", len(reminders))
	}
	// 13:00 UTC is 09:00 EDT.
	if err := scheduler.Tick(context.Background(), utc(2026, 3, 8, 13, 0)); err != nil {
		t.Fatal(err)
	}
	reminders, _, _ := store.snapshot()
	if len(reminders) != 1 || reminders[0].ReminderDate != core.Date("2026-03-08") {
		t.Fatalf("reminders = %+v", reminders)
	}
}

func TestDSTFallBackLocalDay(t *testing.T) {
	store := newFakeStore(pendingInvoice("inv-1", utc(2026, 10, 30, 0, 0)))
	store.settings["timezone"] = "America/New_York"
	scheduler := testScheduler(store, core.Config{})
	// 13:59 UTC is 08:59 EST after the fall-back transition.
	if err := scheduler.Tick(context.Background(), utc(2026, 11, 1, 13, 59)); err != nil {
		t.Fatal(err)
	}
	if reminders, _, _ := store.snapshot(); len(reminders) != 0 {
		t.Fatalf("reminders = %d before 09:00 EST", len(reminders))
	}
	if err := scheduler.Tick(context.Background(), utc(2026, 11, 1, 14, 0)); err != nil {
		t.Fatal(err)
	}
	reminders, _, _ := store.snapshot()
	if len(reminders) != 1 || reminders[0].ReminderDate != core.Date("2026-11-01") {
		t.Fatalf("reminders = %+v", reminders)
	}
}

func TestCanceledContextStopsTick(t *testing.T) {
	store := newFakeStore(pendingInvoice("inv-1", utc(2026, 3, 6, 0, 0)))
	scheduler := testScheduler(store, core.Config{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := scheduler.Tick(ctx, utc(2026, 3, 8, 1, 0)); !errors.Is(err, context.Canceled) {
		t.Fatalf("want canceled, got %v", err)
	}
}

func TestCreateReminderFailureIsReturned(t *testing.T) {
	store := newFakeStore(pendingInvoice("inv-1", utc(2026, 3, 6, 0, 0)))
	store.createErr = errors.New("unique violation")
	scheduler := testScheduler(store, core.Config{})
	err := scheduler.Tick(context.Background(), utc(2026, 3, 8, 1, 0))
	if err == nil || !strings.Contains(err.Error(), "create reminder") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunRejectsInvalidConfiguration(t *testing.T) {
	store := newFakeStore(pendingInvoice("inv-1", utc(2026, 3, 6, 0, 0)))
	store.settings["timezone"] = "Mars/Olympus"
	scheduler := testScheduler(store, core.Config{})
	if err := scheduler.Run(context.Background()); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("want invalid, got %v", err)
	}
}

func TestRunStopsOnCancellation(t *testing.T) {
	store := newFakeStore(pendingInvoice("inv-1", utc(2026, 3, 6, 0, 0)))
	scheduler := testScheduler(store, core.Config{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := scheduler.Run(ctx); err != nil {
		t.Fatalf("run returned %v", err)
	}
}

func TestRunTicksPeriodically(t *testing.T) {
	store := newFakeStore(pendingInvoice("inv-1", utc(2026, 3, 6, 0, 0)))
	scheduler := testScheduler(store, core.Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := scheduler.Run(ctx); err != nil {
		t.Fatalf("run returned %v", err)
	}
	if reminders, _, _ := store.snapshot(); len(reminders) != 1 {
		t.Fatalf("reminders = %d", len(reminders))
	}
}
