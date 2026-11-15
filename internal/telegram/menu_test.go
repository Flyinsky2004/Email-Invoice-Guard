package telegram

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"email-invoice-guard/internal/core"
)

func TestStartShowsMenu(t *testing.T) {
	env := newTestEnv(t)
	calls := env.runMessage("/start")
	text := sentText(calls)
	if !strings.Contains(text, "Invoice Guard") || !strings.Contains(text, "Choose an action") {
		t.Fatalf("unexpected menu: %q", text)
	}
	raw, ok := markupJSON(calls)
	if !ok {
		t.Fatal("menu keyboard missing")
	}
	for _, want := range []string{"mn:mails", "mn:invoices:all", "mn:accounts", "mn:settings", "mn:sync", "mn:help"} {
		if !strings.Contains(raw, want) {
			t.Errorf("menu missing %q: %s", want, raw)
		}
	}
}

func TestMenuAccountsNavigationEditsBubble(t *testing.T) {
	env := newTestEnv(t)
	env.store.listAccounts = func(context.Context) ([]core.MailAccount, error) {
		return []core.MailAccount{sampleAccount()}, nil
	}
	env.runCallback(menuCallbackData(menuAccounts, "", ""))
	edits := env.tg.callsFor("editMessageText")
	if len(edits) != 1 {
		t.Fatalf("editMessageText calls = %d, want 1", len(edits))
	}
	text, _ := edits[0].Body["text"].(string)
	if !strings.Contains(text, "owner@example.com") {
		t.Fatalf("account list incomplete: %q", text)
	}
	if id, _ := edits[0].Body["message_id"].(float64); int64(id) != 9 {
		t.Fatalf("menu edited message %v, want 9", edits[0].Body["message_id"])
	}
	raw, _ := json.Marshal(edits[0].Body["reply_markup"])
	for _, want := range []string{"mn:folders:" + testID(2), "mn:remove:" + testID(2), "mn:home"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("accounts keyboard missing %q: %s", want, raw)
		}
	}
}

func TestMenuInvoiceFilter(t *testing.T) {
	env := newTestEnv(t)
	var got core.InvoiceFilter
	env.store.listInvoices = func(_ context.Context, filter core.InvoiceFilter) ([]core.Invoice, error) {
		got = filter
		return []core.Invoice{sampleInvoice()}, nil
	}
	env.runCallback(menuCallbackData(menuInvoices, "pending", ""))
	if got.Status != core.PendingConfirmation {
		t.Fatalf("filter status = %q, want pending", got.Status)
	}
	text := editedText(env.tg)
	if !strings.Contains(text, "INV-2026-001") {
		t.Fatalf("invoice list missing invoice: %q", text)
	}
}

func TestMenuSettingsAndLanguage(t *testing.T) {
	env := newTestEnv(t)
	env.runCallback(menuCallbackData(menuSettings, "", ""))
	text := editedText(env.tg)
	for _, want := range []string{"timezone: Asia/Taipei", "reminder_time: 09:00", "sync_interval_seconds: 3600"} {
		if !strings.Contains(text, want) {
			t.Fatalf("settings missing %q: %q", want, text)
		}
	}

	env.tg.reset()
	env.runCallback(menuCallbackData(menuLanguage, "zh-CN", ""))
	if v, _ := env.store.setting(settingLanguage); v != string(langZH) {
		t.Fatalf("language not persisted: %q", v)
	}
	text = editedText(env.tg)
	if !strings.Contains(text, "设置") {
		t.Fatalf("settings did not re-render in Chinese: %q", text)
	}
}

func TestMenuSearchUsesSameBubble(t *testing.T) {
	env := newTestEnv(t)
	env.runCallback(menuCallbackData(menuSearch, "", ""))
	text := editedText(env.tg)
	if !strings.Contains(text, "Send the text to search for") {
		t.Fatalf("search prompt missing: %q", text)
	}

	env.tg.reset()
	env.store.listEmails = func(_ context.Context, filter core.EmailFilter) ([]core.NormalizedEmail, error) {
		if filter.Search != "invoice" || filter.Limit != listLimit {
			t.Fatalf("search filter = %+v", filter)
		}
		return []core.NormalizedEmail{sampleEmail()}, nil
	}
	env.runMessage("invoice")
	edits := env.tg.callsFor("editMessageText")
	if len(edits) != 1 {
		t.Fatalf("search result edits = %d, want 1", len(edits))
	}
	if id, _ := edits[0].Body["message_id"].(float64); int64(id) != 9 {
		t.Fatalf("search result edited message %v, want the prompt message 9", edits[0].Body["message_id"])
	}
	text, _ = edits[0].Body["text"].(string)
	if !strings.Contains(text, sampleEmail().Subject) {
		t.Fatalf("search results missing mail: %q", text)
	}
	if env.bot.menuInputState(42, 7) != nil {
		t.Fatal("search input state was not cleared")
	}
}

