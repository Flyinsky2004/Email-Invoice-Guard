package lifecycle

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"email-invoice-guard/internal/core"
)

var transitionTime = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

type fakeStore struct {
	core.Store  // unimplemented calls panic, proving lifecycle uses no other API
	mu          sync.Mutex
	invoices    map[string]core.Invoice
	events      map[string][]core.InvoiceEvent
	transitions int
	reads       int
}

func newFakeStore(invoices ...core.Invoice) *fakeStore {
	store := &fakeStore{
		invoices: make(map[string]core.Invoice, len(invoices)),
		events:   make(map[string][]core.InvoiceEvent),
	}
	for _, invoice := range invoices {
		store.invoices[invoice.ID] = invoice
	}
	return store
}

func (f *fakeStore) GetInvoice(ctx context.Context, id string) (core.Invoice, error) {
	if err := ctx.Err(); err != nil {
		return core.Invoice{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	invoice, ok := f.invoices[id]
	if !ok {
		return core.Invoice{}, core.ErrNotFound
	}
	return invoice, nil
}

func (f *fakeStore) TransitionInvoice(ctx context.Context, id string, from, to core.InvoiceStatus, actor int64, at time.Time) (core.Invoice, error) {
	if err := ctx.Err(); err != nil {
		return core.Invoice{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.transitions++
	invoice, ok := f.invoices[id]
	if !ok {
		return core.Invoice{}, core.ErrNotFound
	}
	if invoice.Status == to {
		return invoice, nil
	}
	if invoice.Status != from {
		return core.Invoice{}, core.ErrConflict
	}
	invoice.Status = to
	if to == core.PaidConfirmed {
		invoice.ConfirmedBy = &actor
		invoice.ConfirmedAt = &at
	} else {
		invoice.ConfirmedBy = nil
		invoice.ConfirmedAt = nil
	}
	invoice.UpdatedAt = at
	f.invoices[id] = invoice
	f.events[id] = append(f.events[id], core.InvoiceEvent{
		ID:          core.NewID(),
		InvoiceID:   id,
		Action:      string(to),
		ActorUserID: actor,
		CreatedAt:   at,
	})
	return invoice, nil
}

func (f *fakeStore) InvoiceEvents(ctx context.Context, id string) ([]core.InvoiceEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]core.InvoiceEvent(nil), f.events[id]...), nil
}

func (f *fakeStore) Transaction(ctx context.Context, fn func(core.Repository) error) error {
	return fn(f)
}

func (f *fakeStore) eventCount(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.events[id])
}

func testInvoice(id string, status core.InvoiceStatus) core.Invoice {
	return core.Invoice{
		ID:         id,
		AccountID:  "account-1",
		ProviderID: "rfchost",
		Number:     "INV-100",
		Status:     status,
		CreatedAt:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func testService(store core.Store, allowed ...int64) *Service {
	service := New(store, allowed)
	service.now = func() time.Time { return transitionTime }
	service.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	return service
}

func TestUnauthorizedCallsHaveNoEffects(t *testing.T) {
	calls := []struct {
		name string
		call func(*Service, context.Context, string, int64) (core.Invoice, error)
	}{
		{"confirm", (*Service).Confirm},
		{"undo", (*Service).Undo},
		{"cancel", (*Service).Cancel},
	}
	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore(testInvoice("inv-1", core.PendingConfirmation))
			service := testService(store, 42)
			if _, err := tc.call(service, context.Background(), "inv-1", 99); !errors.Is(err, core.ErrUnauthorized) {
				t.Fatalf("want unauthorized, got %v", err)
			}
			store.mu.Lock()
			defer store.mu.Unlock()
			if store.transitions != 0 || store.reads != 0 {
				t.Fatalf("unauthorized call touched store: transitions=%d reads=%d", store.transitions, store.reads)
			}
			if store.invoices["inv-1"].Status != core.PendingConfirmation {
				t.Fatal("status changed")
			}
			if len(store.events["inv-1"]) != 0 {
				t.Fatal("event written")
			}
		})
	}
}

