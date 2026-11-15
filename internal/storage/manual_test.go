package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"email-invoice-guard/internal/core"
)

func manualPlanFixture(id string) core.ManualPlan {
	amount := core.Money{Minor: 1299, Currency: "USD"}
	return core.ManualPlan{
		ID:           id,
		Title:        "Rent",
		ProviderName: "Landlord",
		Description:  "monthly rent",
		Amount:       &amount,
		BillingDay:   15,
		ReminderTime: "09:00",
		Timezone:     "Asia/Taipei",
		PaymentURL:   "https://example.com/pay",
		Notes:        "note",
		Status:       core.ManualPlanActive,
		NextDueDate:  core.Date("2026-12-15"),
	}
}

func mustManualPlan(t *testing.T, s *Store, id string) core.ManualPlan {
	t.Helper()
	p, err := s.CreatePlan(context.Background(), manualPlanFixture(id),
		core.ManualReminderEvent{EventType: core.ManualEventPlanCreated})
	if err != nil {
		t.Fatalf("create manual plan: %v", err)
	}
	return p
}

func mustManualOccurrence(t *testing.T, s *Store, planID string, due, next core.Date) core.ManualOccurrence {
	t.Helper()
	created, err := s.CreateOccurrence(context.Background(), core.ManualOccurrence{PlanID: planID, DueDate: due}, next)
	if err != nil {
		t.Fatalf("create occurrence %s: %v", due, err)
	}
	if !created {
		t.Fatalf("occurrence %s was not created", due)
	}
	return manualOccurrenceByDue(t, s, planID, due)
}

func manualOccurrenceByDue(t *testing.T, s *Store, planID string, due core.Date) core.ManualOccurrence {
	t.Helper()
	occs, err := s.ListOccurrences(context.Background(), core.ManualOccurrenceFilter{PlanID: planID})
	if err != nil {
		t.Fatalf("list occurrences: %v", err)
	}
	for _, o := range occs {
		if o.DueDate == due {
			return o
		}
	}
	t.Fatalf("occurrence due %s not found", due)
	return core.ManualOccurrence{}
}

func mustManualDispatch(t *testing.T, s *Store, planID string, occIDs []string, localDate core.Date) core.ManualDispatch {
	t.Helper()
	created, err := s.CreateDispatch(context.Background(), planID, occIDs,
		core.ManualDispatch{LocalDate: localDate, Kind: core.ManualDispatchDailySummary},
		core.Delivery{
			Payload:     core.DeliveryPayload{Kind: "manual", ManualPlanID: planID, ManualOccurrenceIDs: occIDs},
			AvailableAt: time.Now().Add(-time.Minute),
		})
	if err != nil {
		t.Fatalf("create dispatch: %v", err)
	}
	if !created {
		t.Fatalf("dispatch %s was not created", localDate)
	}
	dispatches, err := s.ListPlanDispatches(context.Background(), planID)
	if err != nil {
		t.Fatalf("list dispatches: %v", err)
	}
	for _, d := range dispatches {
		if d.LocalDate == localDate {
			return d
		}
	}
	t.Fatalf("dispatch %s not found", localDate)
	return core.ManualDispatch{}
}

