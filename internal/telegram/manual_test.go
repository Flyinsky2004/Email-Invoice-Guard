package telegram

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"email-invoice-guard/internal/core"
)

// fakeManual embeds core.ManualReminderService so only exercised methods need
// behavior; every call is recorded for assertions.
type fakeManual struct {
	core.ManualReminderService

	mu    sync.Mutex
	calls []string

	lastCreateInput core.ManualReminderInput
	lastUpdateInput core.ManualReminderInput
	lastActor       int64
	lastID          string

	createPlan        func(context.Context, int64, core.ManualReminderInput) (core.ManualPlan, error)
	updatePlan        func(context.Context, int64, string, core.ManualReminderInput) (core.ManualPlan, error)
	pausePlan         func(context.Context, int64, string) (core.ManualPlan, error)
	resumePlan        func(context.Context, int64, string) (core.ManualPlan, error)
	cancelPlan        func(context.Context, int64, string) (core.ManualPlan, error)
	confirmOccurrence func(context.Context, int64, string) (core.ManualOccurrence, error)
	revokeOccurrence  func(context.Context, int64, string) (core.ManualOccurrence, error)
	getPlan           func(context.Context, string) (core.ManualPlan, error)
	listPlans         func(context.Context, core.ManualPlanFilter) ([]core.ManualPlan, error)
	getOccurrence     func(context.Context, string) (core.ManualOccurrence, error)
	listOccurrences   func(context.Context, core.ManualOccurrenceFilter) ([]core.ManualOccurrence, error)
	listPlanMessages  func(context.Context, string) ([]core.TelegramMessage, error)
	events            func(context.Context, string) ([]core.ManualReminderEvent, error)
}

func newFakeManual() *fakeManual { return &fakeManual{} }

func (m *fakeManual) record(call string) {
	m.mu.Lock()
	m.calls = append(m.calls, call)
	m.mu.Unlock()
}

func (m *fakeManual) called(call string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.calls {
		if c == call {
			return true
		}
	}
	return false
}

func (m *fakeManual) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *fakeManual) CreatePlan(ctx context.Context, actor int64, input core.ManualReminderInput) (core.ManualPlan, error) {
	m.record("create")
	m.mu.Lock()
	m.lastCreateInput = input
	m.lastActor = actor
	m.mu.Unlock()
	if m.createPlan != nil {
		return m.createPlan(ctx, actor, input)
	}
	return core.ManualPlan{ID: testID(20), Title: input.Title, Status: core.ManualPlanActive, NextDueDate: core.Date("2026-11-15")}, nil
}

func (m *fakeManual) UpdatePlan(ctx context.Context, actor int64, id string, input core.ManualReminderInput) (core.ManualPlan, error) {
	m.record("update")
	m.mu.Lock()
	m.lastUpdateInput = input
	m.lastActor = actor
	m.lastID = id
	m.mu.Unlock()
	if m.updatePlan != nil {
		return m.updatePlan(ctx, actor, id, input)
	}
	return core.ManualPlan{ID: id, Title: input.Title, Status: core.ManualPlanActive}, nil
}

func (m *fakeManual) PausePlan(ctx context.Context, actor int64, id string) (core.ManualPlan, error) {
	m.record("pause")
	if m.pausePlan != nil {
		return m.pausePlan(ctx, actor, id)
	}
	return core.ManualPlan{ID: id, Title: "VPS renewal", Status: core.ManualPlanPaused}, nil
}

func (m *fakeManual) ResumePlan(ctx context.Context, actor int64, id string) (core.ManualPlan, error) {
	m.record("resume")
	if m.resumePlan != nil {
		return m.resumePlan(ctx, actor, id)
	}
	return core.ManualPlan{ID: id, Title: "VPS renewal", Status: core.ManualPlanActive, NextDueDate: core.Date("2026-12-15")}, nil
}

