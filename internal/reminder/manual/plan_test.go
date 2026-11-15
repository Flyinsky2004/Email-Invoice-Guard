package manual

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"email-invoice-guard/internal/core"
)

func TestCreatePlanComputesFirstDueDate(t *testing.T) {
	cases := []struct {
		day  int
		want core.Date
	}{
		{15, "2026-11-15"},
		{20, "2026-11-20"},
		{10, "2026-12-10"},
		{31, "2026-11-30"},
	}
	for _, c := range cases {
		repo := newFakeRepo()
		s := newTestService(repo, testActor)
		s.now = fixedNow
		plan := mustCreatePlan(t, s, testActor, inputWithDay(c.day))
		if plan.NextDueDate != c.want {
			t.Errorf("billing day %d: next due = %s, want %s", c.day, plan.NextDueDate, c.want)
		}
		if plan.Status != core.ManualPlanActive {
			t.Errorf("billing day %d: status = %s", c.day, plan.Status)
		}
		if occurrences := repo.occurrencesFor(plan.ID); len(occurrences) != 0 {
			t.Errorf("billing day %d: creation must not materialize occurrences, got %d", c.day, len(occurrences))
		}
	}
}

func TestCreatePlanValidation(t *testing.T) {
	cases := []struct {
		name string
		in   core.ManualReminderInput
	}{
		{"empty title", core.ManualReminderInput{Title: "  ", BillingDay: 15, ReminderTime: "09:00", Timezone: "Asia/Taipei"}},
		{"billing day zero", core.ManualReminderInput{Title: "x", BillingDay: 0, ReminderTime: "09:00", Timezone: "Asia/Taipei"}},
		{"billing day high", core.ManualReminderInput{Title: "x", BillingDay: 32, ReminderTime: "09:00", Timezone: "Asia/Taipei"}},
		{"bad reminder time", core.ManualReminderInput{Title: "x", BillingDay: 15, ReminderTime: "25:00", Timezone: "Asia/Taipei"}},
		{"bad timezone", core.ManualReminderInput{Title: "x", BillingDay: 15, ReminderTime: "09:00", Timezone: "Not/AZone"}},
		{"bad currency", core.ManualReminderInput{Title: "x", BillingDay: 15, ReminderTime: "09:00", Timezone: "Asia/Taipei", Amount: &core.Money{Minor: 100, Currency: "XXX"}}},
	}
	for _, c := range cases {
		repo := newFakeRepo()
		s := newTestService(repo, testActor)
		s.now = fixedNow
		if _, err := s.CreatePlan(context.Background(), testActor, c.in); !errors.Is(err, core.ErrInvalid) {
			t.Errorf("%s: error = %v, want ErrInvalid", c.name, err)
		}
		if len(repo.plans) != 0 {
			t.Errorf("%s: invalid input persisted a plan", c.name)
		}
	}
}