func TestManualMigrationFreshDatabase(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	rows, err := s.DB().QueryContext(ctx, `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	var versions []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		versions = append(versions, v)
	}
	rows.Close()
	if !reflect.DeepEqual(versions, []int{1, 2}) {
		t.Fatalf("schema versions = %v, want [1 2]", versions)
	}
	for _, table := range []string{
		"manual_reminder_plans", "manual_reminder_occurrences", "manual_reminder_dispatches",
		"manual_reminder_dispatch_occurrences", "manual_reminder_events",
	} {
		var name string
		if err := s.DB().QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name); err != nil {
			t.Fatalf("table %s: %v", table, err)
		}
	}
}

// createV1Database builds a database that has only migration 0001 applied, as
// an existing deployment would before upgrading to v2.
func createV1Database(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", dsn(path, ""))
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	body, err := fs.ReadFile(migrationFS, "migrations/0001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(body)); err != nil {
		t.Fatalf("apply v1 schema: %v", err)
	}
	now := fmtTime(time.Now())
	if _, err := db.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (1, ?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO mail_accounts
		(id, provider, address, owner_user_id, enabled, last_error, created_at, updated_at)
		VALUES ('acc-v1', 'microsoft', 'v1@example.com', 7, 1, '', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
}

func TestManualMigrationUpgradesV1Database(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v1.db")
	createV1Database(t, path)

	s, err := Open(ctx, path, testKey)
	if err != nil {
		t.Fatalf("open upgraded store: %v", err)
	}
	defer s.Close()

	var applied int
	if err := s.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_migrations WHERE version = 2`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 1 {
		t.Fatalf("migration 2 applied %d times", applied)
	}
	// Existing v1 data survives the additive upgrade.
	acct, err := s.GetAccount(ctx, "acc-v1")
	if err != nil {
		t.Fatalf("v1 account lost: %v", err)
	}
	if acct.Address != "v1@example.com" {
		t.Fatalf("v1 account = %+v", acct)
	}
	// The new tables are usable immediately.
	mustManualPlan(t, s, "plan-upgrade")
}

func TestManualMigrationReopenIsIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "reopen.db")
	s, err := Open(ctx, path, testKey)
	if err != nil {
		t.Fatal(err)
	}
	plan := mustManualPlan(t, s, "plan-reopen")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(ctx, path, testKey)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	var count int
	if err := s2.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("schema_migrations rows = %d, want 2", count)
	}
	got, err := s2.GetPlan(ctx, plan.ID)
	if err != nil {
		t.Fatalf("plan lost after reopen: %v", err)
	}
	if got.Title != plan.Title || got.NextDueDate != plan.NextDueDate {
		t.Fatalf("reopened plan = %+v", got)
	}
}

func TestCreatePlanRoundTripAndEvent(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	plan := mustManualPlan(t, s, "plan-roundtrip")

	got, err := s.GetPlan(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Rent" || got.ProviderName != "Landlord" || got.BillingDay != 15 ||
		got.ReminderTime != "09:00" || got.Timezone != "Asia/Taipei" ||
		got.Status != core.ManualPlanActive || got.NextDueDate != "2026-12-15" {
		t.Fatalf("plan = %+v", got)
	}
	if got.Amount == nil || *got.Amount != (core.Money{Minor: 1299, Currency: "USD"}) {
		t.Fatalf("amount = %+v", got.Amount)
	}
	if got.CancelledAt != nil {
		t.Fatalf("cancelled_at = %v", got.CancelledAt)
	}

	events, err := s.ManualEvents(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].EventType != core.ManualEventPlanCreated {
		t.Fatalf("events = %+v", events)
	}

	// A plan without an amount stores NULL money columns.
	noAmount := manualPlanFixture("plan-no-amount")
	noAmount.Amount = nil
	if _, err := s.CreatePlan(ctx, noAmount, core.ManualReminderEvent{}); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetPlan(ctx, "plan-no-amount")
	if err != nil {
		t.Fatal(err)
	}
	if got.Amount != nil {
		t.Fatalf("amount = %+v, want nil", got.Amount)
	}

	// Duplicate primary key is a conflict.
	if _, err := s.CreatePlan(ctx, manualPlanFixture("plan-roundtrip"), core.ManualReminderEvent{}); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("duplicate plan = %v", err)
	}
}