func TestConfirmPendingToPaid(t *testing.T) {
	store := newFakeStore(testInvoice("inv-1", core.PendingConfirmation))
	service := testService(store, 42)
	invoice, err := service.Confirm(context.Background(), "inv-1", 42)
	if err != nil {
		t.Fatal(err)
	}
	if invoice.Status != core.PaidConfirmed {
		t.Fatalf("status = %s", invoice.Status)
	}
	if invoice.ConfirmedBy == nil || *invoice.ConfirmedBy != 42 {
		t.Fatalf("confirmed_by = %v", invoice.ConfirmedBy)
	}
	if invoice.ConfirmedAt == nil || !invoice.ConfirmedAt.Equal(transitionTime) {
		t.Fatalf("confirmed_at = %v", invoice.ConfirmedAt)
	}
	events, _ := store.InvoiceEvents(context.Background(), "inv-1")
	if len(events) != 1 || events[0].Action != string(core.PaidConfirmed) || events[0].ActorUserID != 42 {
		t.Fatalf("events = %+v", events)
	}
}

func TestConfirmReplayIsIdempotent(t *testing.T) {
	store := newFakeStore(testInvoice("inv-1", core.PendingConfirmation))
	service := testService(store, 42)
	for i := 0; i < 3; i++ {
		invoice, err := service.Confirm(context.Background(), "inv-1", 42)
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if invoice.Status != core.PaidConfirmed {
			t.Fatalf("call %d status = %s", i, invoice.Status)
		}
	}
	if got := store.eventCount("inv-1"); got != 1 {
		t.Fatalf("events = %d, want exactly one", got)
	}
}

func TestUndoPaidToPendingClearsConfirmation(t *testing.T) {
	actor := int64(42)
	confirmedAt := transitionTime.Add(-time.Hour)
	invoice := testInvoice("inv-1", core.PaidConfirmed)
	invoice.ConfirmedBy = &actor
	invoice.ConfirmedAt = &confirmedAt
	store := newFakeStore(invoice)
	service := testService(store, 42)
	got, err := service.Undo(context.Background(), "inv-1", 42)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != core.PendingConfirmation {
		t.Fatalf("status = %s", got.Status)
	}
	if got.ConfirmedBy != nil || got.ConfirmedAt != nil {
		t.Fatalf("confirmation not cleared: %v %v", got.ConfirmedBy, got.ConfirmedAt)
	}
	events, _ := store.InvoiceEvents(context.Background(), "inv-1")
	if len(events) != 1 || events[0].Action != string(core.PendingConfirmation) {
		t.Fatalf("events = %+v", events)
	}
}

func TestUndoReplayIsIdempotent(t *testing.T) {
	actor := int64(42)
	invoice := testInvoice("inv-1", core.PaidConfirmed)
	invoice.ConfirmedBy = &actor
	store := newFakeStore(invoice)
	service := testService(store, 42)
	for i := 0; i < 2; i++ {
		got, err := service.Undo(context.Background(), "inv-1", 42)
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if got.Status != core.PendingConfirmation {
			t.Fatalf("call %d status = %s", i, got.Status)
		}
	}
	if got := store.eventCount("inv-1"); got != 1 {
		t.Fatalf("events = %d", got)
	}
}

func TestCancelAllowedStates(t *testing.T) {
	for _, status := range []core.InvoiceStatus{core.PendingConfirmation, core.ReviewRequired} {
		t.Run(string(status), func(t *testing.T) {
			store := newFakeStore(testInvoice("inv-1", status))
			service := testService(store, 42)
			got, err := service.Cancel(context.Background(), "inv-1", 42)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != core.Cancelled {
				t.Fatalf("status = %s", got.Status)
			}
			events, _ := store.InvoiceEvents(context.Background(), "inv-1")
			if len(events) != 1 || events[0].Action != string(core.Cancelled) {
				t.Fatalf("events = %+v", events)
			}
		})
	}
}

func TestCancelPaidIsConflict(t *testing.T) {
	store := newFakeStore(testInvoice("inv-1", core.PaidConfirmed))
	service := testService(store, 42)
	if _, err := service.Cancel(context.Background(), "inv-1", 42); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("want conflict, got %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.transitions != 0 {
		t.Fatal("paid invoice transitioned")
	}
	if store.invoices["inv-1"].Status != core.PaidConfirmed {
		t.Fatal("paid invoice changed")
	}
	if len(store.events["inv-1"]) != 0 {
		t.Fatal("event written")
	}
}

func TestCancelReplayIsIdempotent(t *testing.T) {
	store := newFakeStore(testInvoice("inv-1", core.Cancelled))
	service := testService(store, 42)
	got, err := service.Cancel(context.Background(), "inv-1", 42)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != core.Cancelled {
		t.Fatalf("status = %s", got.Status)
	}
	if store.eventCount("inv-1") != 0 {
		t.Fatal("replay wrote an event")
	}
}