func TestCreatePlanRejectsUnknownMonthEndPolicy(t *testing.T) {
	cfg := testConfig(testActor)
	cfg.Manual.MonthEndPolicy = "first_day"
	repo := newFakeRepo()
	s := NewService(repo, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.now = fixedNow
	if _, err := s.CreatePlan(context.Background(), testActor, inputWithDay(15)); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}

func TestCreatePlanDefaultsTimeAndTimezone(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	s.now = fixedNow
	plan := mustCreatePlan(t, s, testActor, core.ManualReminderInput{Title: "Hosting", BillingDay: 15})
	if plan.ReminderTime != "09:00" {
		t.Errorf("reminder time = %q", plan.ReminderTime)
	}
	if plan.Timezone != "Asia/Taipei" {
		t.Errorf("timezone = %q", plan.Timezone)
	}
}

func TestCreatePlanAuthorization(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	s.now = fixedNow
	if _, err := s.CreatePlan(context.Background(), 7, inputWithDay(15)); !errors.Is(err, core.ErrUnauthorized) {
		t.Fatalf("error = %v, want ErrUnauthorized", err)
	}
	if len(repo.plans) != 0 {
		t.Fatal("unauthorized call persisted a plan")
	}
	if calls := repo.callCount("CreatePlan"); calls != 0 {
		t.Fatalf("repository CreatePlan calls = %d", calls)
	}
}

func TestCreatePlanWritesEvent(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	s.now = fixedNow
	plan := mustCreatePlan(t, s, testActor, inputWithDay(15))
	events := repo.eventsFor(plan.ID)
	if len(events) != 1 || events[0].EventType != core.ManualEventPlanCreated {
		t.Fatalf("events = %v", eventTypes(events))
	}
	if events[0].ActorID != testActor {
		t.Fatalf("actor = %d", events[0].ActorID)
	}
}

func TestUpdatePlanRecomputesDueOnBillingDayChange(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	s.now = fixedNow
	plan := mustCreatePlan(t, s, testActor, inputWithDay(15))

	updated, err := s.UpdatePlan(context.Background(), testActor, plan.ID, inputWithDay(20))
	if err != nil {
		t.Fatalf("UpdatePlan day 20: %v", err)
	}
	if updated.NextDueDate != "2026-11-20" {
		t.Fatalf("next due = %s, want 2026-11-20", updated.NextDueDate)
	}
	updated, err = s.UpdatePlan(context.Background(), testActor, plan.ID, inputWithDay(10))
	if err != nil {
		t.Fatalf("UpdatePlan day 10: %v", err)
	}
	if updated.NextDueDate != "2026-12-10" {
		t.Fatalf("next due = %s, want 2026-12-10", updated.NextDueDate)
	}
	if !containsEvent(repo.eventsFor(plan.ID), core.ManualEventPlanUpdated) {
		t.Fatal("missing PLAN_UPDATED event")
	}
}

func TestUpdatePlanKeepsDueWhenBillingDayUnchanged(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	s.now = fixedNow
	plan := mustCreatePlan(t, s, testActor, inputWithDay(15))

	in := inputWithDay(15)
	in.Title = "Renamed"
	updated, err := s.UpdatePlan(context.Background(), testActor, plan.ID, in)
	if err != nil {
		t.Fatalf("UpdatePlan: %v", err)
	}
	if updated.Title != "Renamed" {
		t.Fatalf("title = %q", updated.Title)
	}
	if updated.NextDueDate != "2026-11-15" {
		t.Fatalf("next due = %s, want unchanged 2026-11-15", updated.NextDueDate)
	}
}

func TestUpdatePlanRejectsCancelledPlan(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	s.now = fixedNow
	plan := mustCreatePlan(t, s, testActor, inputWithDay(15))
	if _, err := s.CancelPlan(context.Background(), testActor, plan.ID); err != nil {
		t.Fatalf("CancelPlan: %v", err)
	}
	if _, err := s.UpdatePlan(context.Background(), testActor, plan.ID, inputWithDay(20)); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict", err)
	}
}

func TestUpdatePlanNotFound(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	s.now = fixedNow
	if _, err := s.UpdatePlan(context.Background(), testActor, "missing", inputWithDay(15)); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestPauseResumeLifecycle(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	s.now = fixedNow
	plan := mustCreatePlan(t, s, testActor, inputWithDay(15))

	paused, err := s.PausePlan(context.Background(), testActor, plan.ID)
	if err != nil {
		t.Fatalf("PausePlan: %v", err)
	}
	if paused.Status != core.ManualPlanPaused {
		t.Fatalf("status = %s", paused.Status)
	}
	if _, err := s.PausePlan(context.Background(), testActor, plan.ID); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("second pause error = %v, want ErrConflict", err)
	}

	resumed, err := s.ResumePlan(context.Background(), testActor, plan.ID)
	if err != nil {
		t.Fatalf("ResumePlan: %v", err)
	}
	if resumed.Status != core.ManualPlanActive {
		t.Fatalf("status = %s", resumed.Status)
	}
	if resumed.NextDueDate != "2026-11-15" {
		t.Fatalf("next due = %s, want 2026-11-15", resumed.NextDueDate)
	}
	if _, err := s.ResumePlan(context.Background(), testActor, plan.ID); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("second resume error = %v, want ErrConflict", err)
	}
	events := eventTypes(repo.eventsFor(plan.ID))
	if !containsEvent(repo.eventsFor(plan.ID), core.ManualEventPlanPaused) ||
		!containsEvent(repo.eventsFor(plan.ID), core.ManualEventPlanResumed) {
		t.Fatalf("events = %v", events)
	}
}

