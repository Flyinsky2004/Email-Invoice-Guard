package telegram

import (
	"context"
	"strings"
	"testing"
	"time"

	"email-invoice-guard/internal/core"
)

// lastWizardToken returns the wizard ID embedded in the most recent message
// keyboard.
func lastWizardToken(t *testing.T, env *testEnv) string {
	t.Helper()
	env.tg.mu.Lock()
	defer env.tg.mu.Unlock()
	for i := len(env.tg.calls) - 1; i >= 0; i-- {
		call := env.tg.calls[i]
		if call.Method != "sendMessage" && call.Method != "editMessageText" {
			continue
		}
		if token := tokenFromMarkup(call.Body["reply_markup"]); token != "" {
			return token
		}
	}
	t.Fatal("no wizard token in the latest keyboard")
	return ""
}

func tokenFromMarkup(markup any) string {
	root, ok := markup.(map[string]any)
	if !ok {
		return ""
	}
	rows, ok := root["inline_keyboard"].([]any)
	if !ok {
		return ""
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
			data, _ := entry["callback_data"].(string)
			parts := strings.Split(data, ":")
			if len(parts) < 2 {
				continue
			}
			switch parts[0] {
			case cbWizardAdvance, cbWizardBack, cbWizardCancel, cbWizardValue:
				return parts[1]
			}
		}
	}
	return ""
}

func wizardCount(env *testEnv) int {
	env.bot.wizardMu.Lock()
	defer env.bot.wizardMu.Unlock()
	return len(env.bot.wizards)
}

func wizardSend(t *testing.T, env *testEnv, text string) string {
	t.Helper()
	env.tg.reset()
	return sentText(env.runMessage(text))
}

func TestWizardCreateFlow(t *testing.T) {
	env := newTestEnv(t)
	m := newFakeManual()
	env.withManual(m)

	if text := wizardSend(t, env, "/reminder_add"); !strings.Contains(text, "Step 1/8") {
		t.Fatalf("wizard did not start: %q", text)
	}
	steps := []string{"VPS renewal", "Example Host", "18.00 USD", "15", "-", "-", "https://billing.example.test/pay/1", "monthly"}
	var text string
	for _, step := range steps {
		text = wizardSend(t, env, step)
	}
	if !strings.Contains(text, "Review manual reminder") || !strings.Contains(text, "VPS renewal") {
		t.Fatalf("confirm preview = %q", text)
	}
	token := lastWizardToken(t, env)
	answers := env.runCallback(callbackData(cbWizardAdvance, token))
	if len(answers) != 1 || answers[0].Body["text"].(string) != "Plan saved" {
		t.Fatalf("confirm answer = %v", answers)
	}
	if !m.called("create") {
		t.Fatalf("CreatePlan not called: %v", m.calls)
	}
	input := m.lastCreateInput
	if input.Title != "VPS renewal" || input.ProviderName != "Example Host" {
		t.Fatalf("unexpected input: %+v", input)
	}
	if input.Amount == nil || input.Amount.Minor != 1800 || input.Amount.Currency != "USD" {
		t.Fatalf("unexpected amount: %+v", input.Amount)
	}
	if input.BillingDay != 15 || input.ReminderTime != "09:00" || input.Timezone != "Asia/Taipei" {
		t.Fatalf("unexpected schedule fields: %+v", input)
	}
	if input.PaymentURL != "https://billing.example.test/pay/1" || input.Notes != "monthly" {
		t.Fatalf("unexpected optional fields: %+v", input)
	}
	if m.lastActor != 7 {
		t.Fatalf("actor = %d, want 7", m.lastActor)
	}
	if wizardCount(env) != 0 {
		t.Fatal("wizard state survived a successful confirmation")
	}
}

