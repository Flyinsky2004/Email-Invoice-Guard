package manual

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"email-invoice-guard/internal/core"
)

func TestTickBeforeReminderTimeDoesNothing(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	plan := seedPlan(repo, "p1", 15, "2026-11-15", "Asia/Taipei")

	if err := s.Tick(context.Background(), taipeiTime(2026, 11, 15, 8, 59)); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if occurrences := repo.occurrencesFor(plan.ID); len(occurrences) != 0 {
		t.Fatalf("occurrences = %d before reminder time", len(occurrences))
	}
	if dispatches := repo.dispatchesFor(plan.ID); len(dispatches) != 0 {
		t.Fatalf("dispatches = %d before reminder time", len(dispatches))
	}
	if stored, _ := repo.plan(plan.ID); stored.NextDueDate != "2026-11-15" {
		t.Fatalf("next due = %s", stored.NextDueDate)
	}
}

func TestTickAtReminderTimeCreatesOccurrenceAndAdvances(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	plan := seedPlan(repo, "p1", 15, "2026-11-15", "Asia/Taipei")

	if err := s.Tick(context.Background(), taipeiTime(2026, 11, 15, 9, 0)); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	occurrences := repo.occurrencesFor(plan.ID)
	if len(occurrences) != 1 {
		t.Fatalf("occurrences = %d, want 1", len(occurrences))
	}
	if occurrences[0].DueDate != "2026-11-15" || occurrences[0].Status != core.ManualOccurrencePending {
		t.Fatalf("occurrence = %+v", occurrences[0])
	}
	if stored, _ := repo.plan(plan.ID); stored.NextDueDate != "2026-12-15" {
		t.Fatalf("next due = %s, want 2026-12-15", stored.NextDueDate)
	}
	dispatches := repo.dispatchesFor(plan.ID)
	if len(dispatches) != 1 {
		t.Fatalf("dispatches = %d, want 1", len(dispatches))
	}
	dispatch := dispatches[0]
	if dispatch.LocalDate != "2026-11-15" || dispatch.Kind != core.ManualDispatchDailySummary {
		t.Fatalf("dispatch = %+v", dispatch)
	}
	if len(dispatch.OccurrenceIDs) != 1 || dispatch.OccurrenceIDs[0] != occurrences[0].ID {
		t.Fatalf("dispatch occurrence ids = %v", dispatch.OccurrenceIDs)
	}
	deliveries := repo.deliveriesFor(plan.ID)
	if len(deliveries) != 1 {
		t.Fatalf("deliveries = %d, want 1", len(deliveries))
	}
	delivery := deliveries[0]
	if delivery.DedupKey != "manual-reminder:p1:2026-11-15" {
		t.Fatalf("dedup key = %q", delivery.DedupKey)
	}
	if delivery.Status != core.DeliveryPending {
		t.Fatalf("delivery status = %s", delivery.Status)
	}
	if delivery.Payload.Kind != "manual" || delivery.Payload.ManualPlanID != plan.ID {
		t.Fatalf("payload = %+v", delivery.Payload)
	}
	if len(delivery.Payload.ManualOccurrenceIDs) != 1 || delivery.Payload.ManualOccurrenceIDs[0] != occurrences[0].ID {
		t.Fatalf("payload occurrence ids = %v", delivery.Payload.ManualOccurrenceIDs)
	}
	if !containsEvent(repo.eventsFor(plan.ID), core.ManualEventOccurrenceCreated) {
		t.Fatalf("events = %v", eventTypes(repo.eventsFor(plan.ID)))
	}
}

func TestTickRepeatedSameDayNoDuplicates(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	plan := seedPlan(repo, "p1", 15, "2026-11-15", "Asia/Taipei")

	for _, at := range []time.Time{
		taipeiTime(2026, 11, 15, 9, 0),
		taipeiTime(2026, 11, 15, 9, 0),
		taipeiTime(2026, 11, 15, 10, 0),
	} {
		if err := s.Tick(context.Background(), at); err != nil {
			t.Fatalf("Tick: %v", err)
		}
	}
	if occurrences := repo.occurrencesFor(plan.ID); len(occurrences) != 1 {
		t.Fatalf("occurrences = %d, want 1", len(occurrences))
	}
	if dispatches := repo.dispatchesFor(plan.ID); len(dispatches) != 1 {
		t.Fatalf("dispatches = %d, want 1", len(dispatches))
	}
	if deliveries := repo.deliveriesFor(plan.ID); len(deliveries) != 1 {
		t.Fatalf("deliveries = %d, want 1", len(deliveries))
	}
}