func (m *fakeManual) CancelPlan(ctx context.Context, actor int64, id string) (core.ManualPlan, error) {
	m.record("cancel")
	if m.cancelPlan != nil {
		return m.cancelPlan(ctx, actor, id)
	}
	return core.ManualPlan{ID: id, Title: "VPS renewal", Status: core.ManualPlanCancelled}, nil
}

func (m *fakeManual) ConfirmOccurrence(ctx context.Context, actor int64, id string) (core.ManualOccurrence, error) {
	m.record("confirm")
	m.mu.Lock()
	m.lastActor = actor
	m.lastID = id
	m.mu.Unlock()
	if m.confirmOccurrence != nil {
		return m.confirmOccurrence(ctx, actor, id)
	}
	return core.ManualOccurrence{ID: id, PlanID: testID(20), DueDate: core.Date("2026-11-15"), Status: core.ManualOccurrencePaid}, nil
}

func (m *fakeManual) RevokeOccurrence(ctx context.Context, actor int64, id string) (core.ManualOccurrence, error) {
	m.record("revoke")
	m.mu.Lock()
	m.lastActor = actor
	m.lastID = id
	m.mu.Unlock()
	if m.revokeOccurrence != nil {
		return m.revokeOccurrence(ctx, actor, id)
	}
	return core.ManualOccurrence{ID: id, PlanID: testID(20), DueDate: core.Date("2026-11-15"), Status: core.ManualOccurrencePending}, nil
}

func (m *fakeManual) GetPlan(ctx context.Context, id string) (core.ManualPlan, error) {
	m.record("getPlan")
	if m.getPlan != nil {
		return m.getPlan(ctx, id)
	}
	return core.ManualPlan{}, core.ErrNotFound
}

func (m *fakeManual) ListPlans(ctx context.Context, filter core.ManualPlanFilter) ([]core.ManualPlan, error) {
	m.record("listPlans")
	if m.listPlans != nil {
		return m.listPlans(ctx, filter)
	}
	return nil, nil
}

func (m *fakeManual) GetOccurrence(ctx context.Context, id string) (core.ManualOccurrence, error) {
	m.record("getOccurrence")
	if m.getOccurrence != nil {
		return m.getOccurrence(ctx, id)
	}
	return core.ManualOccurrence{}, core.ErrNotFound
}

func (m *fakeManual) ListOccurrences(ctx context.Context, filter core.ManualOccurrenceFilter) ([]core.ManualOccurrence, error) {
	m.record("listOccurrences")
	if m.listOccurrences != nil {
		return m.listOccurrences(ctx, filter)
	}
	return nil, nil
}

func (m *fakeManual) ListPlanMessages(ctx context.Context, planID string) ([]core.TelegramMessage, error) {
	m.record("listPlanMessages")
	if m.listPlanMessages != nil {
		return m.listPlanMessages(ctx, planID)
	}
	return nil, nil
}

func (m *fakeManual) Events(ctx context.Context, planID string) ([]core.ManualReminderEvent, error) {
	m.record("events")
	if m.events != nil {
		return m.events(ctx, planID)
	}
	return nil, nil
}

func sampleManualPlan() core.ManualPlan {
	amount, _ := core.ParseMoney("18.00", "USD")
	return core.ManualPlan{
		ID:           testID(20),
		Title:        "VPS renewal",
		ProviderName: "Example Host",
		Amount:       &amount,
		BillingDay:   15,
		ReminderTime: "09:00",
		Timezone:     "Asia/Taipei",
		PaymentURL:   "https://billing.example.test/pay/1",
		Status:       core.ManualPlanActive,
		NextDueDate:  core.Date("2026-11-15"),
		CreatedAt:    time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:    time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
	}
}

func sampleManualOccurrence(status core.ManualOccurrenceStatus) core.ManualOccurrence {
	amount, _ := core.ParseMoney("18.00", "USD")
	return core.ManualOccurrence{
		ID:      testID(21),
		PlanID:  testID(20),
		DueDate: core.Date("2026-11-15"),
		Amount:  &amount,
		Status:  status,
	}
}