func TestIllegalTransitionsConflict(t *testing.T) {
	cases := []struct {
		name   string
		status core.InvoiceStatus
		call   func(*Service, context.Context, string, int64) (core.Invoice, error)
	}{
		{"confirm review", core.ReviewRequired, (*Service).Confirm},
		{"confirm cancelled", core.Cancelled, (*Service).Confirm},
		{"undo cancelled", core.Cancelled, (*Service).Undo},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore(testInvoice("inv-1", tc.status))
			service := testService(store, 42)
			if _, err := tc.call(service, context.Background(), "inv-1", 42); !errors.Is(err, core.ErrConflict) {
				t.Fatalf("want conflict, got %v", err)
			}
			if store.eventCount("inv-1") != 0 {
				t.Fatal("illegal transition wrote an event")
			}
		})
	}
}

// Undo on an already-pending invoice is a same-target replay: it is a no-op
// rather than an illegal transition.
func TestUndoOnPendingIsIdempotent(t *testing.T) {
	store := newFakeStore(testInvoice("inv-1", core.PendingConfirmation))
	service := testService(store, 42)
	got, err := service.Undo(context.Background(), "inv-1", 42)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != core.PendingConfirmation {
		t.Fatalf("status = %s", got.Status)
	}
	if store.eventCount("inv-1") != 0 {
		t.Fatal("idempotent undo wrote an event")
	}
}

func TestUnknownInvoiceReturnsNotFound(t *testing.T) {
	store := newFakeStore()
	service := testService(store, 42)
	if _, err := service.Confirm(context.Background(), "missing", 42); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("confirm: %v", err)
	}
	if _, err := service.Cancel(context.Background(), "missing", 42); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestEmptyInvoiceIDIsInvalid(t *testing.T) {
	store := newFakeStore()
	service := testService(store, 42)
	for _, id := range []string{"", "   "} {
		if _, err := service.Confirm(context.Background(), id, 42); !errors.Is(err, core.ErrInvalid) {
			t.Fatalf("id %q: %v", id, err)
		}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.transitions != 0 || store.reads != 0 {
		t.Fatal("invalid id touched store")
	}
}

func TestCancelledContextStopsBeforeStore(t *testing.T) {
	store := newFakeStore(testInvoice("inv-1", core.PendingConfirmation))
	service := testService(store, 42)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Confirm(ctx, "inv-1", 42); !errors.Is(err, context.Canceled) {
		t.Fatalf("want canceled, got %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.transitions != 0 || store.reads != 0 {
		t.Fatal("cancelled context touched store")
	}
}

func TestConcurrentConfirmationWritesOneEvent(t *testing.T) {
	store := newFakeStore(testInvoice("inv-1", core.PendingConfirmation))
	service := testService(store, 42)
	const workers = 8
	var wait sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := service.Confirm(context.Background(), "inv-1", 42)
			errs <- err
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent confirm: %v", err)
		}
	}
	if got := store.eventCount("inv-1"); got != 1 {
		t.Fatalf("events = %d, want exactly one", got)
	}
}

// A mail-derived actor is not whitelisted, so an inbound payment email cannot
// move invoice state through this service.
func TestPaymentEmailActorCannotTransition(t *testing.T) {
	store := newFakeStore(testInvoice("inv-1", core.PendingConfirmation))
	service := testService(store, 42)
	if _, err := service.Confirm(context.Background(), "inv-1", 0); !errors.Is(err, core.ErrUnauthorized) {
		t.Fatalf("want unauthorized, got %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.invoices["inv-1"].Status != core.PendingConfirmation {
		t.Fatal("mail actor changed status")
	}
}

func TestAllowedListIsCopied(t *testing.T) {
	store := newFakeStore(testInvoice("inv-1", core.PendingConfirmation))
	allowed := []int64{42}
	service := testService(store, allowed...)
	allowed[0] = 99
	if _, err := service.Confirm(context.Background(), "inv-1", 42); err != nil {
		t.Fatalf("original allowlist entry lost: %v", err)
	}
	if _, err := service.Confirm(context.Background(), "inv-1", 99); !errors.Is(err, core.ErrUnauthorized) {
		t.Fatalf("mutated allowlist leaked: %v", err)
	}
}