func TestMenuTimezoneInputValidation(t *testing.T) {
	env := newTestEnv(t)
	env.runCallback(menuCallbackData(menuTimezone, "", ""))
	env.tg.reset()

	env.runMessage("Not/AZone")
	text := editedText(env.tg)
	if !strings.Contains(text, "Invalid timezone") {
		t.Fatalf("invalid timezone not reported: %q", text)
	}
	if env.bot.menuInputState(42, 7) == nil {
		t.Fatal("invalid input cleared the prompt state")
	}

	env.tg.reset()
	env.runMessage("Asia/Tokyo")
	if v, _ := env.store.setting(settingTimezone); v != "Asia/Tokyo" {
		t.Fatalf("timezone not persisted: %q", v)
	}
	text = editedText(env.tg)
	if !strings.Contains(text, "timezone: Asia/Tokyo") {
		t.Fatalf("settings did not refresh: %q", text)
	}
	if env.bot.menuInputState(42, 7) != nil {
		t.Fatal("valid input did not clear the prompt state")
	}
}

func TestMenuFolderToggle(t *testing.T) {
	env := newTestEnv(t)
	account := sampleAccount()
	env.store.getAccount = func(context.Context, string) (core.MailAccount, error) { return account, nil }
	folders := []core.MailFolder{{ID: "INBOX", AccountID: account.ID, Name: "Inbox", Enabled: true}}
	env.store.listFolders = func(context.Context, string) ([]core.MailFolder, error) { return folders, nil }
	var saved core.MailFolder
	env.store.saveFolder = func(_ context.Context, folder core.MailFolder) error {
		saved = folder
		folders[0] = folder
		return nil
	}

	env.runCallback(menuCallbackData(menuFolders, account.ID, ""))
	text := editedText(env.tg)
	if !strings.Contains(text, "id=INBOX") {
		t.Fatalf("folder list missing folder: %q", text)
	}

	env.tg.reset()
	env.runCallback(menuCallbackData(menuFolderToggle, account.ID, folderToken("INBOX")))
	if saved.Enabled {
		t.Fatal("folder was not disabled")
	}
	text = editedText(env.tg)
	if !strings.Contains(text, "disabled") {
		t.Fatalf("folder view did not refresh: %q", text)
	}
}

func TestMenuFolderToggleSurvivesReordering(t *testing.T) {
	env := newTestEnv(t)
	account := sampleAccount()
	env.store.getAccount = func(context.Context, string) (core.MailAccount, error) { return account, nil }
	folders := []core.MailFolder{
		{ID: "Archive", Name: "Archive", Enabled: true},
		{ID: "INBOX", Name: "Inbox", Enabled: true},
	}
	env.store.listFolders = func(context.Context, string) ([]core.MailFolder, error) { return folders, nil }
	var saved core.MailFolder
	env.store.saveFolder = func(_ context.Context, folder core.MailFolder) error {
		saved = folder
		for i := range folders {
			if folders[i].ID == folder.ID {
				folders[i] = folder
			}
		}
		return nil
	}

	// Render the list, then reorder it before the button is pressed. A stable
	// folder token must still target INBOX.
	env.runCallback(menuCallbackData(menuFolders, account.ID, ""))
	folders[0], folders[1] = folders[1], folders[0]
	env.tg.reset()
	env.runCallback(menuCallbackData(menuFolderToggle, account.ID, folderToken("INBOX")))
	if saved.ID != "INBOX" || saved.Enabled {
		t.Fatalf("wrong folder toggled: %+v", saved)
	}
}