// manualPlanService returns a fake manual service with the sample plan loaded.
func manualPlanService() *fakeManual {
	plan := sampleManualPlan()
	occ := sampleManualOccurrence(core.ManualOccurrencePending)
	m := newFakeManual()
	m.getPlan = func(context.Context, string) (core.ManualPlan, error) { return plan, nil }
	m.getOccurrence = func(context.Context, string) (core.ManualOccurrence, error) { return occ, nil }
	m.listOccurrences = func(context.Context, core.ManualOccurrenceFilter) ([]core.ManualOccurrence, error) {
		return []core.ManualOccurrence{occ}, nil
	}
	return m
}

func TestNewKeepsSignatureAndDisablesManual(t *testing.T) {
	env := newTestEnv(t)
	if env.bot.manual != nil {
		t.Fatal("New must pass a nil manual service")
	}
	text := sentText(env.runMessage("/reminders"))
	if !strings.Contains(text, "not configured") {
		t.Fatalf("manual command without service = %q", text)
	}
	env.tg.reset()
	text = sentText(env.runMessage("/help"))
	if strings.Contains(text, "/reminder_add") {
		t.Fatalf("help advertises manual commands without a service: %q", text)
	}
	env.tg.reset()
	if text = sentText(env.runMessage("/status")); !strings.Contains(text, "Status") {
		t.Fatalf("existing behavior changed: %q", text)
	}
}

func TestManualCommandsReportNotConfigured(t *testing.T) {
	env := newTestEnv(t)
	for _, command := range []string{
		"/reminders", "/reminder " + testID(20), "/reminder_add", "/reminder_edit " + testID(20),
		"/reminder_pause " + testID(20), "/reminder_resume " + testID(20), "/reminder_delete " + testID(20),
		"/reminder_paid " + testID(21), "/reminder_pending", "/reminder_history " + testID(20),
	} {
		env.tg.reset()
		if text := sentText(env.runMessage(command)); !strings.Contains(text, "not configured") {
			t.Fatalf("%q without service = %q", command, text)
		}
	}
	env.tg.reset()
	answers := env.runCallback(callbackData(cbManualPaid, testID(21)))
	if len(answers) != 1 || !strings.Contains(answers[0].Body["text"].(string), "not configured") {
		t.Fatalf("manual callback without service was not answered: %v", answers)
	}
}

func TestHelpIncludesManualCommandsWhenEnabled(t *testing.T) {
	env := newTestEnv(t)
	env.withManual(newFakeManual())
	env.runMessage("/help")
	env.runCallback(menuCallbackData(menuCommands, "", ""))
	text := sentText(env.tg.callsFor("editMessageText"))
	for _, want := range []string{"/reminder_add", "/reminders", "/reminder_pending", "/cancel"} {
		if !strings.Contains(text, want) {
			t.Fatalf("manual help missing %q: %q", want, text)
		}
	}
}

func TestRemindersCommandListsPlans(t *testing.T) {
	env := newTestEnv(t)
	plan := sampleManualPlan()
	paused := plan
	paused.ID = testID(22)
	paused.Title = "Domain renewal"
	paused.Status = core.ManualPlanPaused
	m := newFakeManual()
	m.listPlans = func(context.Context, core.ManualPlanFilter) ([]core.ManualPlan, error) {
		return []core.ManualPlan{plan, paused}, nil
	}
	env.withManual(m)
	text := sentText(env.runMessage("/reminders"))
	for _, want := range []string{"VPS renewal", "ACTIVE", "2026-11-15", "Domain renewal", "PAUSED", plan.ID} {
		if !strings.Contains(text, want) {
			t.Fatalf("plan list missing %q: %q", want, text)
		}
	}
}