func TestResumeDoesNotBackfillMissedPeriods(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	s.now = fixedNow
	plan := seedPlan(repo, "p-paused", 15, "2026-08-15", "Asia/Taipei")
	plan.Status = core.ManualPlanPaused
	repo.putPlan(plan)

	resumed, err := s.ResumePlan(context.Background(), testActor, plan.ID)
	if err != nil {
		t.Fatalf("ResumePlan: %v", err)
	}
	if resumed.NextDueDate != "2026-11-15" {
		t.Fatalf("next due = %s, want first date on or after today 2026-11-15", resumed.NextDueDate)
	}
	if occurrences := repo.occurrencesFor(plan.ID); len(occurrences) != 0 {
		t.Fatalf("resume backfilled %d occurrences", len(occurrences))
	}
}

func TestCancelPlanCancelsOccurrencesAndSkipsDeliveries(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	s.now = fixedNow
	plan := seedPlan(repo, "p-cancel", 15, "2026-11-15", "Asia/Taipei")
	if err := s.Tick(context.Background(), fixedNow()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(repo.occurrencesFor(plan.ID)) != 1 {
		t.Fatal("setup: expected one occurrence")
	}

	cancelled, err := s.CancelPlan(context.Background(), testActor, plan.ID)
	if err != nil {
		t.Fatalf("CancelPlan: %v", err)
	}
	if cancelled.Status != core.ManualPlanCancelled {
		t.Fatalf("status = %s", cancelled.Status)
	}
	for _, occurrence := range repo.occurrencesFor(plan.ID) {
		if occurrence.Status != core.ManualOccurrenceCancelled {
			t.Fatalf("occurrence status = %s", occurrence.Status)
		}
	}
	for _, dispatch := range repo.dispatchesFor(plan.ID) {
		if dispatch.Status != core.DeliverySkipped {
			t.Fatalf("dispatch status = %s", dispatch.Status)
		}
	}
	for _, delivery := range repo.deliveriesFor(plan.ID) {
		if delivery.Status != core.DeliverySkipped {
			t.Fatalf("delivery status = %s", delivery.Status)
		}
	}
	if _, err := s.CancelPlan(context.Background(), testActor, plan.ID); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("second cancel error = %v, want ErrConflict", err)
	}
}

func TestMutatingMethodsRequireAuthorization(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	s.now = fixedNow
	plan := seedPlan(repo, "p-auth", 15, "2026-11-15", "Asia/Taipei")
	occurrence := core.ManualOccurrence{
		ID: "o-auth", PlanID: plan.ID, DueDate: "2026-11-15",
		Status: core.ManualOccurrencePending, CreatedAt: fixedNow().UTC(), UpdatedAt: fixedNow().UTC(),
	}
	repo.putOccurrence(occurrence)
	paused := plan
	paused.Status = core.ManualPlanPaused
	repo.putPlan(paused)

	ctx := context.Background()
	calls := []struct {
		name string
		call func() error
	}{
		{"CreatePlan", func() error { _, err := s.CreatePlan(ctx, 7, inputWithDay(15)); return err }},
		{"UpdatePlan", func() error { _, err := s.UpdatePlan(ctx, 7, plan.ID, inputWithDay(15)); return err }},
		{"PausePlan", func() error { _, err := s.PausePlan(ctx, 7, plan.ID); return err }},
		{"ResumePlan", func() error { _, err := s.ResumePlan(ctx, 7, plan.ID); return err }},
		{"CancelPlan", func() error { _, err := s.CancelPlan(ctx, 7, plan.ID); return err }},
		{"ConfirmOccurrence", func() error { _, err := s.ConfirmOccurrence(ctx, 7, occurrence.ID); return err }},
		{"RevokeOccurrence", func() error { _, err := s.RevokeOccurrence(ctx, 7, occurrence.ID); return err }},
	}
	for _, c := range calls {
		if err := c.call(); !errors.Is(err, core.ErrUnauthorized) {
			t.Errorf("%s error = %v, want ErrUnauthorized", c.name, err)
		}
	}
	if repo.transitions != 0 {
		t.Fatalf("unauthorized calls transitioned occurrences %d times", repo.transitions)
	}
	if got, _ := repo.plan(plan.ID); got.Status != core.ManualPlanPaused {
		t.Fatalf("plan state changed to %s", got.Status)
	}
}