func TestMenuFolderButtonLabelsAreBounded(t *testing.T) {
	env := newTestEnv(t)
	account := sampleAccount()
	env.store.getAccount = func(context.Context, string) (core.MailAccount, error) { return account, nil }
	env.store.listFolders = func(context.Context, string) ([]core.MailFolder, error) {
		return []core.MailFolder{{ID: "INBOX", Name: strings.Repeat("测", 120), Enabled: true}}, nil
	}
	env.runCallback(menuCallbackData(menuFolders, account.ID, ""))
	edits := env.tg.callsFor("editMessageText")
	if len(edits) != 1 {
		t.Fatalf("editMessageText calls = %d, want 1", len(edits))
	}
	markup, _ := edits[0].Body["reply_markup"].(map[string]any)
	rows, _ := markup["inline_keyboard"].([]any)
	for _, row := range rows {
		buttons, _ := row.([]any)
		for _, b := range buttons {
			btn, _ := b.(map[string]any)
			if text, ok := btn["text"].(string); ok && len(text) > 64 {
				t.Errorf("button text is %d bytes: %q", len(text), text)
			}
		}
	}
}

func TestMenuPromptClearedByLegacyCallback(t *testing.T) {
	env := newTestEnv(t)
	env.runCallback(menuCallbackData(menuSearch, "", ""))
	if env.bot.menuInputState(42, 7) == nil {
		t.Fatal("search prompt state missing")
	}
	env.store.getInvoice = func(context.Context, string) (core.Invoice, error) { return sampleInvoice(), nil }
	env.runCallback(callbackData(cbPaidPrompt, sampleInvoice().ID))
	if env.bot.menuInputState(42, 7) != nil {
		t.Fatal("legacy callback did not clear the menu prompt")
	}
	env.tg.reset()
	env.runMessage("invoice")
	if got := env.tg.count("editMessageText"); got != 0 {
		t.Fatalf("stale prompt consumed text: edits=%d", got)
	}
}

func TestMenuPromptExpires(t *testing.T) {
	env := newTestEnv(t)
	env.runCallback(menuCallbackData(menuSearch, "", ""))
	env.bot.now = func() time.Time { return time.Now().Add(11 * time.Minute) }
	env.tg.reset()
	env.runMessage("invoice")
	text := editedText(env.tg)
	if !strings.Contains(text, "Input expired") {
		t.Fatalf("expired prompt not reported: %q", text)
	}
	if env.bot.menuInputState(42, 7) != nil {
		t.Fatal("expired prompt was not cleared")
	}
}

func TestMenuSearchWithManualService(t *testing.T) {
	env := newTestEnv(t)
	env.withManual(newFakeManual())
	env.runCallback(menuCallbackData(menuSearch, "", ""))
	env.store.listEmails = func(context.Context, core.EmailFilter) ([]core.NormalizedEmail, error) {
		return []core.NormalizedEmail{sampleEmail()}, nil
	}
	env.tg.reset()
	env.runMessage("invoice")
	if !strings.Contains(editedText(env.tg), sampleEmail().Subject) {
		t.Fatal("search input was not handled with a manual service present")
	}
}

func TestMenuRemoveAccount(t *testing.T) {
	env := newTestEnv(t)
	account := sampleAccount()
	env.store.getAccount = func(context.Context, string) (core.MailAccount, error) { return account, nil }
	var removed string
	env.store.removeAccount = func(_ context.Context, id string) error { removed = id; return nil }

	env.runCallback(menuCallbackData(menuRemove, account.ID, ""))
	text := editedText(env.tg)
	if !strings.Contains(text, "Disable account") {
		t.Fatalf("confirmation prompt missing: %q", text)
	}
	if removed != "" {
		t.Fatal("account removed before confirmation")
	}

	env.tg.reset()
	env.runCallback(menuCallbackData(menuRemoveDo, account.ID, ""))
	if removed != account.ID {
		t.Fatalf("account not removed: %q", removed)
	}
}

func TestMenuMutationsRejectForeignOwner(t *testing.T) {
	env := newTestEnv(t)
	foreign := core.MailAccount{ID: testID(2), Address: "other@example.com", OwnerUserID: 99}
	env.store.getAccount = func(context.Context, string) (core.MailAccount, error) { return foreign, nil }
	env.store.removeAccount = func(context.Context, string) error {
		t.Fatal("foreign account removal attempted")
		return nil
	}
	env.store.saveFolder = func(context.Context, core.MailFolder) error {
		t.Fatal("foreign folder saved")
		return nil
	}

	env.runCallback(menuCallbackData(menuRemoveDo, foreign.ID, ""))
	if text := editedText(env.tg); !strings.Contains(text, "Account not found") {
		t.Fatalf("foreign removal not rejected: %q", text)
	}

	env.tg.reset()
	env.runCallback(menuCallbackData(menuFolderToggle, foreign.ID, folderToken("INBOX")))
	if text := editedText(env.tg); !strings.Contains(text, "Account not found") {
		t.Fatalf("foreign folder toggle not rejected: %q", text)
	}
}

