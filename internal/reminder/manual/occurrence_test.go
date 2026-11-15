package manual

import (
	"context"
	"errors"
	"testing"

	"email-invoice-guard/internal/core"
)

func seedOccurrence(repo *fakeRepo, planID, id string, status core.ManualOccurrenceStatus) core.ManualOccurrence {
	occurrence := core.ManualOccurrence{
		ID:        id,
		PlanID:    planID,
		DueDate:   "2026-11-15",
		Status:    status,
		CreatedAt: fixedNow().UTC(),
		UpdatedAt: fixedNow().UTC(),
	}
	repo.putOccurrence(occurrence)
	return occurrence
}

func TestConfirmOccurrence(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	s.now = fixedNow
	plan := seedPlan(repo, "p1", 15, "2026-11-15", "Asia/Taipei")
	occurrence := seedOccurrence(repo, plan.ID, "o1", core.ManualOccurrencePending)

	confirmed, err := s.ConfirmOccurrence(context.Background(), testActor, occurrence.ID)
	if err != nil {
		t.Fatalf("ConfirmOccurrence: %v", err)
	}
	if confirmed.Status != core.ManualOccurrencePaid {
		t.Fatalf("status = %s", confirmed.Status)
	}
	if confirmed.ConfirmedBy == nil || *confirmed.ConfirmedBy != testActor {
		t.Fatalf("confirmed by = %v", confirmed.ConfirmedBy)
	}
	if confirmed.ConfirmedAt == nil || !confirmed.ConfirmedAt.Equal(fixedNow().UTC()) {
		t.Fatalf("confirmed at = %v", confirmed.ConfirmedAt)
	}
	events := repo.eventsFor(plan.ID)
	if !containsEvent(events, core.ManualEventPaymentConfirmed) {
		t.Fatalf("events = %v", eventTypes(events))
	}

	before := len(events)
	again, err := s.ConfirmOccurrence(context.Background(), testActor, occurrence.ID)
	if err != nil {
		t.Fatalf("idempotent ConfirmOccurrence: %v", err)
	}
	if again.Status != core.ManualOccurrencePaid {
		t.Fatalf("idempotent status = %s", again.Status)
	}
	if len(repo.eventsFor(plan.ID)) != before {
		t.Fatal("idempotent replay wrote a second event")
	}
}

func TestRevokeOccurrence(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	s.now = fixedNow
	plan := seedPlan(repo, "p1", 15, "2026-11-15", "Asia/Taipei")
	occurrence := seedOccurrence(repo, plan.ID, "o1", core.ManualOccurrencePending)
	if _, err := s.ConfirmOccurrence(context.Background(), testActor, occurrence.ID); err != nil {
		t.Fatalf("ConfirmOccurrence: %v", err)
	}

	revoked, err := s.RevokeOccurrence(context.Background(), testActor, occurrence.ID)
	if err != nil {
		t.Fatalf("RevokeOccurrence: %v", err)
	}
	if revoked.Status != core.ManualOccurrencePending {
		t.Fatalf("status = %s", revoked.Status)
	}
	if revoked.ConfirmedAt != nil || revoked.ConfirmedBy != nil {
		t.Fatalf("confirmation fields not cleared: %v %v", revoked.ConfirmedAt, revoked.ConfirmedBy)
	}
	events := repo.eventsFor(plan.ID)
	if !containsEvent(events, core.ManualEventPaymentConfirmationRevoked) {
		t.Fatalf("events = %v", eventTypes(events))
	}

	before := len(events)
	again, err := s.RevokeOccurrence(context.Background(), testActor, occurrence.ID)
	if err != nil {
		t.Fatalf("idempotent RevokeOccurrence: %v", err)
	}
	if again.Status != core.ManualOccurrencePending {
		t.Fatalf("idempotent status = %s", again.Status)
	}
	if len(repo.eventsFor(plan.ID)) != before {
		t.Fatal("idempotent revoke wrote a second event")
	}
}

func TestRevokePendingOccurrenceIsIdempotent(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	s.now = fixedNow
	plan := seedPlan(repo, "p1", 15, "2026-11-15", "Asia/Taipei")
	occurrence := seedOccurrence(repo, plan.ID, "o1", core.ManualOccurrencePending)

	result, err := s.RevokeOccurrence(context.Background(), testActor, occurrence.ID)
	if err != nil {
		t.Fatalf("RevokeOccurrence: %v", err)
	}
	if result.Status != core.ManualOccurrencePending {
		t.Fatalf("status = %s", result.Status)
	}
	if events := repo.eventsFor(plan.ID); len(events) != 0 {
		t.Fatalf("events = %v", eventTypes(events))
	}
}

func TestConfirmCancelledOccurrenceConflicts(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	s.now = fixedNow
	plan := seedPlan(repo, "p1", 15, "2026-11-15", "Asia/Taipei")
	occurrence := seedOccurrence(repo, plan.ID, "o1", core.ManualOccurrenceCancelled)

	if _, err := s.ConfirmOccurrence(context.Background(), testActor, occurrence.ID); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict", err)
	}
	if _, err := s.RevokeOccurrence(context.Background(), testActor, occurrence.ID); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("revoke error = %v, want ErrConflict", err)
	}
}

func TestOccurrenceNotFound(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	s.now = fixedNow
	if _, err := s.ConfirmOccurrence(context.Background(), testActor, "missing"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("confirm error = %v", err)
	}
	if _, err := s.RevokeOccurrence(context.Background(), testActor, "missing"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("revoke error = %v", err)
	}
}

func TestEmptyOccurrenceIDIsInvalid(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	s.now = fixedNow
	if _, err := s.ConfirmOccurrence(context.Background(), testActor, " "); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("confirm error = %v", err)
	}
	if _, err := s.RevokeOccurrence(context.Background(), testActor, " "); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("revoke error = %v", err)
	}
}

func TestOccurrenceAuthorization(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	s.now = fixedNow
	plan := seedPlan(repo, "p1", 15, "2026-11-15", "Asia/Taipei")
	occurrence := seedOccurrence(repo, plan.ID, "o1", core.ManualOccurrencePending)

	if _, err := s.ConfirmOccurrence(context.Background(), 7, occurrence.ID); !errors.Is(err, core.ErrUnauthorized) {
		t.Fatalf("confirm error = %v", err)
	}
	if _, err := s.RevokeOccurrence(context.Background(), 7, occurrence.ID); !errors.Is(err, core.ErrUnauthorized) {
		t.Fatalf("revoke error = %v", err)
	}
	if repo.transitions != 0 {
		t.Fatalf("transitions = %d", repo.transitions)
	}
	if stored, err := repo.GetOccurrence(context.Background(), occurrence.ID); err != nil || stored.Status != core.ManualOccurrencePending {
		t.Fatalf("stored status = %s, %v", stored.Status, err)
	}
}