func TestWizardValidationFailures(t *testing.T) {
	env := newTestEnv(t)
	m := newFakeManual()
	env.withManual(m)
	wizardSend(t, env, "/reminder_add")

	cases := []struct {
		input string
		want  string
	}{
		{strings.Repeat("x", manualMaxTitle+1), "Title must be 1-200 characters."},
		{"VPS renewal", "Step 2/8"},
	}
	for _, tc := range cases {
		if text := wizardSend(t, env, tc.input); !strings.Contains(text, tc.want) {
			t.Fatalf("%q = %q, want %q", tc.input, text, tc.want)
		}
	}
	// Provider, then invalid amount forms.
	wizardSend(t, env, "-")
	if text := wizardSend(t, env, "abc"); !strings.Contains(text, "Send an amount") {
		t.Fatalf("invalid amount accepted: %q", text)
	}
	if text := wizardSend(t, env, "18.00 XYZ"); !strings.Contains(text, "Invalid amount") {
		t.Fatalf("unsupported currency accepted: %q", text)
	}
	wizardSend(t, env, "18.00 USD")
	if text := wizardSend(t, env, "0"); !strings.Contains(text, "1 to 31") {
		t.Fatalf("billing day 0 accepted: %q", text)
	}
	if text := wizardSend(t, env, "32"); !strings.Contains(text, "1 to 31") {
		t.Fatalf("billing day 32 accepted: %q", text)
	}
	wizardSend(t, env, "15")
	if text := wizardSend(t, env, "25:00"); !strings.Contains(text, "HH:MM") {
		t.Fatalf("invalid time accepted: %q", text)
	}
	wizardSend(t, env, "-")
	if text := wizardSend(t, env, "Mars/Base"); !strings.Contains(text, "Invalid timezone") {
		t.Fatalf("invalid timezone accepted: %q", text)
	}
	wizardSend(t, env, "-")
	if text := wizardSend(t, env, "http://insecure.test/pay"); !strings.Contains(text, "https URL") {
		t.Fatalf("insecure URL accepted: %q", text)
	}
	if text := wizardSend(t, env, "https://user:"+"pass"+"@host.test/pay"); !strings.Contains(text, "https URL") {
		t.Fatalf("URL with credentials accepted: %q", text)
	}
	wizardSend(t, env, "-")
	if text := wizardSend(t, env, strings.Repeat("n", manualMaxNotes+1)); !strings.Contains(text, "2000 characters") {
		t.Fatalf("oversized notes accepted: %q", text)
	}
	if m.called("create") {
		t.Fatal("invalid wizard created a plan")
	}
}

func TestWizardTTLExpiry(t *testing.T) {
	env := newTestEnv(t)
	env.cfg.Manual.WizardTTL = time.Minute
	m := newFakeManual()
	env.withManual(m)
	wizardSend(t, env, "/reminder_add")

	env.bot.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	text := wizardSend(t, env, "VPS renewal")
	if !strings.Contains(text, "Wizard expired") {
		t.Fatalf("expired wizard resumed: %q", text)
	}
	if wizardCount(env) != 0 {
		t.Fatal("expired wizard state was retained")
	}
	if m.called("create") {
		t.Fatal("expired wizard created a plan")
	}
}

func TestWizardCancelCommandAndCallback(t *testing.T) {
	env := newTestEnv(t)
	m := newFakeManual()
	env.withManual(m)

	wizardSend(t, env, "/reminder_add")
	if text := wizardSend(t, env, "/cancel"); !strings.Contains(text, "Wizard cancelled.") {
		t.Fatalf("cancel command = %q", text)
	}
	if wizardCount(env) != 0 {
		t.Fatal("cancel command left wizard state")
	}
	if text := wizardSend(t, env, "/cancel"); !strings.Contains(text, "No active wizard.") {
		t.Fatalf("second cancel = %q", text)
	}

	wizardSend(t, env, "/reminder_add")
	wizardSend(t, env, "VPS renewal")
	token := lastWizardToken(t, env)
	answers := env.runCallback(callbackData(cbWizardCancel, token))
	if len(answers) != 1 || answers[0].Body["text"].(string) != "Wizard cancelled" {
		t.Fatalf("cancel callback = %v", answers)
	}
	if wizardCount(env) != 0 {
		t.Fatal("cancel callback left wizard state")
	}
}

func TestWizardBack(t *testing.T) {
	env := newTestEnv(t)
	env.withManual(newFakeManual())
	wizardSend(t, env, "/reminder_add")
	wizardSend(t, env, "VPS renewal")
	token := lastWizardToken(t, env)
	answers := env.runCallback(callbackData(cbWizardBack, token))
	if len(answers) != 1 || answers[0].Body["text"].(string) != "Back" {
		t.Fatalf("back callback = %v", answers)
	}
	if text := editedText(env.tg); !strings.Contains(text, "Step 1/8") {
		t.Fatalf("back did not return to the title step: %q", text)
	}
	if text := wizardSend(t, env, "New title"); !strings.Contains(text, "Step 2/8") {
		t.Fatalf("wizard did not continue after back: %q", text)
	}
}

func TestWizardCurrencySelection(t *testing.T) {
	env := newTestEnv(t)
	m := newFakeManual()
	env.withManual(m)
	wizardSend(t, env, "/reminder_add")
	wizardSend(t, env, "VPS renewal")
	wizardSend(t, env, "-")
	if text := wizardSend(t, env, "18.00"); !strings.Contains(text, "select the currency") {
		t.Fatalf("currency step not reached: %q", text)
	}
	token := lastWizardToken(t, env)
	answers := env.runCallback(callbackData(cbWizardValue, token) + ":USD")
	if len(answers) != 1 || answers[0].Body["text"].(string) != "Selected USD" {
		t.Fatalf("currency selection = %v", answers)
	}
	if text := editedText(env.tg); !strings.Contains(text, "Step 4/8") {
		t.Fatalf("currency selection did not advance: %q", text)
	}
	for _, step := range []string{"15", "-", "-", "-", "-"} {
		wizardSend(t, env, step)
	}
	token = lastWizardToken(t, env)
	env.runCallback(callbackData(cbWizardAdvance, token))
	if !m.called("create") {
		t.Fatalf("CreatePlan not called: %v", m.calls)
	}
	if m.lastCreateInput.Amount == nil || m.lastCreateInput.Amount.Minor != 1800 || m.lastCreateInput.Amount.Currency != "USD" {
		t.Fatalf("currency selection lost: %+v", m.lastCreateInput.Amount)
	}
}