func TestMenuReminderPause(t *testing.T) {
	env := newTestEnv(t)
	m := manualPlanService()
	plan := sampleManualPlan()
	m.listPlans = func(context.Context, core.ManualPlanFilter) ([]core.ManualPlan, error) {
		return []core.ManualPlan{plan}, nil
	}
	env.withManual(m)

	env.runCallback(menuCallbackData(menuReminders, "", ""))
	text := editedText(env.tg)
	if !strings.Contains(text, plan.Title) {
		t.Fatalf("reminder list missing plan: %q", text)
	}

	env.tg.reset()
	answers := env.runCallback(menuCallbackData(menuReminderPause, plan.ID, ""))
	if !m.called("pause") {
		t.Fatalf("PausePlan not called: %v", m.calls)
	}
	if len(answers) != 1 {
		t.Fatalf("answers = %d, want 1", len(answers))
	}
	if text, _ := answers[0].Body["text"].(string); !strings.Contains(text, "Plan paused") {
		t.Fatalf("unexpected pause answer: %q", text)
	}
}

func TestMenuInvoiceFilters(t *testing.T) {
	env := newTestEnv(t)
	var got core.InvoiceFilter
	env.store.listInvoices = func(_ context.Context, filter core.InvoiceFilter) ([]core.Invoice, error) {
		got = filter
		return nil, nil
	}
	cases := []struct {
		param  string
		status core.InvoiceStatus
	}{
		{"pending", core.PendingConfirmation},
		{"paid", core.PaidConfirmed},
		{"review", core.ReviewRequired},
		{"overdue", core.PendingConfirmation},
		{"all", ""},
	}
	for _, tc := range cases {
		env.tg.reset()
		env.runCallback(menuCallbackData(menuInvoices, tc.param, ""))
		if got.Status != tc.status {
			t.Errorf("%s status = %q, want %q", tc.param, got.Status, tc.status)
		}
		if editedText(env.tg) == "" {
			t.Errorf("%s view rendered no text", tc.param)
		}
	}

	env.tg.reset()
	env.runCallback(menuCallbackData(menuInvoices, "bogus", ""))
	if !strings.Contains(editedText(env.tg), "Usage") {
		t.Fatal("invalid filter was not rejected")
	}
}

func TestMenuFoldersEmpty(t *testing.T) {
	env := newTestEnv(t)
	env.runCallback(menuCallbackData(menuFolders, "", ""))
	text := editedText(env.tg)
	if !strings.Contains(text, "Choose an account") {
		t.Fatalf("empty account picker missing: %q", text)
	}

	account := sampleAccount()
	env.store.getAccount = func(context.Context, string) (core.MailAccount, error) { return account, nil }
	env.store.listFolders = func(context.Context, string) ([]core.MailFolder, error) { return nil, nil }
	env.tg.reset()
	env.runCallback(menuCallbackData(menuFolders, account.ID, ""))
	text = editedText(env.tg)
	if !strings.Contains(text, "Run /sync first") {
		t.Fatalf("empty folder list does not point at sync: %q", text)
	}
}

func TestMenuReminderResumeAndDelete(t *testing.T) {
	env := newTestEnv(t)
	m := manualPlanService()
	env.withManual(m)
	plan := sampleManualPlan()

	env.runCallback(menuCallbackData(menuReminderResume, plan.ID, ""))
	if !m.called("resume") {
		t.Fatalf("ResumePlan not called: %v", m.calls)
	}

	env.tg.reset()
	env.runCallback(menuCallbackData(menuReminderDeleteDo, plan.ID, ""))
	if !m.called("cancel") {
		t.Fatalf("CancelPlan not called: %v", m.calls)
	}
}