func TestReminderCommandShowsPlanAndPendingOccurrences(t *testing.T) {
	env := newTestEnv(t)
	env.withManual(manualPlanService())
	text := sentText(env.runMessage("/reminder " + testID(20)))
	for _, want := range []string{"VPS renewal", "Example Host", "18.00 USD", "2026-11-15", "PENDING", "billing.example.test"} {
		if !strings.Contains(text, want) {
			t.Fatalf("plan detail missing %q: %q", want, text)
		}
	}
	if !hasCallbackPrefix(env.tg.callsFor("sendMessage"), cbManualPaid+":") {
		t.Fatal("plan detail has no mark-paid button")
	}
}

func TestReminderCommandUsageAndNotFound(t *testing.T) {
	env := newTestEnv(t)
	env.withManual(newFakeManual())
	cases := []struct {
		command string
		want    string
	}{
		{"/reminder", "Usage: /reminder"},
		{"/reminder short", "Usage: /reminder"},
		{"/reminder_edit", "Usage: /reminder_edit"},
		{"/reminder_pause bad", "Usage: /reminder_pause"},
		{"/reminder_resume bad", "Usage: /reminder_resume"},
		{"/reminder_delete bad", "Usage: /reminder_delete"},
		{"/reminder_paid bad", "Usage: /reminder_paid"},
		{"/reminder_history bad", "Usage: /reminder_history"},
		{"/reminder " + testID(20), "not found"},
	}
	for _, tc := range cases {
		env.tg.reset()
		if text := sentText(env.runMessage(tc.command)); !strings.Contains(text, tc.want) {
			t.Fatalf("%q = %q, want %q", tc.command, text, tc.want)
		}
	}
}

func TestReminderPendingListsOrderedByDueDate(t *testing.T) {
	env := newTestEnv(t)
	plan := sampleManualPlan()
	early := sampleManualOccurrence(core.ManualOccurrencePending)
	early.ID = testID(31)
	early.DueDate = core.Date("2026-11-01")
	late := sampleManualOccurrence(core.ManualOccurrencePending)
	late.ID = testID(32)
	late.DueDate = core.Date("2026-12-01")
	m := newFakeManual()
	m.getPlan = func(context.Context, string) (core.ManualPlan, error) { return plan, nil }
	m.listOccurrences = func(context.Context, core.ManualOccurrenceFilter) ([]core.ManualOccurrence, error) {
		return []core.ManualOccurrence{late, early}, nil
	}
	env.withManual(m)
	text := sentText(env.runMessage("/reminder_pending"))
	if strings.Index(text, "2026-11-01") > strings.Index(text, "2026-12-01") {
		t.Fatalf("pending occurrences not ordered by due date: %q", text)
	}
	if !hasCallbackPrefix(env.tg.callsFor("sendMessage"), cbManualPaid+":") {
		t.Fatal("pending list has no mark-paid button")
	}
}

func TestReminderHistoryListsEvents(t *testing.T) {
	env := newTestEnv(t)
	plan := sampleManualPlan()
	m := newFakeManual()
	m.getPlan = func(context.Context, string) (core.ManualPlan, error) { return plan, nil }
	m.events = func(context.Context, string) ([]core.ManualReminderEvent, error) {
		return []core.ManualReminderEvent{
			{ID: testID(41), PlanID: plan.ID, EventType: core.ManualEventPlanCreated, CreatedAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)},
			{ID: testID(42), PlanID: plan.ID, EventType: core.ManualEventPaymentConfirmed, OccurrenceID: testID(21), CreatedAt: time.Date(2026, 11, 15, 1, 0, 0, 0, time.UTC)},
			{ID: testID(44), PlanID: plan.ID, EventType: core.ManualEventReminderSent, CreatedAt: time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)},
		}, nil
	}
	env.withManual(m)
	text := sentText(env.runMessage("/reminder_history " + plan.ID))
	for _, want := range []string{"PLAN_CREATED", "PAYMENT_CONFIRMED", "REMINDER_SENT", testID(21)} {
		if !strings.Contains(text, want) {
			t.Fatalf("history missing %q: %q", want, text)
		}
	}
	if strings.Contains(text, "Dispatches") {
		t.Fatalf("history must render events only: %q", text)
	}
}