func TestTickFutureNextDueNoOccurrenceButSummarizesPending(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	plan := seedPlan(repo, "p1", 20, "2026-11-20", "Asia/Taipei")

	if err := s.Tick(context.Background(), taipeiTime(2026, 11, 15, 9, 0)); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if occurrences := repo.occurrencesFor(plan.ID); len(occurrences) != 0 {
		t.Fatalf("occurrences = %d, want 0 for a future due date", len(occurrences))
	}
	if dispatches := repo.dispatchesFor(plan.ID); len(dispatches) != 0 {
		t.Fatalf("dispatches = %d with no pending occurrence", len(dispatches))
	}

	// An older pending occurrence still triggers the daily summary even though
	// the plan's next due date is in the future.
	overdue := core.ManualOccurrence{
		ID: "o-overdue", PlanID: plan.ID, DueDate: "2026-11-10",
		Status: core.ManualOccurrencePending, CreatedAt: fixedNow().UTC(), UpdatedAt: fixedNow().UTC(),
	}
	repo.putOccurrence(overdue)
	if err := s.Tick(context.Background(), taipeiTime(2026, 11, 15, 9, 0)); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if occurrences := repo.occurrencesFor(plan.ID); len(occurrences) != 1 {
		t.Fatalf("occurrences = %d, want only the pre-existing one", len(occurrences))
	}
	dispatches := repo.dispatchesFor(plan.ID)
	if len(dispatches) != 1 || len(dispatches[0].OccurrenceIDs) != 1 || dispatches[0].OccurrenceIDs[0] != overdue.ID {
		t.Fatalf("dispatches = %+v", dispatches)
	}
}

func TestTickPausedPlanNoScheduling(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	plan := seedPlan(repo, "p1", 15, "2026-11-15", "Asia/Taipei")
	plan.Status = core.ManualPlanPaused
	repo.putPlan(plan)
	existing := core.ManualOccurrence{
		ID: "o-existing", PlanID: plan.ID, DueDate: "2026-10-15",
		Status: core.ManualOccurrencePending, CreatedAt: fixedNow().UTC(), UpdatedAt: fixedNow().UTC(),
	}
	repo.putOccurrence(existing)

	if err := s.Tick(context.Background(), taipeiTime(2026, 11, 15, 9, 0)); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if occurrences := repo.occurrencesFor(plan.ID); len(occurrences) != 1 || occurrences[0].ID != existing.ID {
		t.Fatalf("occurrences = %+v", occurrences)
	}
	if dispatches := repo.dispatchesFor(plan.ID); len(dispatches) != 0 {
		t.Fatalf("paused plan produced %d dispatches", len(dispatches))
	}
}

func TestTickCancelledPlanNoScheduling(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	plan := seedPlan(repo, "p1", 15, "2026-11-15", "Asia/Taipei")
	plan.Status = core.ManualPlanCancelled
	repo.putPlan(plan)

	if err := s.Tick(context.Background(), taipeiTime(2026, 11, 15, 9, 0)); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if occurrences := repo.occurrencesFor(plan.ID); len(occurrences) != 0 {
		t.Fatalf("cancelled plan produced %d occurrences", len(occurrences))
	}
	if dispatches := repo.dispatchesFor(plan.ID); len(dispatches) != 0 {
		t.Fatalf("cancelled plan produced %d dispatches", len(dispatches))
	}
}