func TestMenuReadOnlyViewsRender(t *testing.T) {
	env := newTestEnv(t)
	env.store.listEmails = func(context.Context, core.EmailFilter) ([]core.NormalizedEmail, error) {
		return []core.NormalizedEmail{sampleEmail()}, nil
	}
	env.store.getEmail = func(context.Context, string) (core.NormalizedEmail, error) {
		return sampleEmail(), nil
	}
	env.store.getAccount = func(context.Context, string) (core.MailAccount, error) {
		return sampleAccount(), nil
	}
	env.store.getInvoice = func(context.Context, string) (core.Invoice, error) {
		return sampleInvoice(), nil
	}
	env.store.listAssets = func(context.Context, core.AssetFilter) ([]core.Asset, error) {
		return []core.Asset{{ID: testID(4), ProviderID: "rfchost"}}, nil
	}
	env.store.assetInvoices = func(context.Context, string) ([]core.Invoice, error) {
		return []core.Invoice{sampleInvoice()}, nil
	}
	env.store.status = func(context.Context) (core.SystemStatus, error) {
		return core.SystemStatus{Accounts: 1, Emails: 2, PendingInvoices: 3, PendingDeliveries: 4}, nil
	}
	views := []struct {
		name string
		data string
		want string
	}{
		{"mails", menuCallbackData(menuMails, "", ""), "Latest mail"},
		{"mail", menuCallbackData(menuMail, testID(1), ""), "Your invoice INV-100"},
		{"invoice", menuCallbackData(menuInvoice, testID(3), ""), "INV-2026-001"},
		{"assets", menuCallbackData(menuAssets, "", ""), "rfchost"},
		{"asset history", menuCallbackData(menuAssetHistory, testID(4), ""), "INV-2026-001"},
		{"status", menuCallbackData(menuStatus, "", ""), "Accounts: 1"},
		{"providers", menuCallbackData(menuProviders, "", ""), "RFCHOST v1"},
	}
	for _, tc := range views {
		env.tg.reset()
		env.runCallback(tc.data)
		text := editedText(env.tg)
		if !strings.Contains(text, tc.want) {
			t.Errorf("%s view missing %q: %q", tc.name, tc.want, text)
		}
	}
}

func TestMenuReminderHistoryAndPending(t *testing.T) {
	env := newTestEnv(t)
	m := manualPlanService()
	m.events = func(context.Context, string) ([]core.ManualReminderEvent, error) {
		return []core.ManualReminderEvent{{
			EventType: "REMINDER_SENT",
			CreatedAt: time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC),
		}}, nil
	}
	env.withManual(m)
	plan := sampleManualPlan()

	env.runCallback(menuCallbackData(menuReminderHistory, plan.ID, ""))
	text := editedText(env.tg)
	if !strings.Contains(text, "Manual reminder history") || !strings.Contains(text, "REMINDER_SENT") {
		t.Fatalf("history view incomplete: %q", text)
	}

	env.tg.reset()
	env.runCallback(menuCallbackData(menuPending, "", ""))
	text = editedText(env.tg)
	if !strings.Contains(text, "Pending manual occurrences") {
		t.Fatalf("pending view incomplete: %q", text)
	}
}

func TestMenuAddAccount(t *testing.T) {
	env := newTestEnv(t)
	onboarding := &fakeOnboarding{url: "https://example.com/oauth/start?token=abc"}
	env.onboarding[core.Microsoft] = onboarding
	env.rebuild()
	env.runCallback(menuCallbackData(menuAdd, "microsoft", ""))
	text := editedText(env.tg)
	if !strings.Contains(text, "https://example.com/oauth/start?token=abc") {
		t.Fatalf("onboarding link missing: %q", text)
	}
	onboarding.mu.Lock()
	actor := onboarding.actor
	onboarding.mu.Unlock()
	if actor != 7 {
		t.Fatalf("onboarding actor = %d, want 7", actor)
	}
}

func TestMenuStartsWizardInPlace(t *testing.T) {
	env := newTestEnv(t)
	env.withManual(newFakeManual())
	env.runCallback(menuCallbackData(menuReminderNew, "", ""))
	edits := env.tg.callsFor("editMessageText")
	if len(edits) != 1 {
		t.Fatalf("editMessageText calls = %d, want 1", len(edits))
	}
	text, _ := edits[0].Body["text"].(string)
	if !strings.Contains(text, "Step 1/10") {
		t.Fatalf("wizard prompt missing: %q", text)
	}
	raw, _ := json.Marshal(edits[0].Body["reply_markup"])
	if !strings.Contains(string(raw), "wx:") {
		t.Fatalf("wizard cancel button missing: %s", raw)
	}
}