func TestReminderPauseResumeDelete(t *testing.T) {
	env := newTestEnv(t)
	m := newFakeManual()
	env.withManual(m)
	id := testID(20)
	if text := sentText(env.runMessage("/reminder_pause " + id)); !strings.Contains(text, "paused") {
		t.Fatalf("pause reply = %q", text)
	}
	env.tg.reset()
	if text := sentText(env.runMessage("/reminder_resume " + id)); !strings.Contains(text, "resumed") {
		t.Fatalf("resume reply = %q", text)
	}
	env.tg.reset()
	if text := sentText(env.runMessage("/reminder_delete " + id)); !strings.Contains(text, "cancelled") {
		t.Fatalf("delete reply = %q", text)
	}
	for _, call := range []string{"pause", "resume", "cancel"} {
		if !m.called(call) {
			t.Fatalf("service call %q not recorded: %v", call, m.calls)
		}
	}
}

func TestReminderPaidCommand(t *testing.T) {
	env := newTestEnv(t)
	m := newFakeManual()
	env.withManual(m)
	text := sentText(env.runMessage("/reminder_paid " + testID(21)))
	if !strings.Contains(text, "marked as paid") {
		t.Fatalf("paid reply = %q", text)
	}
	if !m.called("confirm") || m.lastActor != 7 {
		t.Fatalf("confirm not called with the authorized actor: %v actor=%d", m.calls, m.lastActor)
	}
}

func TestReminderEditPrefillsWizard(t *testing.T) {
	env := newTestEnv(t)
	m := manualPlanService()
	env.withManual(m)
	text := sentText(env.runMessage("/reminder_edit " + testID(20)))
	for _, want := range []string{"Review manual reminder", "VPS renewal", "18.00 USD", "Billing day: 15"} {
		if !strings.Contains(text, want) {
			t.Fatalf("edit preview missing %q: %q", want, text)
		}
	}
	if !hasCallbackPrefix(env.tg.callsFor("sendMessage"), cbWizardAdvance+":") {
		t.Fatal("edit preview has no confirm button")
	}
}

func TestManualCallbackPaidShowsPrompt(t *testing.T) {
	env := newTestEnv(t)
	m := manualPlanService()
	env.withManual(m)
	answers := env.runCallback(callbackData(cbManualPaid, testID(21)))
	if len(answers) != 1 || answers[0].Body["text"].(string) != "Confirm payment" {
		t.Fatalf("paid prompt answer = %v", answers)
	}
	if m.called("confirm") {
		t.Fatal("mp must not confirm before the second stage")
	}
	if env.tg.count("editMessageText") != 1 {
		t.Fatalf("prompt was not edited into the message: %d", env.tg.count("editMessageText"))
	}
	text := editedText(env.tg)
	for _, want := range []string{"VPS renewal", "2026-11-15", "18.00 USD", "Confirm"} {
		if !strings.Contains(text, want) {
			t.Fatalf("prompt missing %q: %q", want, text)
		}
	}
	calls := env.tg.callsFor("editMessageText")
	if !hasCallbackPrefix(calls, cbManualConfirm+":") {
		t.Fatal("prompt has no confirm button")
	}
	if !hasCallbackPrefix(calls, cbManualView+":") {
		t.Fatal("prompt has no cancel button")
	}
}