func TestWizardCallbackDataFitsTelegramLimit(t *testing.T) {
	env := newTestEnv(t)
	env.withManual(newFakeManual())
	wizardSend(t, env, "/reminder_add")
	wizardSend(t, env, "VPS renewal")
	wizardSend(t, env, "-")
	wizardSend(t, env, "18.00")
	wizardSend(t, env, "15")
	wizardSend(t, env, "-")
	wizardSend(t, env, "-")
	wizardSend(t, env, "-")
	wizardSend(t, env, "-")

	env.tg.mu.Lock()
	defer env.tg.mu.Unlock()
	seen := 0
	for _, call := range env.tg.calls {
		for _, data := range callbackDataValues(call.Body["reply_markup"]) {
			seen++
			if len(data) > 64 {
				t.Fatalf("wizard callback data %q is %d bytes", data, len(data))
			}
			if _, _, _, ok := parseWizardCallback(data); !ok {
				t.Fatalf("wizard callback data did not round trip: %q", data)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no wizard callback data collected")
	}
}

func callbackDataValues(markup any) []string {
	root, ok := markup.(map[string]any)
	if !ok {
		return nil
	}
	rows, ok := root["inline_keyboard"].([]any)
	if !ok {
		return nil
	}
	var out []string
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
			if data, ok := entry["callback_data"].(string); ok {
				out = append(out, data)
			}
		}
	}
	return out
}

func TestWizardStaleTokenRejected(t *testing.T) {
	env := newTestEnv(t)
	env.withManual(newFakeManual())
	wizardSend(t, env, "/reminder_add")
	wizardSend(t, env, "First title")
	oldToken := lastWizardToken(t, env)
	wizardSend(t, env, "/reminder_add")
	wizardSend(t, env, "Second title")

	answers := env.runCallback(callbackData(cbWizardBack, oldToken))
	if len(answers) != 1 || !strings.Contains(answers[0].Body["text"].(string), "no longer active") {
		t.Fatalf("stale token answer = %v", answers)
	}
	if wizardCount(env) != 1 {
		t.Fatal("stale callback removed the active wizard")
	}
}

func TestWizardExpiredCallback(t *testing.T) {
	env := newTestEnv(t)
	env.cfg.Manual.WizardTTL = time.Minute
	env.withManual(newFakeManual())
	wizardSend(t, env, "/reminder_add")
	token := lastWizardToken(t, env)
	env.bot.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	answers := env.runCallback(callbackData(cbWizardAdvance, token))
	if len(answers) != 1 || answers[0].Body["text"].(string) != "Wizard expired" {
		t.Fatalf("expired callback answer = %v", answers)
	}
	if wizardCount(env) != 0 {
		t.Fatal("expired callback retained wizard state")
	}
}

func TestWizardEditConfirmUpdates(t *testing.T) {
	env := newTestEnv(t)
	plan := sampleManualPlan()
	plan.AssetID = testID(50)
	plan.Description = "Primary web server"
	m := manualPlanService()
	m.getPlan = func(context.Context, string) (core.ManualPlan, error) { return plan, nil }
	env.withManual(m)
	wizardSend(t, env, "/reminder_edit "+plan.ID)
	token := lastWizardToken(t, env)
	answers := env.runCallback(callbackData(cbWizardAdvance, token))
	if len(answers) != 1 || answers[0].Body["text"].(string) != "Plan saved" {
		t.Fatalf("edit confirm answer = %v", answers)
	}
	if !m.called("update") {
		t.Fatalf("UpdatePlan not called: %v", m.calls)
	}
	if m.lastID != plan.ID {
		t.Fatalf("updated plan = %q, want %q", m.lastID, plan.ID)
	}
	if m.lastUpdateInput.Title != plan.Title || m.lastUpdateInput.BillingDay != plan.BillingDay {
		t.Fatalf("edit lost prefilled values: %+v", m.lastUpdateInput)
	}
	if m.lastUpdateInput.AssetID != plan.AssetID || m.lastUpdateInput.Description != plan.Description {
		t.Fatalf("edit lost non-wizard fields: %+v", m.lastUpdateInput)
	}
}

func TestWizardRejectsCallbackAtTextStep(t *testing.T) {
	env := newTestEnv(t)
	env.withManual(newFakeManual())
	wizardSend(t, env, "/reminder_add")
	token := lastWizardToken(t, env)
	answers := env.runCallback(callbackData(cbWizardAdvance, token))
	if len(answers) != 1 || !strings.Contains(answers[0].Body["text"].(string), "text prompt") {
		t.Fatalf("advance at text step = %v", answers)
	}
}