func TestMenuReminderDeleteConfirmation(t *testing.T) {
	env := newTestEnv(t)
	m := manualPlanService()
	env.withManual(m)
	plan := sampleManualPlan()

	env.runCallback(menuCallbackData(menuReminderDelete, plan.ID, ""))
	text := editedText(env.tg)
	if !strings.Contains(text, "Cancel plan") {
		t.Fatalf("delete confirmation missing: %q", text)
	}
	if m.called("cancel") {
		t.Fatal("plan cancelled before confirmation")
	}

	env.tg.reset()
	env.runCallback(menuCallbackData(menuReminderDeleteDo, plan.ID, ""))
	if !m.called("cancel") {
		t.Fatalf("CancelPlan not called: %v", m.calls)
	}
}

func TestMenuSyncAction(t *testing.T) {
	env := newTestEnv(t)
	env.runCallback(menuCallbackData(menuSync, "", ""))
	text := editedText(env.tg)
	if !strings.Contains(text, "Sync started in the background") {
		t.Fatalf("unexpected sync reply: %q", text)
	}
	waitFor(t, 2*time.Second, func() bool {
		env.syncer.mu.Lock()
		defer env.syncer.mu.Unlock()
		return env.syncer.calls == 1
	})
}

func TestMenuHelpAndCommands(t *testing.T) {
	env := newTestEnv(t)
	env.runCallback(menuCallbackData(menuHelp, "", ""))
	text := editedText(env.tg)
	if !strings.Contains(text, "Use the buttons below") {
		t.Fatalf("help menu missing intro: %q", text)
	}
	env.tg.reset()
	env.runCallback(menuCallbackData(menuCommands, "", ""))
	text = editedText(env.tg)
	if !strings.Contains(text, "/add") || !strings.Contains(text, "/settings") {
		t.Fatalf("command reference incomplete: %q", text)
	}
}

func TestCommandReplyCarriesHomeButton(t *testing.T) {
	env := newTestEnv(t)
	calls := env.runMessage("/status")
	raw, ok := markupJSON(calls)
	if !ok || !strings.Contains(raw, "mn:home") {
		t.Fatalf("status reply has no Home button: %s", raw)
	}
}

func TestMenuHomeNotModifiedDoesNotDuplicate(t *testing.T) {
	env := newTestEnv(t)
	env.tg.setHandler("editMessageText", func(context.Context, map[string]any) (any, *tgFailure) {
		return nil, &tgFailure{code: 400, description: "Bad Request: message is not modified: specified new message content and reply markup are the same"}
	})
	env.runCallback(menuCallbackData(menuHome, "", ""))
	if got := env.tg.count("sendMessage"); got != 0 {
		t.Fatalf("duplicate message sent: %d", got)
	}
}

func TestMenuCallbackDataWithinLimit(t *testing.T) {
	id := testID(2)
	cases := []string{
		menuCallbackData(menuHome, "", ""),
		menuCallbackData(menuFolders, id, ""),
		menuCallbackData(menuFolderToggle, id, "12"),
		menuCallbackData(menuReminderHistory, id, ""),
		menuCallbackData(menuLanguage, "zh-CN", ""),
		menuCallbackData(menuReminderDeleteDo, id, ""),
	}
	for _, data := range cases {
		if len(data) > 64 {
			t.Errorf("%q is %d bytes, want <= 64", data, len(data))
		}
		if _, _, _, ok := parseMenuCallback(data); !ok {
			t.Errorf("%q was rejected", data)
		}
	}
}

func TestParseMenuCallbackRejectsBadData(t *testing.T) {
	for _, data := range []string{"", "mn", "mn:", "mn:bad view", "mn:home:bad param!", "xx:home", "mn:home:a:b:c"} {
		if _, _, _, ok := parseMenuCallback(data); ok {
			t.Errorf("%q was accepted", data)
		}
	}
}

func TestValidReminderTimeIsStrict(t *testing.T) {
	for _, ok := range []string{"00:00", "09:30", "23:59"} {
		if !validReminderTime(ok) {
			t.Errorf("%q rejected", ok)
		}
	}
	for _, bad := range []string{"9:30", "24:00", "12:60", "1230", "12:3", ""} {
		if validReminderTime(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func markupJSON(calls []tgCall) (string, bool) {
	if len(calls) == 0 {
		return "", false
	}
	markup, ok := calls[0].Body["reply_markup"].(map[string]any)
	if !ok {
		return "", false
	}
	raw, err := json.Marshal(markup)
	if err != nil {
		return "", false
	}
	return string(raw), true
}