func TestCreatePlanHonorsContextCancellation(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	s.now = fixedNow
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.CreatePlan(ctx, testActor, inputWithDay(15)); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestCreatePlanStoresAmountSnapshot(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	s.now = fixedNow
	in := inputWithDay(15)
	amount := core.Money{Minor: 1299, Currency: "USD"}
	in.Amount = &amount
	plan := mustCreatePlan(t, s, testActor, in)
	amount.Minor = 9999
	if plan.Amount == nil || plan.Amount.Minor != 1299 {
		t.Fatalf("amount snapshot = %+v", plan.Amount)
	}
	if stored, ok := repo.plan(plan.ID); !ok || stored.Amount == nil || stored.Amount.Minor != 1299 {
		t.Fatalf("stored amount = %+v", stored.Amount)
	}
}

func TestPauseResumeNotFound(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	s.now = fixedNow
	if _, err := s.PausePlan(context.Background(), testActor, "missing"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("pause error = %v", err)
	}
	if _, err := s.ResumePlan(context.Background(), testActor, "missing"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("resume error = %v", err)
	}
	if _, err := s.CancelPlan(context.Background(), testActor, "missing"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("cancel error = %v", err)
	}
}

func TestEmptyPlanIDIsInvalid(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	s.now = fixedNow
	for _, call := range []func() error{
		func() error {
			_, err := s.UpdatePlan(context.Background(), testActor, " ", inputWithDay(15))
			return err
		},
		func() error { _, err := s.PausePlan(context.Background(), testActor, " "); return err },
		func() error { _, err := s.ResumePlan(context.Background(), testActor, " "); return err },
		func() error { _, err := s.CancelPlan(context.Background(), testActor, " "); return err },
	} {
		if err := call(); !errors.Is(err, core.ErrInvalid) {
			t.Fatalf("error = %v, want ErrInvalid", err)
		}
	}
}

func TestResumeRejectsEmptyTimezone(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	s.now = fixedNow
	plan := seedPlan(repo, "p-tz-empty", 15, "2026-08-15", "")
	plan.Status = core.ManualPlanPaused
	repo.putPlan(plan)
	if _, err := s.ResumePlan(context.Background(), testActor, plan.ID); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}

func TestResumeUsesPlanTimezone(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	// 2026-11-15T20:00:00Z is already 2026-11-16 in Asia/Taipei.
	s.now = func() time.Time { return time.Date(2026, 11, 15, 20, 0, 0, 0, time.UTC) }
	plan := seedPlan(repo, "p-tz", 15, "2026-08-15", "Asia/Taipei")
	plan.Status = core.ManualPlanPaused
	repo.putPlan(plan)

	resumed, err := s.ResumePlan(context.Background(), testActor, plan.ID)
	if err != nil {
		t.Fatalf("ResumePlan: %v", err)
	}
	if resumed.NextDueDate != "2026-12-15" {
		t.Fatalf("next due = %s, want 2026-12-15 (plan-local today is the 16th)", resumed.NextDueDate)
	}
}

func TestCreatePlanRespectsReminderTimeOnBillingDay(t *testing.T) {
	cases := []struct {
		name string
		now  time.Time
		want core.Date
	}{
		{"before reminder time schedules today", taipeiTime(2026, 11, 15, 8, 59), "2026-11-15"},
		{"exactly at reminder time schedules today", taipeiTime(2026, 11, 15, 9, 0), "2026-11-15"},
		{"after reminder time schedules next month", taipeiTime(2026, 11, 15, 9, 1), "2026-12-15"},
	}
	for _, c := range cases {
		repo := newFakeRepo()
		s := newTestService(repo, testActor)
		s.now = func() time.Time { return c.now }
		plan := mustCreatePlan(t, s, testActor, inputWithDay(15))
		if plan.NextDueDate != c.want {
			t.Errorf("%s: next due = %s, want %s", c.name, plan.NextDueDate, c.want)
		}
		if occurrences := repo.occurrencesFor(plan.ID); len(occurrences) != 0 {
			t.Errorf("%s: creation must not materialize occurrences", c.name)
		}
	}
}

func TestResumePlanAfterReminderTimeSkipsToday(t *testing.T) {
	repo := newFakeRepo()
	plan := core.ManualPlan{
		ID: "plan1", Title: "Hosting", BillingDay: 15, ReminderTime: "09:00",
		Timezone: "Asia/Taipei", Status: core.ManualPlanPaused, NextDueDate: "2026-11-15",
		CreatedAt: fixedNow().UTC(), UpdatedAt: fixedNow().UTC(),
	}
	repo.putPlan(plan)
	s := newTestService(repo, testActor)
	s.now = func() time.Time { return taipeiTime(2026, 11, 15, 10, 0) }
	resumed, err := s.ResumePlan(context.Background(), testActor, plan.ID)
	if err != nil {
		t.Fatalf("ResumePlan: %v", err)
	}
	if resumed.NextDueDate != "2026-12-15" {
		t.Errorf("resumed next due = %s, want 2026-12-15", resumed.NextDueDate)
	}
}

func TestUpdateScheduleAfterReminderTimeSchedulesNextMonth(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	s.now = func() time.Time { return taipeiTime(2026, 11, 15, 10, 0) }
	plan := mustCreatePlan(t, s, testActor, inputWithDay(10))
	updated, err := s.UpdatePlan(context.Background(), testActor, plan.ID, inputWithDay(15))
	if err != nil {
		t.Fatalf("UpdatePlan: %v", err)
	}
	if updated.NextDueDate != "2026-12-15" {
		t.Errorf("updated next due = %s, want 2026-12-15", updated.NextDueDate)
	}
}

func TestUpdateScheduleSkipsExistingOccurrence(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo, testActor)
	s.now = func() time.Time { return taipeiTime(2026, 11, 15, 10, 0) }
	plan := mustCreatePlan(t, s, testActor, inputWithDay(10))
	repo.putOccurrence(core.ManualOccurrence{
		ID: "occ1", PlanID: plan.ID, DueDate: "2026-12-15",
		Status: core.ManualOccurrencePending, CreatedAt: fixedNow().UTC(), UpdatedAt: fixedNow().UTC(),
	})
	updated, err := s.UpdatePlan(context.Background(), testActor, plan.ID, inputWithDay(15))
	if err != nil {
		t.Fatalf("UpdatePlan: %v", err)
	}
	if updated.NextDueDate != "2027-01-15" {
		t.Errorf("updated next due = %s, want 2027-01-15", updated.NextDueDate)
	}
}

func TestResumeSkipsExistingOccurrence(t *testing.T) {
	repo := newFakeRepo()
	plan := core.ManualPlan{
		ID: "plan1", Title: "Hosting", BillingDay: 15, ReminderTime: "09:00",
		Timezone: "Asia/Taipei", Status: core.ManualPlanPaused, NextDueDate: "2026-11-15",
		CreatedAt: fixedNow().UTC(), UpdatedAt: fixedNow().UTC(),
	}
	repo.putPlan(plan)
	repo.putOccurrence(core.ManualOccurrence{
		ID: "occ1", PlanID: plan.ID, DueDate: "2026-12-15",
		Status: core.ManualOccurrencePending, CreatedAt: fixedNow().UTC(), UpdatedAt: fixedNow().UTC(),
	})
	s := newTestService(repo, testActor)
	s.now = func() time.Time { return taipeiTime(2026, 11, 15, 10, 0) }
	resumed, err := s.ResumePlan(context.Background(), testActor, plan.ID)
	if err != nil {
		t.Fatalf("ResumePlan: %v", err)
	}
	if resumed.NextDueDate != "2027-01-15" {
		t.Errorf("resumed next due = %s, want 2027-01-15", resumed.NextDueDate)
	}
}