func TestManualCallbackConfirmEditsMessage(t *testing.T) {
	env := newTestEnv(t)
	m := manualPlanService()
	env.withManual(m)
	answers := env.runCallback(callbackData(cbManualConfirm, testID(21)))
	if len(answers) != 1 || answers[0].Body["text"].(string) != "Marked as paid" {
		t.Fatalf("confirm callback answer = %v", answers)
	}
	if !m.called("confirm") || m.lastActor != 7 {
		t.Fatalf("confirm not called with actor 7: %v", m.calls)
	}
	if env.tg.count("editMessageText") != 1 {
		t.Fatalf("message was not edited after confirm: %d", env.tg.count("editMessageText"))
	}
	if text := editedText(env.tg); !strings.Contains(text, "VPS renewal") {
		t.Fatalf("edited message missing plan title: %q", text)
	}
}

func TestManualCallbackUndoEditsMessage(t *testing.T) {
	env := newTestEnv(t)
	m := manualPlanService()
	env.withManual(m)
	answers := env.runCallback(callbackData(cbManualUndo, testID(21)))
	if len(answers) != 1 || answers[0].Body["text"].(string) != "Restored to pending" {
		t.Fatalf("undo callback answer = %v", answers)
	}
	if !m.called("revoke") {
		t.Fatalf("revoke not called: %v", m.calls)
	}
	if text := editedText(env.tg); !strings.Contains(text, "2026-11-15") {
		t.Fatalf("edited message missing restored occurrence: %q", text)
	}
}

func TestManualCallbackViewAndHistory(t *testing.T) {
	env := newTestEnv(t)
	plan := sampleManualPlan()
	m := manualPlanService()
	m.events = func(context.Context, string) ([]core.ManualReminderEvent, error) {
		return []core.ManualReminderEvent{{ID: testID(41), PlanID: plan.ID, EventType: core.ManualEventPlanCreated}}, nil
	}
	env.withManual(m)
	env.runCallback(callbackData(cbManualView, plan.ID))
	if text := editedText(env.tg); !strings.Contains(text, "VPS renewal") {
		t.Fatalf("view plan reply = %q", text)
	}
	env.tg.reset()
	env.runCallback(callbackData(cbManualHistory, plan.ID))
	if text := sentText(env.tg.callsFor("sendMessage")); !strings.Contains(text, "PLAN_CREATED") {
		t.Fatalf("history reply = %q", text)
	}
}

func TestManualCallbackDeniedForeignUser(t *testing.T) {
	env := newTestEnv(t)
	m := manualPlanService()
	env.withManual(m)
	cq := env.callback(callbackData(cbManualConfirm, testID(21)))
	cq.From.ID = 8
	env.bot.handleCallback(context.Background(), cq)
	answers := env.tg.callsFor("answerCallbackQuery")
	if len(answers) != 1 || answers[0].Body["text"].(string) != "Not authorized" {
		t.Fatalf("foreign user callback = %v", answers)
	}
	if m.called("confirm") {
		t.Fatal("foreign user reached the manual service")
	}
}

func TestManualCallbackDeniedForeignChat(t *testing.T) {
	env := newTestEnv(t)
	m := manualPlanService()
	env.withManual(m)
	cq := env.callback(callbackData(cbManualConfirm, testID(21)))
	cq.Message.Chat.ID = 99
	env.bot.handleCallback(context.Background(), cq)
	if got := env.tg.count("answerCallbackQuery"); got != 1 {
		t.Fatalf("foreign chat callback answers = %d", got)
	}
	if m.called("confirm") {
		t.Fatal("foreign chat reached the manual service")
	}
}

func TestManualCallbackEditFailureKeepsState(t *testing.T) {
	env := newTestEnv(t)
	m := manualPlanService()
	env.withManual(m)
	env.tg.setHandler("editMessageText", func(context.Context, map[string]any) (any, *tgFailure) {
		return nil, &tgFailure{code: 400, description: "message can't be edited"}
	})
	answers := env.runCallback(callbackData(cbManualConfirm, testID(21)))
	if len(answers) != 1 || answers[0].Body["text"].(string) != "Marked as paid" {
		t.Fatalf("answer after edit failure = %v", answers)
	}
	if !m.called("confirm") {
		t.Fatal("persisted confirmation was undone by an edit failure")
	}
	if env.tg.count("sendMessage") == 0 {
		t.Fatal("edit failure did not fall back to a fresh message")
	}
}