func TestCreatePlanValidation(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	cases := map[string]func(*core.ManualPlan){
		"empty title":        func(p *core.ManualPlan) { p.Title = "  " },
		"billing day low":    func(p *core.ManualPlan) { p.BillingDay = 0 },
		"billing day high":   func(p *core.ManualPlan) { p.BillingDay = 32 },
		"empty time":         func(p *core.ManualPlan) { p.ReminderTime = "" },
		"empty timezone":     func(p *core.ManualPlan) { p.Timezone = "" },
		"bad status":         func(p *core.ManualPlan) { p.Status = "BOGUS" },
		"bad date":           func(p *core.ManualPlan) { p.NextDueDate = "2026-13-40" },
		"empty date":         func(p *core.ManualPlan) { p.NextDueDate = "" },
		"amount no currency": func(p *core.ManualPlan) { p.Amount = &core.Money{Minor: 1} },
	}
	for name, mutate := range cases {
		plan := manualPlanFixture("plan-invalid-" + name)
		mutate(&plan)
		if _, err := s.CreatePlan(ctx, plan, core.ManualReminderEvent{}); !errors.Is(err, core.ErrInvalid) {
			t.Fatalf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	// Invalid event metadata is rejected before anything is written.
	bad := manualPlanFixture("plan-bad-metadata")
	if _, err := s.CreatePlan(ctx, bad, core.ManualReminderEvent{Metadata: "{not json"}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("bad metadata = %v", err)
	}
	if _, err := s.GetPlan(ctx, "plan-bad-metadata"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("rejected plan was written: %v", err)
	}
}

func TestUpdatePlanRules(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if _, err := s.UpdatePlan(ctx, manualPlanFixture("missing"), core.ManualReminderEvent{}); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("missing update = %v", err)
	}

	plan := mustManualPlan(t, s, "plan-update")
	updated := plan
	updated.Title = "Rent 2027"
	updated.NextDueDate = "2027-01-15"
	got, err := s.UpdatePlan(ctx, updated, core.ManualReminderEvent{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Rent 2027" || got.NextDueDate != "2027-01-15" || got.Status != core.ManualPlanActive {
		t.Fatalf("updated plan = %+v", got)
	}
	stored, err := s.GetPlan(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Title != "Rent 2027" {
		t.Fatalf("stored title = %q", stored.Title)
	}
	events, err := s.ManualEvents(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[1].EventType != core.ManualEventPlanUpdated {
		t.Fatalf("events = %+v", events)
	}

	if _, err := s.CancelPlan(ctx, plan.ID, 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdatePlan(ctx, updated, core.ManualReminderEvent{}); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("update after cancel = %v", err)
	}
}

func TestPauseResumeCancelLifecycle(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	plan := mustManualPlan(t, s, "plan-life")

	paused, err := s.PausePlan(ctx, plan.ID, 9, time.Date(2026, 12, 1, 8, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if paused.Status != core.ManualPlanPaused {
		t.Fatalf("pause status = %s", paused.Status)
	}
	if _, err := s.PausePlan(ctx, plan.ID, 9, time.Now()); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("second pause = %v", err)
	}

	resumed, err := s.ResumePlan(ctx, plan.ID, "2027-01-15", 9, time.Date(2026, 12, 2, 8, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != core.ManualPlanActive || resumed.NextDueDate != "2027-01-15" {
		t.Fatalf("resume = %+v", resumed)
	}
	if _, err := s.ResumePlan(ctx, plan.ID, "2027-02-15", 9, time.Now()); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("second resume = %v", err)
	}

	occ := mustManualOccurrence(t, s, plan.ID, "2027-01-15", "2027-02-15")
	dispatch := mustManualDispatch(t, s, plan.ID, []string{occ.ID}, "2027-01-15")

	cancelled, err := s.CancelPlan(ctx, plan.ID, 9, time.Date(2026, 12, 3, 8, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != core.ManualPlanCancelled || cancelled.CancelledAt == nil {
		t.Fatalf("cancel = %+v", cancelled)
	}
	gotOcc, err := s.GetOccurrence(ctx, occ.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotOcc.Status != core.ManualOccurrenceCancelled {
		t.Fatalf("occurrence status = %s", gotOcc.Status)
	}
	var outboxStatus string
	if err := s.DB().QueryRowContext(ctx, `SELECT status FROM delivery_outbox WHERE id = ?`, dispatch.OutboxID).Scan(&outboxStatus); err != nil {
		t.Fatal(err)
	}
	if outboxStatus != string(core.DeliverySkipped) {
		t.Fatalf("outbox status = %s", outboxStatus)
	}
	gotDispatch, err := s.GetDispatch(ctx, dispatch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotDispatch.Status != core.DeliverySkipped {
		t.Fatalf("dispatch status = %s", gotDispatch.Status)
	}
	if _, err := s.CancelPlan(ctx, plan.ID, 9, time.Now()); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("second cancel = %v", err)
	}

	events, err := s.ManualEvents(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, ev := range events {
		seen[ev.EventType] = true
	}
	for _, want := range []string{
		core.ManualEventPlanCreated, core.ManualEventPlanPaused, core.ManualEventPlanResumed,
		core.ManualEventOccurrenceCreated, core.ManualEventOccurrenceCancelled, core.ManualEventPlanCancelled,
	} {
		if !seen[want] {
			t.Fatalf("missing event %s in %+v", want, events)
		}
	}
}

func TestCreateOccurrenceCASAndDuplicates(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	plan := mustManualPlan(t, s, "plan-occ")

	occ := mustManualOccurrence(t, s, plan.ID, "2026-12-15", "2027-01-15")
	if occ.Status != core.ManualOccurrencePending || occ.NotifiedAt != nil || occ.ConfirmedAt != nil {
		t.Fatalf("occurrence = %+v", occ)
	}
	if occ.Amount == nil || *occ.Amount != *plan.Amount {
		t.Fatalf("amount snapshot = %+v", occ.Amount)
	}
	advanced, err := s.GetPlan(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if advanced.NextDueDate != "2027-01-15" {
		t.Fatalf("next due = %s", advanced.NextDueDate)
	}

	// The CAS no longer matches the old due date.
	if created, err := s.CreateOccurrence(ctx, core.ManualOccurrence{PlanID: plan.ID, DueDate: "2026-12-15"}, "2027-02-15"); err != nil || created {
		t.Fatalf("stale CAS = %v %v", created, err)
	}
	// Duplicate (plan, due date) is a no-op even if the plan is rewound.
	if _, err := s.DB().ExecContext(ctx, `UPDATE manual_reminder_plans SET next_due_date = '2026-12-15' WHERE id = ?`, plan.ID); err != nil {
		t.Fatal(err)
	}
	if created, err := s.CreateOccurrence(ctx, core.ManualOccurrence{PlanID: plan.ID, DueDate: "2026-12-15"}, "2027-02-15"); err != nil || created {
		t.Fatalf("duplicate occurrence = %v %v", created, err)
	}

	// A paused plan never materializes occurrences.
	if _, err := s.PausePlan(ctx, plan.ID, 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	if created, err := s.CreateOccurrence(ctx, core.ManualOccurrence{PlanID: plan.ID, DueDate: "2027-01-15"}, "2027-02-15"); err != nil || created {
		t.Fatalf("paused plan occurrence = %v %v", created, err)
	}

	if _, err := s.CreateOccurrence(ctx, core.ManualOccurrence{PlanID: "missing", DueDate: "2027-01-15"}, "2027-02-15"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("missing plan = %v", err)
	}
	if _, err := s.CreateOccurrence(ctx, core.ManualOccurrence{PlanID: plan.ID, DueDate: "bad"}, "2027-02-15"); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("bad date = %v", err)
	}
	if _, err := s.CreateOccurrence(ctx, core.ManualOccurrence{PlanID: plan.ID, DueDate: "2027-01-15"}, "bad"); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("bad next date = %v", err)
	}
}

func TestCreateDispatchLinksOnlyPending(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	plan := mustManualPlan(t, s, "plan-disp")
	occ1 := mustManualOccurrence(t, s, plan.ID, "2026-12-15", "2027-01-15")
	occ2 := mustManualOccurrence(t, s, plan.ID, "2027-01-15", "2027-02-15")
	if _, err := s.TransitionOccurrence(ctx, occ2.ID, core.ManualOccurrencePending, core.ManualOccurrencePaid, 3, time.Now()); err != nil {
		t.Fatal(err)
	}

	created, err := s.CreateDispatch(ctx, plan.ID, []string{occ1.ID, occ2.ID, "missing"},
		core.ManualDispatch{LocalDate: "2026-12-15"},
		core.Delivery{Payload: core.DeliveryPayload{Kind: "manual", ManualPlanID: plan.ID}})
	if err != nil || !created {
		t.Fatalf("dispatch = %v %v", created, err)
	}
	dispatches, err := s.ListPlanDispatches(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(dispatches) != 1 {
		t.Fatalf("dispatches = %d", len(dispatches))
	}
	d := dispatches[0]
	if d.Kind != core.ManualDispatchDailySummary || d.Status != core.DeliveryPending {
		t.Fatalf("dispatch = %+v", d)
	}
	if !reflect.DeepEqual(d.OccurrenceIDs, []string{occ1.ID}) {
		t.Fatalf("linked occurrences = %v", d.OccurrenceIDs)
	}
	pendingOcc, err := s.GetOccurrence(ctx, occ1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pendingOcc.NotifiedAt != nil {
		t.Fatalf("dispatch creation marked notified: %v", pendingOcc.NotifiedAt)
	}
	paid, err := s.GetOccurrence(ctx, occ2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if paid.NotifiedAt != nil {
		t.Fatalf("paid occurrence was notified: %v", paid.NotifiedAt)
	}

	var dedup, status string
	if err := s.DB().QueryRowContext(ctx, `SELECT dedup_key, status FROM delivery_outbox WHERE id = ?`, d.OutboxID).Scan(&dedup, &status); err != nil {
		t.Fatal(err)
	}
	if dedup != "manual-reminder:plan-disp:2026-12-15" || status != string(core.DeliveryPending) {
		t.Fatalf("outbox = %s %s", dedup, status)
	}

	// Same (plan, local date, kind) is a duplicate.
	if created, err := s.CreateDispatch(ctx, plan.ID, []string{occ1.ID},
		core.ManualDispatch{LocalDate: "2026-12-15"},
		core.Delivery{Payload: core.DeliveryPayload{Kind: "manual", ManualPlanID: plan.ID}}); err != nil || created {
		t.Fatalf("duplicate dispatch = %v %v", created, err)
	}
	// No pending occurrence means no dispatch and no outbox row.
	if created, err := s.CreateDispatch(ctx, plan.ID, []string{occ2.ID},
		core.ManualDispatch{LocalDate: "2027-01-15"},
		core.Delivery{Payload: core.DeliveryPayload{Kind: "manual", ManualPlanID: plan.ID}}); err != nil || created {
		t.Fatalf("paid-only dispatch = %v %v", created, err)
	}
	var outboxCount int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM delivery_outbox`).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if outboxCount != 1 {
		t.Fatalf("outbox rows = %d, want 1", outboxCount)
	}

	if _, err := s.CreateDispatch(ctx, plan.ID, []string{occ1.ID},
		core.ManualDispatch{LocalDate: "2026-12-16", Kind: "WEEKLY"},
		core.Delivery{Payload: core.DeliveryPayload{Kind: "manual"}}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("bad kind = %v", err)
	}
	if _, err := s.CreateDispatch(ctx, "missing", []string{occ1.ID},
		core.ManualDispatch{LocalDate: "2026-12-16"},
		core.Delivery{Payload: core.DeliveryPayload{Kind: "manual"}}); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("missing plan = %v", err)
	}
}

func TestCreateDispatchOutboxIDUnique(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	plan := mustManualPlan(t, s, "plan-outbox-unique")
	occ1 := mustManualOccurrence(t, s, plan.ID, "2026-12-15", "2027-01-15")
	dispatch := mustManualDispatch(t, s, plan.ID, []string{occ1.ID}, "2026-12-15")

	// Reusing an existing outbox ID through a different dedup key violates the
	// outbox primary key and must roll the whole dispatch back.
	_, err := s.CreateDispatch(ctx, plan.ID, []string{occ1.ID},
		core.ManualDispatch{LocalDate: "2026-12-16"},
		core.Delivery{ID: dispatch.OutboxID, DedupKey: "manual-reminder:plan-outbox-unique:2026-12-16"})
	if !errors.Is(err, core.ErrConflict) {
		t.Fatalf("duplicate outbox id = %v", err)
	}
	var count int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM manual_reminder_dispatches`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("dispatches = %d, want 1 after rollback", count)
	}
}

func TestTransitionOccurrenceCAS(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	plan := mustManualPlan(t, s, "plan-transition")
	occ := mustManualOccurrence(t, s, plan.ID, "2026-12-15", "2027-01-15")
	at := time.Date(2026, 12, 10, 3, 4, 5, 0, time.UTC)

	paid, err := s.TransitionOccurrence(ctx, occ.ID, core.ManualOccurrencePending, core.ManualOccurrencePaid, 5, at)
	if err != nil {
		t.Fatal(err)
	}
	if paid.Status != core.ManualOccurrencePaid || paid.ConfirmedBy == nil || *paid.ConfirmedBy != 5 ||
		paid.ConfirmedAt == nil || !paid.ConfirmedAt.Equal(at) {
		t.Fatalf("paid = %+v", paid)
	}
	events, err := s.ManualEvents(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	paidEvents := 0
	for _, ev := range events {
		if ev.EventType == core.ManualEventPaymentConfirmed {
			paidEvents++
		}
	}
	if paidEvents != 1 {
		t.Fatalf("paid events = %d", paidEvents)
	}

	// Same-target replay is idempotent and adds no event.
	beforeReplay, err := s.ManualEvents(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.TransitionOccurrence(ctx, occ.ID, core.ManualOccurrencePaid, core.ManualOccurrencePaid, 5, at)
	if err != nil {
		t.Fatal(err)
	}
	if replay.Status != core.ManualOccurrencePaid {
		t.Fatalf("replay = %+v", replay)
	}
	events, err = s.ManualEvents(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != len(beforeReplay) {
		t.Fatalf("replay added an event: %+v", events)
	}

	// A mismatched source state is a conflict.
	if _, err := s.TransitionOccurrence(ctx, occ.ID, core.ManualOccurrencePending, core.ManualOccurrenceCancelled, 5, at); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("mismatched transition = %v", err)
	}

	revoked, err := s.TransitionOccurrence(ctx, occ.ID, core.ManualOccurrencePaid, core.ManualOccurrencePending, 5, at)
	if err != nil {
		t.Fatal(err)
	}
	if revoked.Status != core.ManualOccurrencePending || revoked.ConfirmedBy != nil || revoked.ConfirmedAt != nil {
		t.Fatalf("revoked = %+v", revoked)
	}
	events, err = s.ManualEvents(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if events[len(events)-1].EventType != core.ManualEventPaymentConfirmationRevoked {
		t.Fatalf("last event = %s", events[len(events)-1].EventType)
	}

	if _, err := s.TransitionOccurrence(ctx, "missing", core.ManualOccurrencePending, core.ManualOccurrencePaid, 1, at); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("missing occurrence = %v", err)
	}
	if _, err := s.TransitionOccurrence(ctx, occ.ID, "BOGUS", core.ManualOccurrencePaid, 1, at); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("bad status = %v", err)
	}
}

func TestListPlansAndOccurrencesFilters(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	p1 := mustManualPlan(t, s, "plan-list-a")
	p2 := mustManualPlan(t, s, "plan-list-b")
	p3 := mustManualPlan(t, s, "plan-list-c")
	if _, err := s.PausePlan(ctx, p2.ID, 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	plans, err := s.ListPlans(ctx, core.ManualPlanFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 3 {
		t.Fatalf("plans = %d", len(plans))
	}
	paused, err := s.ListPlans(ctx, core.ManualPlanFilter{Status: core.ManualPlanPaused})
	if err != nil {
		t.Fatal(err)
	}
	if len(paused) != 1 || paused[0].ID != p2.ID {
		t.Fatalf("paused plans = %+v", paused)
	}
	limited, err := s.ListPlans(ctx, core.ManualPlanFilter{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 1 {
		t.Fatalf("limited plans = %d", len(limited))
	}
	if _, err := s.ListPlans(ctx, core.ManualPlanFilter{Status: "BOGUS"}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("bad status filter = %v", err)
	}

	// Three occurrences on p1 with increasing due dates.
	mustManualOccurrence(t, s, p1.ID, "2026-12-15", "2027-01-15")
	mustManualOccurrence(t, s, p1.ID, "2027-01-15", "2027-02-15")
	occ3 := mustManualOccurrence(t, s, p1.ID, "2027-02-15", "2027-03-15")
	if _, err := s.TransitionOccurrence(ctx, occ3.ID, core.ManualOccurrencePending, core.ManualOccurrencePaid, 2, time.Now()); err != nil {
		t.Fatal(err)
	}
	all, err := s.ListOccurrences(ctx, core.ManualOccurrenceFilter{PlanID: p1.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("occurrences = %d", len(all))
	}
	byDate, err := s.ListOccurrences(ctx, core.ManualOccurrenceFilter{PlanID: p1.ID, DueOnOrBefore: "2027-01-15"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byDate) != 2 {
		t.Fatalf("due on or before = %d", len(byDate))
	}
	paid, err := s.ListOccurrences(ctx, core.ManualOccurrenceFilter{PlanID: p1.ID, Status: core.ManualOccurrencePaid})
	if err != nil {
		t.Fatal(err)
	}
	if len(paid) != 1 || paid[0].ID != occ3.ID {
		t.Fatalf("paid occurrences = %+v", paid)
	}
	one, err := s.ListOccurrences(ctx, core.ManualOccurrenceFilter{PlanID: p1.ID, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(one) != 1 || one[0].DueDate != "2026-12-15" {
		t.Fatalf("limited occurrences = %+v", one)
	}
	if _, err := s.ListOccurrences(ctx, core.ManualOccurrenceFilter{DueOnOrBefore: "bad"}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("bad date filter = %v", err)
	}
	_ = p3
}

func TestListManualMessagesOrdered(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	plan := mustManualPlan(t, s, "plan-messages")
	occ := mustManualOccurrence(t, s, plan.ID, "2026-12-15", "2027-01-15")
	dispatch := mustManualDispatch(t, s, plan.ID, []string{occ.ID}, "2026-12-15")

	for _, part := range []int{1, 0} {
		if err := s.RecordTelegramMessage(ctx, core.TelegramMessage{
			OutboxID: dispatch.OutboxID, ChatID: 7, MessageID: int64(500 + part), Part: part,
		}); err != nil {
			t.Fatal(err)
		}
	}
	messages, err := s.ListManualMessages(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Part != 0 || messages[1].Part != 1 {
		t.Fatalf("messages = %+v", messages)
	}

	other := mustManualPlan(t, s, "plan-other")
	otherOcc := mustManualOccurrence(t, s, other.ID, "2026-12-15", "2027-01-15")
	otherDispatch := mustManualDispatch(t, s, other.ID, []string{otherOcc.ID}, "2026-12-15")
	if err := s.RecordTelegramMessage(ctx, core.TelegramMessage{
		OutboxID: otherDispatch.OutboxID, ChatID: 7, MessageID: 900, Part: 0,
	}); err != nil {
		t.Fatal(err)
	}
	messages, err = s.ListManualMessages(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages leaked across plans: %+v", messages)
	}
}

func TestManualEventsInsertionOrder(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	plan := mustManualPlan(t, s, "plan-events")
	if _, err := s.PausePlan(ctx, plan.ID, 1, time.Date(2026, 12, 5, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	// A caller-supplied instant earlier than the pause must not reorder events.
	if _, err := s.ResumePlan(ctx, plan.ID, "2027-01-15", 1, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	events, err := s.ManualEvents(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{core.ManualEventPlanCreated, core.ManualEventPlanPaused, core.ManualEventPlanResumed}
	if len(events) != len(want) {
		t.Fatalf("events = %+v", events)
	}
	for i, w := range want {
		if events[i].EventType != w {
			t.Fatalf("event %d = %s, want %s", i, events[i].EventType, w)
		}
	}
}

func TestManualGetNotFound(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if _, err := s.GetPlan(ctx, "missing"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("plan = %v", err)
	}
	if _, err := s.GetOccurrence(ctx, "missing"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("occurrence = %v", err)
	}
	if _, err := s.GetDispatch(ctx, "missing"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("dispatch = %v", err)
	}
}

func TestManualMethodsInsideTransaction(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	var planID, dispatchID string
	err := s.Transaction(ctx, func(repo core.Repository) error {
		mr, ok := repo.(core.ManualReminderRepository)
		if !ok {
			t.Fatal("transaction repository does not implement ManualReminderRepository")
		}
		plan, err := mr.CreatePlan(ctx, manualPlanFixture("plan-tx"), core.ManualReminderEvent{})
		if err != nil {
			return err
		}
		planID = plan.ID
		created, err := mr.CreateOccurrence(ctx, core.ManualOccurrence{PlanID: plan.ID, DueDate: "2026-12-15"}, "2027-01-15")
		if err != nil {
			return err
		}
		if !created {
			t.Fatal("occurrence not created inside transaction")
		}
		occs, err := mr.ListOccurrences(ctx, core.ManualOccurrenceFilter{PlanID: plan.ID})
		if err != nil {
			return err
		}
		ok, err = mr.CreateDispatch(ctx, plan.ID, []string{occs[0].ID},
			core.ManualDispatch{LocalDate: "2026-12-15"},
			core.Delivery{Payload: core.DeliveryPayload{Kind: "manual", ManualPlanID: plan.ID}})
		if err != nil {
			return err
		}
		if !ok {
			t.Fatal("dispatch not created inside transaction")
		}
		dispatches, err := mr.ListPlanDispatches(ctx, plan.ID)
		if err != nil {
			return err
		}
		dispatchID = dispatches[0].ID
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetPlan(ctx, planID); err != nil {
		t.Fatalf("committed plan: %v", err)
	}
	if _, err := s.GetDispatch(ctx, dispatchID); err != nil {
		t.Fatalf("committed dispatch: %v", err)
	}

	rollback := errors.New("rollback")
	err = s.Transaction(ctx, func(repo core.Repository) error {
		mr := repo.(core.ManualReminderRepository)
		if _, err := mr.CreatePlan(ctx, manualPlanFixture("plan-rollback"), core.ManualReminderEvent{}); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("transaction error = %v", err)
	}
	if _, err := s.GetPlan(ctx, "plan-rollback"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("rolled back plan visible: %v", err)
	}
}

func countManualEvents(t *testing.T, s *Store, planID, eventType string) int {
	t.Helper()
	events, err := s.ManualEvents(context.Background(), planID)
	if err != nil {
		t.Fatalf("manual events: %v", err)
	}
	count := 0
	for _, ev := range events {
		if ev.EventType == eventType {
			count++
		}
	}
	return count
}

func TestCompleteDeliveryMarksNotifiedAndWritesReminderSent(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	plan := mustManualPlan(t, s, "plan-notify")
	occ1 := mustManualOccurrence(t, s, plan.ID, "2026-12-15", "2027-01-15")
	occ2 := mustManualOccurrence(t, s, plan.ID, "2027-01-15", "2027-02-15")
	dispatch := mustManualDispatch(t, s, plan.ID, []string{occ1.ID, occ2.ID}, "2026-12-15")

	// Enqueueing a summary must not mark anything notified; only delivery does.
	for _, id := range []string{occ1.ID, occ2.ID} {
		o, err := s.GetOccurrence(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if o.NotifiedAt != nil {
			t.Fatalf("occurrence %s notified at dispatch creation: %v", id, o.NotifiedAt)
		}
	}
	// occ2 is paid after the dispatch was queued; delivery must not touch it.
	if _, err := s.TransitionOccurrence(ctx, occ2.ID, core.ManualOccurrencePending, core.ManualOccurrencePaid, 4, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimDelivery(ctx, time.Now(), time.Minute); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 12, 15, 1, 2, 3, 0, time.UTC)
	if err := s.CompleteDelivery(ctx, dispatch.OutboxID, nil, at); err != nil {
		t.Fatal(err)
	}

	notified, err := s.GetOccurrence(ctx, occ1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if notified.NotifiedAt == nil || !notified.NotifiedAt.Equal(at) {
		t.Fatalf("pending occurrence notified_at = %v, want %v", notified.NotifiedAt, at)
	}
	paid, err := s.GetOccurrence(ctx, occ2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if paid.NotifiedAt != nil {
		t.Fatalf("paid occurrence was marked notified: %v", paid.NotifiedAt)
	}

	events, err := s.ManualEvents(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	var reminderSent []core.ManualReminderEvent
	for _, ev := range events {
		if ev.EventType == core.ManualEventReminderSent {
			reminderSent = append(reminderSent, ev)
		}
	}
	if len(reminderSent) != 1 {
		t.Fatalf("REMINDER_SENT events = %d, want 1", len(reminderSent))
	}
	ev := reminderSent[0]
	if ev.OccurrenceID != "" {
		t.Fatalf("REMINDER_SENT occurrence_id = %q, want empty", ev.OccurrenceID)
	}
	if !ev.CreatedAt.Equal(at) {
		t.Fatalf("REMINDER_SENT created_at = %v, want %v", ev.CreatedAt, at)
	}
	var metadata map[string]string
	if err := json.Unmarshal([]byte(ev.Metadata), &metadata); err != nil {
		t.Fatalf("REMINDER_SENT metadata %q: %v", ev.Metadata, err)
	}
	if metadata["dispatch_id"] != dispatch.ID {
		t.Fatalf("REMINDER_SENT metadata = %s, want dispatch %s", ev.Metadata, dispatch.ID)
	}

	// Replaying completion must not duplicate the audit event.
	if err := s.CompleteDelivery(ctx, dispatch.OutboxID, nil, at); err != nil {
		t.Fatal(err)
	}
	if n := countManualEvents(t, s, plan.ID, core.ManualEventReminderSent); n != 1 {
		t.Fatalf("REMINDER_SENT after replay = %d, want 1", n)
	}
}