func TestCatchUpWithinWindowMaterializesAll(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	s.maxCatchUp = 3
	plan := seedPlan(repo, "p1", 15, "2026-08-15", "Asia/Taipei")

	if err := s.Tick(context.Background(), taipeiTime(2026, 11, 15, 9, 0)); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	occurrences := repo.occurrencesFor(plan.ID)
	want := []core.Date{"2026-08-15", "2026-09-15", "2026-10-15", "2026-11-15"}
	if len(occurrences) != len(want) {
		t.Fatalf("occurrences = %d, want %d", len(occurrences), len(want))
	}
	for i, due := range want {
		if occurrences[i].DueDate != due {
			t.Fatalf("occurrence[%d] due = %s, want %s", i, occurrences[i].DueDate, due)
		}
	}
	if stored, _ := repo.plan(plan.ID); stored.NextDueDate != "2026-12-15" {
		t.Fatalf("next due = %s", stored.NextDueDate)
	}
	dispatches := repo.dispatchesFor(plan.ID)
	if len(dispatches) != 1 || len(dispatches[0].OccurrenceIDs) != len(want) {
		t.Fatalf("dispatch = %+v", dispatches)
	}
}

func TestCatchUpBeyondWindowJumpsForwardToToday(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	s.maxCatchUp = 2
	plan := seedPlan(repo, "p1", 15, "2026-08-15", "Asia/Taipei")

	if err := s.Tick(context.Background(), taipeiTime(2026, 11, 15, 9, 0)); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	occurrences := repo.occurrencesFor(plan.ID)
	if len(occurrences) != 1 || occurrences[0].DueDate != "2026-11-15" {
		t.Fatalf("occurrences = %+v, want the current period only", occurrences)
	}
	if stored, _ := repo.plan(plan.ID); stored.NextDueDate != "2026-12-15" {
		t.Fatalf("next due = %s", stored.NextDueDate)
	}
	if dispatches := repo.dispatchesFor(plan.ID); len(dispatches) != 1 {
		t.Fatalf("dispatches = %d", len(dispatches))
	}
}

func TestCatchUpJumpToFuturePersistsNextDue(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	s.maxCatchUp = 2
	plan := seedPlan(repo, "p1", 15, "2026-08-15", "Asia/Taipei")

	// Today is the 20th, so the first billing date on or after today is next
	// month's 15th: the jump is persisted and nothing is materialized.
	if err := s.Tick(context.Background(), taipeiTime(2026, 11, 20, 9, 0)); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if occurrences := repo.occurrencesFor(plan.ID); len(occurrences) != 0 {
		t.Fatalf("occurrences = %d, want 0", len(occurrences))
	}
	stored, _ := repo.plan(plan.ID)
	if stored.NextDueDate != "2026-12-15" {
		t.Fatalf("next due = %s, want 2026-12-15", stored.NextDueDate)
	}
	if len(repo.updatePlanEvents) != 1 {
		t.Fatalf("update plan events = %d", len(repo.updatePlanEvents))
	}
	event := repo.updatePlanEvents[0]
	if event.EventType != core.ManualEventPlanUpdated || !strings.Contains(event.Metadata, "catch_up") {
		t.Fatalf("event = %+v", event)
	}
	if dispatches := repo.dispatchesFor(plan.ID); len(dispatches) != 0 {
		t.Fatalf("dispatches = %d", len(dispatches))
	}
}