func TestManualCallbackPaymentErrorsReported(t *testing.T) {
	env := newTestEnv(t)
	m := manualPlanService()
	m.confirmOccurrence = func(context.Context, int64, string) (core.ManualOccurrence, error) {
		return core.ManualOccurrence{}, core.ErrConflict
	}
	env.withManual(m)
	answers := env.runCallback(callbackData(cbManualConfirm, testID(21)))
	if len(answers) != 1 || !strings.Contains(answers[0].Body["text"].(string), "current state") {
		t.Fatalf("conflict answer = %v", answers)
	}
}

func TestReminderPlanDetailEscapesHostileNotes(t *testing.T) {
	env := newTestEnv(t)
	plan := sampleManualPlan()
	plan.Title = `A&B<"x">`
	plan.Notes = `<img src=x onerror=alert(1)>`
	plan.Description = `<script>alert(1)</script>`
	plan.PaymentURL = "http://insecure.test/pay"
	occ := sampleManualOccurrence(core.ManualOccurrencePending)
	m := newFakeManual()
	m.getPlan = func(context.Context, string) (core.ManualPlan, error) { return plan, nil }
	m.listOccurrences = func(context.Context, core.ManualOccurrenceFilter) ([]core.ManualOccurrence, error) {
		return []core.ManualOccurrence{occ}, nil
	}
	env.withManual(m)
	text := sentText(env.runMessage("/reminder " + plan.ID))
	if !strings.Contains(text, "A&amp;B&lt;&#34;x&#34;&gt;") {
		t.Fatalf("title was not escaped: %q", text)
	}
	if !strings.Contains(text, "&lt;img src=x onerror=alert(1)&gt;") {
		t.Fatalf("notes were not escaped: %q", text)
	}
	if strings.Contains(text, "<img") || strings.Contains(text, "<script>") {
		t.Fatalf("hostile markup survived: %q", text)
	}
	if strings.Contains(text, "insecure.test") {
		t.Fatalf("untrusted payment URL rendered: %q", text)
	}
	if !strings.Contains(text, occ.ID) {
		t.Fatalf("plan detail hides the occurrence id: %q", text)
	}
	assertValidParts(t, []string{text})
}

func TestReminderPendingShowsOccurrenceIDs(t *testing.T) {
	env := newTestEnv(t)
	plan := sampleManualPlan()
	occ := sampleManualOccurrence(core.ManualOccurrencePending)
	m := newFakeManual()
	m.getPlan = func(context.Context, string) (core.ManualPlan, error) { return plan, nil }
	m.listOccurrences = func(context.Context, core.ManualOccurrenceFilter) ([]core.ManualOccurrence, error) {
		return []core.ManualOccurrence{occ}, nil
	}
	env.withManual(m)
	text := sentText(env.runMessage("/reminder_pending"))
	if !strings.Contains(text, occ.ID) {
		t.Fatalf("pending list hides the occurrence id: %q", text)
	}
}

func hasCallbackPrefix(calls []tgCall, prefix string) bool {
	for _, call := range calls {
		markup, ok := call.Body["reply_markup"].(map[string]any)
		if !ok {
			continue
		}
		rows, ok := markup["inline_keyboard"].([]any)
		if !ok {
			continue
		}
		for _, row := range rows {
			buttons, ok := row.([]any)
			if !ok {
				continue
			}
			for _, button := range buttons {
				entry, ok := button.(map[string]any)
				if !ok {
					continue
				}
				if data, ok := entry["callback_data"].(string); ok && strings.HasPrefix(data, prefix) {
					return true
				}
			}
		}
	}
	return false
}

func editedText(m *mockTelegram) string {
	call, ok := m.last("editMessageText")
	if !ok {
		return ""
	}
	text, _ := call.Body["text"].(string)
	return text
}