func TestPlanLocalTimezoneGovernsCalendarDay(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	plan := seedPlan(repo, "p1", 15, "2026-11-15", "America/New_York")

	// 13:00 UTC is 08:00 in New York, before the 09:00 plan-local reminder.
	if err := s.Tick(context.Background(), time.Date(2026, 11, 15, 13, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if occurrences := repo.occurrencesFor(plan.ID); len(occurrences) != 0 {
		t.Fatalf("occurrences = %d before plan-local reminder time", len(occurrences))
	}

	// 14:00 UTC is 09:00 in New York.
	if err := s.Tick(context.Background(), time.Date(2026, 11, 15, 14, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	occurrences := repo.occurrencesFor(plan.ID)
	if len(occurrences) != 1 || occurrences[0].DueDate != "2026-11-15" {
		t.Fatalf("occurrences = %+v", occurrences)
	}
}

func TestDSTDoesNotShiftCalendarDate(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	plan := seedPlan(repo, "p1", 8, "2026-03-08", "America/New_York")

	// 06:30 UTC is 01:30 EST, before the 09:00 reminder on the DST start day.
	if err := s.Tick(context.Background(), time.Date(2026, 3, 8, 6, 30, 0, 0, time.UTC)); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if occurrences := repo.occurrencesFor(plan.ID); len(occurrences) != 0 {
		t.Fatalf("occurrences = %d before reminder time", len(occurrences))
	}

	// 13:30 UTC is 09:30 EDT, after the reminder; the calendar date is still
	// 2026-03-08 and must not shift with the offset change.
	if err := s.Tick(context.Background(), time.Date(2026, 3, 8, 13, 30, 0, 0, time.UTC)); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	occurrences := repo.occurrencesFor(plan.ID)
	if len(occurrences) != 1 || occurrences[0].DueDate != "2026-03-08" {
		t.Fatalf("occurrences = %+v", occurrences)
	}
	if stored, _ := repo.plan(plan.ID); stored.NextDueDate != "2026-04-08" {
		t.Fatalf("next due = %s", stored.NextDueDate)
	}
}

func TestOneSummaryPerPlanPerLocalDayCoversAllOccurrences(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	plan := seedPlan(repo, "p1", 15, "2026-12-15", "Asia/Taipei")
	first := core.ManualOccurrence{
		ID: "o-first", PlanID: plan.ID, DueDate: "2026-11-01",
		Status: core.ManualOccurrencePending, CreatedAt: fixedNow().UTC(), UpdatedAt: fixedNow().UTC(),
	}
	second := core.ManualOccurrence{
		ID: "o-second", PlanID: plan.ID, DueDate: "2026-11-15",
		Status: core.ManualOccurrencePending, CreatedAt: fixedNow().UTC(), UpdatedAt: fixedNow().UTC(),
	}
	repo.putOccurrence(first)
	repo.putOccurrence(second)

	for i := 0; i < 3; i++ {
		if err := s.Tick(context.Background(), taipeiTime(2026, 11, 15, 9, 0)); err != nil {
			t.Fatalf("Tick: %v", err)
		}
	}
	dispatches := repo.dispatchesFor(plan.ID)
	if len(dispatches) != 1 {
		t.Fatalf("dispatches = %d, want one per local day", len(dispatches))
	}
	if len(dispatches[0].OccurrenceIDs) != 2 {
		t.Fatalf("dispatch occurrence ids = %v", dispatches[0].OccurrenceIDs)
	}
}

func TestRestartNoReplay(t *testing.T) {
	repo := newFakeRepo()
	first := newTestService(repo, testActor)
	first.now = fixedNow
	plan := mustCreatePlan(t, first, testActor, inputWithDay(15))
	if err := first.Tick(context.Background(), fixedNow()); err != nil {
		t.Fatalf("first Tick: %v", err)
	}

	second := newTestService(repo, testActor)
	second.now = fixedNow
	if err := second.Tick(context.Background(), fixedNow()); err != nil {
		t.Fatalf("second Tick: %v", err)
	}
	if occurrences := repo.occurrencesFor(plan.ID); len(occurrences) != 1 {
		t.Fatalf("occurrences after restart = %d, want 1", len(occurrences))
	}
	if dispatches := repo.dispatchesFor(plan.ID); len(dispatches) != 1 {
		t.Fatalf("dispatches after restart = %d, want 1", len(dispatches))
	}
	if deliveries := repo.deliveriesFor(plan.ID); len(deliveries) != 1 {
		t.Fatalf("deliveries after restart = %d, want 1", len(deliveries))
	}
}

func TestSchedulerNeverConfirmsPayment(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	plan := seedPlan(repo, "p1", 15, "2026-11-15", "Asia/Taipei")
	if err := s.Tick(context.Background(), taipeiTime(2026, 11, 15, 9, 0)); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if repo.transitions != 0 {
		t.Fatalf("scheduler transitioned payment state %d times", repo.transitions)
	}
	for _, occurrence := range repo.occurrencesFor(plan.ID) {
		if occurrence.Status != core.ManualOccurrencePending {
			t.Fatalf("occurrence status = %s", occurrence.Status)
		}
	}
}

func TestTickContextCancellation(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	seedPlan(repo, "p1", 15, "2026-11-15", "Asia/Taipei")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Tick(ctx, taipeiTime(2026, 11, 15, 9, 0)); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestRunStopsOnInvalidConfig(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	s.now = fixedNow
	seedPlan(repo, "p1", 15, "2026-11-15", "Not/AZone")
	if err := s.Run(context.Background()); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}

func TestRunRetriesTransientFailures(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	s.now = fixedNow
	repo.setErr("ListPlans", errors.New("transient database error"))
	if err := s.tick(context.Background()); err != nil {
		t.Fatalf("transient tick error = %v, want nil", err)
	}
	repo.setErr("ListPlans", nil)
	if err := s.tick(context.Background()); err != nil {
		t.Fatalf("recovered tick error = %v", err)
	}
	if calls := repo.callCount("ListPlans"); calls != 2 {
		t.Fatalf("ListPlans calls = %d, want 2", calls)
	}
}

func TestRunStopsOnCancellation(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	s.now = fixedNow
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Run(ctx); err != nil {
		t.Fatalf("Run error = %v", err)
	}
}

func TestTickContinuesAfterPlanError(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	seedPlan(repo, "a-invalid", 15, "2026-11-15", "Not/AZone")
	valid := seedPlan(repo, "b-valid", 15, "2026-11-15", "Asia/Taipei")

	err := s.Tick(context.Background(), taipeiTime(2026, 11, 15, 9, 0))
	if !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
	if occurrences := repo.occurrencesFor(valid.ID); len(occurrences) != 1 {
		t.Fatalf("valid plan occurrences = %d, want 1", len(occurrences))
	}
}

func TestTickSnapshotsAmount(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	plan := seedPlan(repo, "p1", 15, "2026-11-15", "Asia/Taipei")
	amount := core.Money{Minor: 500, Currency: "USD"}
	plan.Amount = &amount
	repo.putPlan(plan)

	if err := s.Tick(context.Background(), taipeiTime(2026, 11, 15, 9, 0)); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	occurrences := repo.occurrencesFor(plan.ID)
	if len(occurrences) != 1 || occurrences[0].Amount == nil || occurrences[0].Amount.Minor != 500 {
		t.Fatalf("occurrence amount = %+v", occurrences)
	}
	amount.Minor = 9999
	if occurrences[0].Amount.Minor != 500 {
		t.Fatal("occurrence amount aliases the plan amount")
	}
}

func TestDispatchSummarySkipsNonActivePlan(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	plan := seedPlan(repo, "p1", 15, "2026-11-15", "Asia/Taipei")
	plan.Status = core.ManualPlanPaused
	repo.putPlan(plan)
	repo.putOccurrence(core.ManualOccurrence{
		ID: "o-pending", PlanID: plan.ID, DueDate: "2026-11-15",
		Status: core.ManualOccurrencePending, CreatedAt: fixedNow().UTC(), UpdatedAt: fixedNow().UTC(),
	})

	if err := s.dispatchSummary(context.Background(), plan, "2026-11-15", fixedNow()); err != nil {
		t.Fatalf("dispatchSummary: %v", err)
	}
	if dispatches := repo.dispatchesFor(plan.ID); len(dispatches) != 0 {
		t.Fatalf("paused plan produced %d dispatches", len(dispatches))
	}
}

func TestTickRejectsEmptyTimezone(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	seedPlan(repo, "p1", 15, "2026-11-15", "")
	if err := s.Tick(context.Background(), taipeiTime(2026, 11, 15, 9, 0)); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}

func TestTickDoesNotCreateOccurrenceForFutureDue(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	plan := seedPlan(repo, "p1", 15, "2026-12-15", "Asia/Taipei")
	if err := s.Tick(context.Background(), taipeiTime(2026, 11, 15, 9, 0)); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if occurrences := repo.occurrencesFor(plan.ID); len(occurrences) != 0 {
		t.Fatalf("occurrences = %d, want 0", len(occurrences))
	}
}
